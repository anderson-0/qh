package postgres

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

// login is what a client sent to fakeServer on one connection attempt.
type login struct {
	user, password string
	sentPassword   bool
}

// fakeServer accepts Postgres connections, asks for a cleartext password,
// records what the client sends and refuses the login. No Docker needed.
func fakeServer(t *testing.T) (addr string, logins <-chan login) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan login, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(10 * time.Second))
				be := pgproto3.NewBackend(c, c)
				msg, err := be.ReceiveStartupMessage()
				if _, ok := msg.(*pgproto3.SSLRequest); ok && err == nil {
					c.Write([]byte("N"))
					msg, err = be.ReceiveStartupMessage()
				}
				sm, ok := msg.(*pgproto3.StartupMessage)
				if err != nil || !ok {
					return
				}
				l := login{user: sm.Parameters["user"]}
				be.Send(&pgproto3.AuthenticationCleartextPassword{})
				if be.Flush() != nil || be.SetAuthType(pgproto3.AuthTypeCleartextPassword) != nil {
					ch <- l
					return
				}
				if pm, err := be.Receive(); err == nil {
					if p, ok := pm.(*pgproto3.PasswordMessage); ok {
						l.password, l.sentPassword = p.Password, true
					}
				}
				be.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "28P01", Message: "password authentication failed"})
				be.Flush()
				ch <- l
			}()
		}
	}()
	return ln.Addr().String(), ch
}

// dialTo is a server-mode Dial that sends every connection to addr.
func dialTo(addr string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
}

// firstLogin opens spec and returns the first login the fake server saw.
func firstLogin(t *testing.T, spec engine.ConnSpec, logins <-chan login) login {
	t.Helper()
	if _, err := (Engine{}).Open(context.Background(), spec); err == nil {
		t.Fatal("open succeeded against the fake server")
	}
	select {
	case l := <-logins:
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("no login reached the fake server")
		return login{}
	}
}

var fileKeys = []string{"passfile", "service", "servicefile", "sslcert", "sslkey", "sslrootcert"}

func TestServerDSNRejectsFileKeys(t *testing.T) {
	for _, key := range fileKeys {
		for _, dsn := range []string{
			"postgres://u:hunter22@db.example.com/app?sslmode=disable&" + key + "=/etc/hosts",
			"host=db.example.com user=u password=hunter22 dbname=app " + key + "=/etc/hosts",
		} {
			check := Engine{}.CheckServerDSN(dsn)
			dialed := false
			_, open := Engine{}.Open(context.Background(), engine.ConnSpec{ID: "x", DSN: dsn, Mode: engine.ModeRO, PoolSize: 1, Server: true,
				Dial: func(context.Context, string, string) (net.Conn, error) {
					dialed = true
					return nil, net.ErrClosed
				}})
			for name, err := range map[string]error{"check": check, "open": open} {
				if err == nil || qerr.From(err).Category != qerr.Invalid ||
					!strings.HasPrefix(err.Error(), "error[invalid]: this server only accepts these Postgres connection settings: host, port, dbname, user, password") ||
					strings.Contains(err.Error(), "hunter22") || strings.Contains(err.Error(), "/etc/hosts") || strings.Contains(err.Error(), "db.example.com") {
					t.Errorf("%s %q: %v", name, dsn, err)
				}
			}
			if dialed {
				t.Errorf("%q: dialed despite a refused setting", dsn)
			}
		}
	}
}

func TestServerDSNAcceptsAllowedKeys(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:pw@db.example.com:5433/app?sslmode=require&sslsni=1&connect_timeout=5&target_session_attrs=any&application_name=x&channel_binding=prefer&require_auth=password&min_protocol_version=3.0&max_protocol_version=3.0&sslnegotiation=postgres",
		"postgres://u@db.example.com/app?ssl=true&dbname=other",
		"host=db.example.com port=5432 dbname=app user=u password='p w' sslmode=verify-full",
		// A trailing backslash escapes one following space; the settings qh
		// appends must survive it.
		`host=db.example.com user=victim password='' sslmode=disable application_name=x\`,
		// pgconn accepts a URL whose query ends in a separator.
		"postgres://u@db.example.com/app?sslmode=disable&",
		"postgres://u@db.example.com/app?",
	} {
		if err := (Engine{}).CheckServerDSN(dsn); err != nil {
			t.Errorf("%q: %v", dsn, err)
		}
	}
	for _, dsn := range []string{
		"postgres://u@db.example.com/app?pool_max_conns=50",
		`host=db.example.com user=u password=hunter22 passfile=/x application_name=x\`,
		`host=db.example.com user=u passfile=/x\`,
		// A swallowed sentinel leaves the allow-list verdict in place.
		"host=db.example.com user=u passfile=/x application_name=",
		"host=db.example.com user=u sslrootcert=/etc/hosts application_name=\t",
	} {
		if err := (Engine{}).CheckServerDSN(dsn); err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: this server only accepts") {
			t.Errorf("%q: %v", dsn, err)
		}
	}
}

func TestServerIgnoresEnvironmentPassword(t *testing.T) {
	t.Setenv("PGPASSWORD", "operator-secret")
	addr, logins := fakeServer(t)
	spec := engine.ConnSpec{ID: "x", DSN: "postgres://victim@tenant.example.com/app?sslmode=disable", Mode: engine.ModeRO, PoolSize: 1, Server: true, Dial: dialTo(addr)}
	if l := firstLogin(t, spec, logins); l.user != "victim" || l.password != "" {
		t.Fatalf("login %+v", l)
	}
	cfg, err := poolConfig(spec)
	if err != nil || cfg.ConnConfig.Password != "" {
		t.Fatalf("password %q err %v", cfg.ConnConfig.Password, err)
	}
}

func TestServerIgnoresPgpass(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".pgpass"), []byte("*:*:*:*:pgpass-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, logins := fakeServer(t)
	host, port, _ := net.SplitHostPort(addr)
	// Local mode reads ~/.pgpass, as libpq does: the fixture is effective.
	local := engine.ConnSpec{ID: "x", DSN: "host=" + host + " port=" + port + " user=victim dbname=app sslmode=disable", Mode: engine.ModeRO, PoolSize: 1}
	if l := firstLogin(t, local, logins); l.password != "pgpass-secret" {
		t.Fatalf("local login %+v", l)
	}
	server := local
	server.Server, server.Dial = true, dialTo(addr)
	if l := firstLogin(t, server, logins); l.password != "" {
		t.Fatalf("server login %+v", l)
	}
	// An empty password plus a trailing backslash: the backslash must not
	// swallow the forced passfile='' (pgconn reads ~/.pgpass when the
	// password is empty).
	server.DSN = "host=" + host + " port=" + port + ` user=victim password='' sslmode=disable application_name=x\`
	if l := firstLogin(t, server, logins); l.password != "" {
		t.Fatalf("server login after a trailing backslash %+v", l)
	}
}

func TestServerUsesTenantPassword(t *testing.T) {
	t.Setenv("PGPASSWORD", "operator-secret")
	addr, logins := fakeServer(t)
	for _, dsn := range []string{
		"postgres://victim:tenant-pw@tenant.example.com/app?sslmode=disable",
		"host=tenant.example.com user=victim password=tenant-pw dbname=app sslmode=disable",
	} {
		spec := engine.ConnSpec{ID: "x", DSN: dsn, Mode: engine.ModeRO, PoolSize: 1, Server: true, Dial: dialTo(addr)}
		if l := firstLogin(t, spec, logins); l.user != "victim" || l.password != "tenant-pw" {
			t.Fatalf("%q: login %+v", dsn, l)
		}
	}
}

func TestLocalModeHonoursEnvironmentPassword(t *testing.T) {
	t.Setenv("PGPASSWORD", "operator-secret")
	addr, logins := fakeServer(t)
	host, port, _ := net.SplitHostPort(addr)
	spec := engine.ConnSpec{ID: "x", DSN: "postgres://victim@" + net.JoinHostPort(host, port) + "/app?sslmode=disable", Mode: engine.ModeRO, PoolSize: 1}
	if l := firstLogin(t, spec, logins); l.password != "operator-secret" {
		t.Fatalf("login %+v", l)
	}
}

// writeClientCert puts a client certificate and key where pgconn looks by
// default: ~/.postgresql/postgresql.{crt,key}.
func writeClientCert(t *testing.T, home string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "operator"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".postgresql")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "postgresql.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(filepath.Join(dir, "postgresql.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600)
}

func TestServerIgnoresDefaultClientCertificate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClientCert(t, home)
	for _, dsn := range []string{"postgres://u@db.example.com/app?sslmode=require", "host=db.example.com user=u sslmode=prefer"} {
		local, err := poolConfig(engine.ConnSpec{DSN: dsn, PoolSize: 1})
		if err != nil || local.ConnConfig.TLSConfig == nil || len(local.ConnConfig.TLSConfig.Certificates) != 1 {
			t.Fatalf("%q: the fixture certificate is not picked up in local mode: %v", dsn, err)
		}
		server, err := poolConfig(engine.ConnSpec{DSN: dsn, PoolSize: 1, Server: true, Dial: dialTo("127.0.0.1:1")})
		if err != nil {
			t.Fatal(err)
		}
		tlsConfigs := []*tls.Config{server.ConnConfig.TLSConfig}
		for _, fb := range server.ConnConfig.Fallbacks {
			tlsConfigs = append(tlsConfigs, fb.TLSConfig)
		}
		for i, c := range tlsConfigs {
			if c != nil && (len(c.Certificates) > 0 || c.GetClientCertificate != nil) {
				t.Errorf("%q: TLS config %d still carries a client certificate", dsn, i)
			}
		}
	}
}

// Tenant connection strings whose last value could swallow the settings qh
// appends: each is refused, or logs in without the operator's ~/.pgpass.
func TestServerPgpassSwallowVariants(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".pgpass"), []byte("*:*:*:*:pgpass-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, logins := fakeServer(t)
	host, port, _ := net.SplitHostPort(addr)
	tails := []string{
		`application_name=`,
		`application_name= `,
		"application_name=\t",
		`application_name=x\`,
		`application_name=x\\`,
		`application_name=x\\\`,
		`application_name=x\'`,
		"application_name=x\\\t",
		`application_name=\`,
	}
	var dsns []string
	for _, tail := range tails {
		dsns = append(dsns,
			"host="+host+" port="+port+" user=victim password='' sslmode=disable "+tail,
			"host="+host+" port="+port+" user=victim sslmode=disable "+tail)
	}
	dsns = append(dsns, "postgres://victim@"+net.JoinHostPort(host, port)+"/app?sslmode=disable&password=&application_name=")
	for _, dsn := range dsns {
		spec := engine.ConnSpec{ID: "x", DSN: dsn, Mode: engine.ModeRO, PoolSize: 1, Server: true, Dial: dialTo(addr)}
		_, err := Engine{}.Open(context.Background(), spec)
		if err == nil {
			t.Errorf("%q: open succeeded against the fake server", dsn)
			continue
		}
		if qerr.From(err).Category == qerr.Invalid {
			select {
			case l := <-logins:
				t.Errorf("%q: refused (%v) but logged in %+v", dsn, err, l)
			default:
			}
			continue
		}
		select {
		case l := <-logins:
			if l.password != "" {
				t.Errorf("%q: sent password %q", dsn, l.password)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%q: no login and not refused: %v", dsn, err)
		}
	}
}

// A custom dialer without Server is a local SSH tunnel: the server-mode
// allow-list must not apply.
func TestLocalDialIsNotServerMode(t *testing.T) {
	dsn := "postgres://u:p@db.example.com/app?passfile=/nonexistent&sslmode=disable"
	if _, err := poolConfig(engine.ConnSpec{DSN: dsn, PoolSize: 1, Dial: dialTo("127.0.0.1:1")}); err != nil {
		t.Fatalf("local dial refused: %v", err)
	}
	if _, err := poolConfig(engine.ConnSpec{DSN: dsn, PoolSize: 1, Dial: dialTo("127.0.0.1:1"), Server: true}); err == nil {
		t.Fatal("server mode accepted passfile")
	}
}

func TestPasswordOverride(t *testing.T) {
	cfg, err := poolConfig(engine.ConnSpec{DSN: "postgres://u:wrong@db.example.com/app", PoolSize: 1, Password: "right"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Password != "right" {
		t.Fatalf("password = %q", cfg.ConnConfig.Password)
	}
	cfg, err = poolConfig(engine.ConnSpec{DSN: "postgres://u@db.example.com/app", PoolSize: 1, Password: "right", Server: true, Dial: dialTo("127.0.0.1:1")})
	if err != nil || cfg.ConnConfig.Password != "right" {
		t.Fatalf("server mode: %v %q", err, cfg.ConnConfig.Password)
	}
}
