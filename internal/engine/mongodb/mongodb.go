package mongodb

import (
	"context"
	"crypto/tls"
	"errors"
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

// uri is a parsed mongodb:// or mongodb+srv:// connection string, split
// exactly as the driver splits it (x/mongo/driver/connstring), so that
// Inspect, redaction, TLS stripping and the server-mode check all see what
// the driver will apply.
type uri struct {
	srv            bool
	user, password string
	hasPassword    bool
	pwStart, pwEnd int      // the raw password's byte offsets in the DSN; pwStart < 0 when there is none
	hosts          []string // host or host:port, unescaped as the driver does
	db             string
	hasPath        bool   // a "/" follows the hosts
	base           string // the DSN up to the "?" before the options
	options        []option
}

// option is one query option, in order.
type option struct {
	raw   string // as written, "key=value"
	key   string // unescaped, in its original spelling
	lkey  string // key lowered with strings.ToLower, as the driver does
	value string // unescaped
}

func badURI() error {
	return qerr.New(qerr.Invalid, "invalid mongodb connection string").
		WithHint("The form is scheme://user:password@host:27017/database, where scheme is mongodb or mongodb+srv; escape @ : / ? in the user name and password as %40 %3A %2F %3F")
}

func parseURI(dsn string) (*uri, error) {
	u := &uri{pwStart: -1, pwEnd: -1}
	var off int
	switch {
	case strings.HasPrefix(dsn, "mongodb+srv://"):
		u.srv, off = true, len("mongodb+srv://")
	case strings.HasPrefix(dsn, "mongodb://"):
		off = len("mongodb://")
	default:
		return nil, badURI()
	}
	rest := dsn[off:]
	// The driver takes the userinfo up to the first "@" anywhere in the
	// string, and refuses "/" in it and ":" in the password.
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		name, pw, has := strings.Cut(rest[:at], ":")
		if strings.Contains(name, "/") || has && strings.ContainsAny(pw, ":/") {
			return nil, badURI()
		}
		var err error
		if u.user, err = url.PathUnescape(name); err != nil {
			return nil, badURI()
		}
		if has {
			if u.password, err = url.PathUnescape(pw); err != nil {
				return nil, badURI()
			}
			u.hasPassword = true
			u.pwStart, u.pwEnd = off+len(name)+1, off+at
		}
		off += at + 1
		rest = rest[at+1:]
	}
	hostPart, tail := rest, ""
	if i := strings.IndexAny(rest, "/?@"); i >= 0 {
		if rest[i] != '/' { // "@" in the hosts, or "?" with no "/" before it
			return nil, badURI()
		}
		hostPart, tail = rest[:i], rest[i:]
	}
	if hostPart == "" {
		return nil, badURI()
	}
	for _, h := range strings.Split(hostPart, ",") {
		if h == "" {
			return nil, badURI()
		}
		h, err := url.QueryUnescape(h)
		if err != nil {
			return nil, badURI()
		}
		_, port, err := net.SplitHostPort(h)
		if err != nil {
			var ae *net.AddrError
			if !errors.As(err, &ae) || ae.Err != "missing port in address" {
				return nil, badURI()
			}
		}
		if port != "" {
			if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 || u.srv {
				return nil, badURI()
			}
		}
		u.hosts = append(u.hosts, h)
	}
	if u.srv && len(u.hosts) != 1 {
		return nil, badURI()
	}
	u.base = dsn
	if tail != "" {
		u.hasPath = true
		path, query, hasQuery := strings.Cut(tail[1:], "?")
		db, err := url.QueryUnescape(path)
		if err != nil {
			return nil, badURI()
		}
		u.db = db
		if hasQuery {
			u.base = dsn[:len(dsn)-len(query)-1]
			if u.options, err = parseOptions(query); err != nil {
				return nil, err
			}
		}
	}
	return u, nil
}

// parseOptions parses query as the driver does: pairs split on "&" and ";",
// each "key=value" with both parts QueryUnescaped and the key lowered with
// strings.ToLower.
func parseOptions(query string) ([]option, error) {
	var opts []option
	for _, pair := range strings.FieldsFunc(query, func(r rune) bool { return r == '&' || r == ';' }) {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return nil, badURI()
		}
		key, err := url.QueryUnescape(k)
		if err != nil {
			return nil, badURI()
		}
		val, err := url.QueryUnescape(v)
		if err != nil {
			return nil, badURI()
		}
		opts = append(opts, option{raw: pair, key: key, lkey: strings.ToLower(key), value: val})
	}
	return opts, nil
}

// param returns the last value of the option called name, matched as the
// driver matches it.
func (u *uri) param(name string) (string, bool) {
	name = strings.ToLower(name)
	val, found := "", false
	for _, o := range u.options {
		if o.lkey == name {
			val, found = o.value, true
		}
	}
	return val, found
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
	info.Redacted = u.redact(dsn)
	return info, nil
}

// secretParams are options whose values are secrets, lowered.
// authMechanismProperties can carry AWS_SESSION_TOKEN.
var secretParams = []string{"tlscertificatekeyfilepassword", "sslclientcertificatekeypassword", "authmechanismproperties"}

// redact returns dsn (which parsed as u) with the password and the values
// of secretParams replaced by ***, leaving every other byte as it was.
func (u *uri) redact(dsn string) string {
	base := u.base
	if u.pwStart >= 0 {
		base = dsn[:u.pwStart] + "***" + dsn[u.pwEnd:len(u.base)]
	}
	if len(u.base) == len(dsn) {
		return base
	}
	var b strings.Builder
	b.WriteString(base)
	b.WriteByte('?')
	query := dsn[len(u.base)+1:]
	start := 0
	for i := 0; i <= len(query); i++ {
		if i < len(query) && query[i] != '&' && query[i] != ';' {
			continue
		}
		kv := query[start:i]
		if k, _, ok := strings.Cut(kv, "="); ok {
			if uk, err := url.QueryUnescape(k); err == nil && contains(secretParams, strings.ToLower(uk)) {
				kv = k + "=***"
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

// serverOptions are the options server mode accepts, lowered. None reads a
// file or changes how hosts are reached: every connection still goes
// through spec.Dial. The driver ignores wtimeoutms and waitqueuetimeoutms.
var serverOptions = []string{
	"authsource", "authmechanism", "replicaset", "tls", "ssl", "retrywrites", "retryreads", "w", "wtimeoutms",
	"journal", "readpreference", "readpreferencetags", "maxstalenessseconds", "readconcernlevel", "appname",
	"compressors", "zlibcompressionlevel", "zstdcompressionlevel", "connecttimeoutms", "sockettimeoutms",
	"serverselectiontimeoutms", "heartbeatfrequencyms", "localthresholdms", "maxpoolsize", "minpoolsize",
	"maxconnecting", "maxidletimems", "waitqueuetimeoutms", "directconnection", "loadbalanced", "srvmaxhosts",
	"srvservicename", "timeoutms", "servermonitoringmode",
}

// serverMechanisms are the authMechanism values server mode accepts, matched
// exactly. The others use the qh host's ambient credentials (AWS
// environment and instance metadata, Kerberos tickets, cloud identity
// tokens). MONGODB-X509 takes its certificate from the connection's tls
// settings.
var serverMechanisms = []string{"SCRAM-SHA-1", "SCRAM-SHA-256", "PLAIN", "MONGODB-X509"}

// maxEchoedKey is the longest refused option key, in bytes, that
// CheckServerDSN names in its error.
const maxEchoedKey = 64

const serverOptionsHint = "TLS certificates, keys and CAs go in the connection's tls settings, and the password in the connection string's user info or password_ref"

// CheckServerDSN implements engine.ServerDSNChecker. Server mode accepts
// only the options in serverOptions.
func (Engine) CheckServerDSN(dsn string) error {
	u, err := parseURI(dsn)
	if err != nil {
		return err
	}
	for _, o := range u.options {
		if !isASCII(o.key) || !contains(serverOptions, o.lkey) {
			// A long key is not echoed: cutting it could leave part of a
			// secret, or of a character, that redaction no longer matches.
			if len(o.key) > maxEchoedKey {
				return qerr.New(qerr.Invalid, "this server does not accept an option longer than "+strconv.Itoa(maxEchoedKey)+" bytes in a mongodb connection string").
					WithHint(serverOptionsHint)
			}
			return qerr.New(qerr.Invalid, "this server does not accept the option "+o.key+" in a mongodb connection string").
				WithHint(serverOptionsHint)
		}
		if o.lkey == "authmechanism" && !contains(serverMechanisms, o.value) {
			return qerr.New(qerr.Invalid, "this server does not accept that authMechanism; it accepts "+strings.Join(serverMechanisms, ", ")).
				WithHint(serverOptionsHint)
		}
	}
	return nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// dialer adapts spec.Dial to the driver's ContextDialer.
type dialer func(ctx context.Context, network, addr string) (net.Conn, error)

func (d dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d(ctx, network, addr)
}

// tlsParams are the connection-string settings spec.TLS replaces, lowered.
var tlsParams = []string{"tls", "ssl", "tlscafile", "tlscertificatekeyfile", "tlscertificatekeyfilepassword",
	"tlscertificatefile", "tlsprivatekeyfile",
	"sslcertificateauthorityfile", "sslclientcertificatekeyfile", "sslclientcertificatekeypassword", "sslinsecure",
	"tlsinsecure", "tlsallowinvalidcertificates", "tlsallowinvalidhostnames",
	"tlsdisableocspendpointcheck", "tlsdisablecertificaterevocationcheck"}

// withoutParams returns the DSN u was parsed from without the options named
// in names (lowered), plus extra ("k=v") when not empty.
func (u *uri) withoutParams(names []string, extra string) string {
	var keep []string
	for _, o := range u.options {
		if !contains(names, o.lkey) {
			keep = append(keep, o.raw)
		}
	}
	if extra != "" {
		keep = append(keep, extra)
	}
	if len(keep) == 0 {
		return u.base
	}
	base := u.base
	if !u.hasPath {
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
		dsn = u.withoutParams(tlsParams, extra)
	}
	opts := options.Client().ApplyURI(dsn)
	if err := opts.Validate(); err != nil {
		// The driver's text can quote the connection string.
		clean, cerr := cleaner(spec)
		if cerr != nil {
			return nil, nil, cerr
		}
		return nil, nil, qerr.New(qerr.Invalid, "invalid mongodb connection string: "+clean(err.Error()))
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
	clean, err := cleaner(spec)
	if err != nil {
		return nil, err
	}
	spec.Redact = clean // so clientOptions cleans with the same function
	opts, u, err := clientOptions(spec)
	if err != nil {
		return nil, err
	}
	c := &conn{defaultDB: u.db, pinned: u.db != "", namespaces: spec.Namespaces,
		readOnly: spec.Mode != engine.ModeRW, clean: clean}
	c.setConcerns(opts)
	if c.defaultDB == "" {
		c.defaultDB = "test"
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, c.err(err)
	}
	c.client = client
	if _, err := c.Ping(ctx); err != nil {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Disconnect(dctx)
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
	concerns
	// clean removes the connection's secrets from text, in case the driver
	// quotes them (see cleaner). Use cleanText, which also works on a conn
	// that Open did not build.
	clean func(string) string
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

// err maps a driver error and removes the connection's secrets from its
// text. mapErr cleans driver text before cutting it; the message and hint are
// then cleaned whole.
func (c *conn) err(err error) error {
	e := *qerr.From(mapErr(err, c.cleanText))
	e.Message, e.Hint = c.cleanText(e.Message), c.cleanText(e.Hint)
	return &e
}

// cleanText removes the connection's secrets from s. A conn that Open did
// not build (in tests) has no clean function and returns s unchanged.
func (c *conn) cleanText(s string) string {
	if c.clean == nil {
		return s
	}
	return c.clean(s)
}
