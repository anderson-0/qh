package mongodb

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func init() { engine.Register(Engine{}) }

// Engine is the MongoDB engine.
type Engine struct{}

func (Engine) Name() string { return "mongodb" }

// BuildDSN implements engine.DSNBuilder.
func (Engine) BuildDSN(f engine.DSNFields) (string, error) { return engine.BuildURL("mongodb", f) }

func (Engine) Classify(q string) (engine.Access, error) {
	c, err := Parse(q)
	if err != nil {
		return 0, err
	}
	return classify(c)
}

func (Engine) ReferencedObjects(q string) []string {
	c, err := Parse(q)
	if err != nil || c.Collection == "" {
		return nil
	}
	return []string{c.Collection}
}

// uri is a parsed mongodb:// or mongodb+srv:// connection string. net/url
// cannot parse a host list, so the parts are split by hand.
type uri struct {
	srv            bool
	user, password string
	hasPassword    bool
	hosts          []string // host or host:port
	db             string
	query          url.Values
}

func badURI() error {
	return qerr.New(qerr.Invalid, "invalid mongodb connection string").
		WithHint("The form is scheme://user:password@host:27017/database, where scheme is mongodb or mongodb+srv")
}

func parseURI(dsn string) (*uri, error) {
	u := &uri{}
	rest, ok := strings.CutPrefix(dsn, "mongodb://")
	if !ok {
		if rest, ok = strings.CutPrefix(dsn, "mongodb+srv://"); !ok {
			return nil, badURI()
		}
		u.srv = true
	}
	hostEnd := strings.IndexAny(rest, "/?")
	authority, tail := rest, ""
	if hostEnd >= 0 {
		authority, tail = rest[:hostEnd], rest[hostEnd:]
	}
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		userinfo := authority[:at]
		authority = authority[at+1:]
		name, pw, has := strings.Cut(userinfo, ":")
		var err error
		if u.user, err = url.PathUnescape(name); err != nil {
			return nil, badURI()
		}
		if has {
			if u.password, err = url.PathUnescape(pw); err != nil {
				return nil, badURI()
			}
			u.hasPassword = true
		}
	}
	if authority == "" {
		return nil, badURI()
	}
	for _, h := range strings.Split(authority, ",") {
		if h == "" {
			return nil, badURI()
		}
		if _, port, err := net.SplitHostPort(h); err == nil {
			if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 || u.srv {
				return nil, badURI()
			}
		} else if strings.Contains(strings.TrimSuffix(strings.TrimPrefix(h, "["), "]"), ":") && !strings.HasPrefix(h, "[") {
			return nil, badURI()
		}
		u.hosts = append(u.hosts, h)
	}
	if u.srv && len(u.hosts) != 1 {
		return nil, badURI()
	}
	path, query, _ := strings.Cut(tail, "?")
	// The driver unescapes the database and options with QueryUnescape.
	db, err := url.QueryUnescape(strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, badURI()
	}
	u.db = db
	if u.query, err = parseOptions(query); err != nil {
		return nil, badURI()
	}
	return u, nil
}

// splitOptions splits a query string as the driver does: on "&" and ";".
func splitOptions(query string) []string {
	return strings.FieldsFunc(query, func(r rune) bool { return r == '&' || r == ';' })
}

// parseOptions parses query exactly as the driver does, so that the
// server-mode checks see the options the driver will apply.
func parseOptions(query string) (url.Values, error) {
	v := url.Values{}
	for _, pair := range splitOptions(query) {
		k, val, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return nil, badURI()
		}
		k, err := url.QueryUnescape(k)
		if err != nil {
			return nil, badURI()
		}
		if val, err = url.QueryUnescape(val); err != nil {
			return nil, badURI()
		}
		v.Add(k, val)
	}
	return v, nil
}

// param returns the value of query parameter name, matched case-insensitively
// as the driver does.
func (u *uri) param(name string) (string, bool) {
	for k, v := range u.query {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[len(v)-1], true
		}
	}
	return "", false
}

func (Engine) Inspect(dsn string) (engine.DSNInfo, error) {
	u, err := parseURI(dsn)
	if err != nil {
		return engine.DSNInfo{}, err
	}
	info := engine.DSNInfo{Database: u.db, User: u.user, Password: u.password}
	var names []string
	for i, h := range u.hosts {
		host, port, err := net.SplitHostPort(h)
		if err != nil {
			host, port = strings.Trim(h, "[]"), ""
		}
		names = append(names, host)
		if i == 0 && !u.srv {
			info.Port = 27017
			if port != "" {
				info.Port, _ = strconv.Atoi(port)
			}
		}
	}
	info.Host = strings.Join(names, ",")
	info.Redacted = redactURI(dsn)
	return info, nil
}

// secretParams are query parameters whose values are secrets.
var secretParams = []string{"tlsCertificateKeyFilePassword", "sslClientCertificateKeyPassword"}

// redactURI replaces the password in dsn's authority, and the values of
// secretParams, with ***, leaving every other byte as it was.
func redactURI(dsn string) string {
	dsn = redactAuthority(dsn)
	base, query, ok := strings.Cut(dsn, "?")
	if !ok {
		return dsn
	}
	var b strings.Builder
	b.WriteString(base + "?")
	start := 0
	for i := 0; i <= len(query); i++ {
		if i < len(query) && query[i] != '&' && query[i] != ';' {
			continue
		}
		kv := query[start:i]
		k, _, has := strings.Cut(kv, "=")
		if uk, err := url.QueryUnescape(k); err == nil {
			k = uk
		}
		for _, sp := range secretParams {
			if has && strings.EqualFold(k, sp) {
				kv = kv[:strings.IndexByte(kv, '=')+1] + "***"
			}
		}
		b.WriteString(kv)
		if i < len(query) {
			b.WriteByte(query[i])
		}
		start = i + 1
	}
	return b.String()
}

// redactAuthority replaces the password in dsn's authority with ***.
func redactAuthority(dsn string) string {
	i := strings.Index(dsn, "://")
	if i < 0 {
		return dsn
	}
	start := i + 3
	end := len(dsn)
	if j := strings.IndexAny(dsn[start:], "/?"); j >= 0 {
		end = start + j
	}
	at := strings.LastIndexByte(dsn[start:end], '@')
	if at < 0 {
		return dsn
	}
	colon := strings.IndexByte(dsn[start:start+at], ':')
	if colon < 0 {
		return dsn
	}
	return dsn[:start+colon+1] + "***" + dsn[start+at:]
}

// refusedServerParams make the driver read files on the qh host, or turn off
// certificate checks; server mode refuses them. The ssl* names and
// tlsCertificateFile/tlsPrivateKeyFile are aliases the driver also accepts.
var refusedServerParams = []string{
	"tlsCAFile", "tlsCertificateKeyFile", "tlsCertificateKeyFilePassword", "tlsInsecure",
	"tlsCertificateFile", "tlsPrivateKeyFile",
	"sslCertificateAuthorityFile", "sslClientCertificateKeyFile", "sslClientCertificateKeyPassword", "sslInsecure",
	"tlsAllowInvalidCertificates", "tlsAllowInvalidHostnames",
}

// refusedMechanisms use the qh host's ambient credentials (AWS environment
// and instance metadata, Kerberos tickets, cloud identity tokens).
var refusedMechanisms = []string{"MONGODB-AWS", "GSSAPI", "MONGODB-OIDC"}

// CheckServerDSN implements engine.ServerDSNChecker.
func (Engine) CheckServerDSN(dsn string) error {
	u, err := parseURI(dsn)
	if err != nil {
		return err
	}
	for _, p := range refusedServerParams {
		if _, ok := u.param(p); ok {
			return qerr.New(qerr.Invalid, "this server does not accept "+p+" in a mongodb connection string").
				WithHint("Give TLS certificates through the connection's tls settings instead")
		}
	}
	if m, ok := u.param("authMechanism"); ok {
		for _, r := range refusedMechanisms {
			if strings.EqualFold(m, r) {
				return qerr.New(qerr.Invalid, "this server does not accept authMechanism "+r).
					WithHint("Use a user name and password (SCRAM), or X.509 with the connection's tls settings")
			}
		}
	}
	return nil
}

// dialer adapts spec.Dial to the driver's ContextDialer.
type dialer func(ctx context.Context, network, addr string) (net.Conn, error)

func (d dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d(ctx, network, addr)
}

// tlsParams are the connection-string settings spec.TLS replaces.
var tlsParams = []string{"tls", "ssl", "tlsCAFile", "tlsCertificateKeyFile", "tlsCertificateKeyFilePassword",
	"tlsCertificateFile", "tlsPrivateKeyFile",
	"sslCertificateAuthorityFile", "sslClientCertificateKeyFile", "sslClientCertificateKeyPassword", "sslInsecure",
	"tlsInsecure", "tlsAllowInvalidCertificates", "tlsAllowInvalidHostnames",
	"tlsDisableOCSPEndpointCheck", "tlsDisableCertificateRevocationCheck"}

// withoutParams returns dsn without the named query parameters
// (case-insensitively, after unescaping as the driver does), plus extra
// ("k=v") when not empty.
func withoutParams(dsn string, names []string, extra string) string {
	base, query, _ := strings.Cut(dsn, "?")
	var keep []string
	for _, kv := range splitOptions(query) {
		k, _, _ := strings.Cut(kv, "=")
		if uk, err := url.QueryUnescape(k); err == nil {
			k = uk
		}
		drop := false
		for _, n := range names {
			if strings.EqualFold(k, n) {
				drop = true
			}
		}
		if !drop {
			keep = append(keep, kv)
		}
	}
	if extra != "" {
		keep = append(keep, extra)
	}
	if len(keep) == 0 {
		return base
	}
	if !strings.Contains(base[strings.Index(base, "://")+3:], "/") {
		base += "/"
	}
	return base + "?" + strings.Join(keep, "&")
}

func clientOptions(spec engine.ConnSpec) (*options.ClientOptions, *uri, error) {
	// The checks come before ApplyURI, which reads the files the
	// connection string names.
	if spec.Server {
		if err := (Engine{}).CheckServerDSN(spec.DSN); err != nil {
			return nil, nil, err
		}
	}
	u, err := parseURI(spec.DSN)
	if err != nil {
		return nil, nil, err
	}
	dsn := spec.DSN
	var tlsCfg *tls.Config
	if spec.TLS != nil {
		// The driver sets ServerName per host when it is empty.
		if tlsCfg, err = spec.TLS.Config(""); err != nil {
			return nil, nil, err
		}
		extra := ""
		if tlsCfg == nil {
			extra = "tls=false"
		}
		dsn = withoutParams(dsn, tlsParams, extra)
	}
	opts := options.Client().ApplyURI(dsn)
	if err := opts.Validate(); err != nil {
		return nil, nil, qerr.New(qerr.Invalid, "invalid mongodb connection string: "+scrubText(err.Error(), spec.DSN, u.password, spec.Password))
	}
	if tlsCfg != nil {
		opts.SetTLSConfig(tlsCfg)
	}
	if spec.Password != "" {
		if opts.Auth == nil || opts.Auth.Username == "" {
			return nil, nil, qerr.New(qerr.Invalid, "a password needs a user name in the mongodb connection string")
		}
		opts.Auth.Password, opts.Auth.PasswordSet = spec.Password, true
	}
	if spec.Dial != nil {
		opts.SetDialer(dialer(spec.Dial))
		// The driver makes HTTP requests of its own (OCSP responders named
		// by the server's certificate); they go through spec.Dial too, with
		// no proxy from the environment.
		opts.SetHTTPClient(&http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{DialContext: spec.Dial, TLSHandshakeTimeout: 10 * time.Second},
		})
	}
	opts.SetMaxPoolSize(uint64(max(spec.PoolSize, 1)))
	opts.SetAppName("qh")
	if opts.ServerSelectionTimeout == nil {
		opts.SetServerSelectionTimeout(10 * time.Second)
	}
	if opts.ConnectTimeout == nil {
		opts.SetConnectTimeout(10 * time.Second)
	}
	return opts, u, nil
}

func (Engine) Open(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	opts, u, err := clientOptions(spec)
	if err != nil {
		return nil, err
	}
	c := &conn{defaultDB: u.db, pinned: u.db != "", namespaces: spec.Namespaces,
		readOnly: spec.Mode != engine.ModeRW, password: u.password, extra: spec.Password}
	if c.defaultDB == "" {
		c.defaultDB = "test"
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, c.err(err)
	}
	c.client = client
	if _, err := c.Ping(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return c, nil
}

type conn struct {
	client     *mongo.Client
	defaultDB  string // the DSN's database, else "test" as in mongosh
	pinned     bool   // the DSN names a database: it is the only namespace
	namespaces []string
	readOnly   bool
	// password and extra are scrubbed from every error, in case the driver
	// quotes them.
	password, extra string
}

func (c *conn) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.client.Disconnect(ctx)
}

func (c *conn) Ping(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var info struct {
		Version string `bson:"version"`
	}
	if err := c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info); err != nil {
		return "", c.err(err)
	}
	return "MongoDB " + info.Version, nil
}

// err maps a driver error and removes the passwords from its text.
func (c *conn) err(err error) error {
	e := qerr.From(mapErr(err))
	out := *e
	out.Message = scrubText(out.Message, c.password, c.extra)
	out.Hint = scrubText(out.Hint, c.password, c.extra)
	return &out
}

func scrubText(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return s
}

// Query, Explain and Introspect are implemented in exec.go and introspect.go.
func (c *conn) Query(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	return nil, qerr.New(qerr.Invalid, fmt.Sprintf("not implemented: %q", q))
}

func (c *conn) Explain(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	return nil, qerr.New(qerr.Invalid, "not implemented")
}

func (c *conn) Introspect(ctx context.Context) (*engine.Catalog, error) {
	return nil, qerr.New(qerr.Invalid, "not implemented")
}
