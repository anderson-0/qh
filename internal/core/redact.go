package core

import (
	"context"
	"errors"
	"strings"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/redact"
)

// redactedConn removes the secrets of the connection generation that opened
// it from every error its methods return. It owns conn.
type redactedConn struct {
	conn engine.Conn
	set  *redact.Set
}

func (c *redactedConn) Query(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	res, err := c.conn.Query(ctx, q, o)
	return res, c.set.Error(err)
}

func (c *redactedConn) Explain(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	res, err := c.conn.Explain(ctx, q, o)
	return res, c.set.Error(err)
}

func (c *redactedConn) Introspect(ctx context.Context) (*engine.Catalog, error) {
	cat, err := c.conn.Introspect(ctx)
	return cat, c.set.Error(err)
}

func (c *redactedConn) Ping(ctx context.Context) (string, error) {
	v, err := c.conn.Ping(ctx)
	return v, c.set.Error(err)
}

func (c *redactedConn) Close() error { return c.set.Error(c.conn.Close()) }

// inlineSecrets are the secrets written in a connection's own settings: its
// connection string (unless its engine's strings are displayable paths), the
// structured or override password, and inline SSH and TLS secrets. A nil e
// (an engine name not yet validated) treats the string as a secret.
func inlineSecrets(e engine.Engine, dsn, password string, ssh *engine.SSH, tls *engine.TLS) []string {
	var out []string
	if _, displayable := e.(engine.DisplayableDSN); !displayable {
		out = append(out, dsn)
	}
	out = append(out, password)
	if ssh != nil {
		out = append(out, ssh.PrivateKey, ssh.Passphrase, ssh.Password)
	}
	if tls != nil {
		out = append(out, tls.Key)
	}
	return out
}

// credentials returns the secrets e finds in dsn. When e cannot tell where
// they are, the connection fails with fixed text.
func credentials(e engine.Engine, dsn string) ([]string, error) {
	x, ok := e.(engine.CredentialExtractor)
	if !ok || dsn == "" {
		return nil, nil
	}
	creds, err := x.Credentials(dsn)
	if err != nil {
		return nil, invalidConnString(e)
	}
	return creds, nil
}

func invalidConnString(e engine.Engine) error {
	return qerr.New(qerr.Invalid, "invalid "+e.Name()+" connection string")
}

// inspectFailed makes a failed Inspect safe to show. An engine's fixed text
// (category invalid, no cause) is kept, redacted; anything else becomes
// fixed text. (D3)
func inspectFailed(eng engine.Engine, err error, set *redact.Set) error {
	var e *qerr.Error
	if errors.As(err, &e) && e.Category == qerr.Invalid && e.Err == nil {
		return set.Error(e)
	}
	return invalidConnString(eng)
}

// inputSecrets is the set of secrets in carries as given, before normalize
// moves a structured password into the connection string. An unknown engine
// name treats the string as a secret.
func inputSecrets(in ConnInput) *redact.Set {
	eng, _ := engine.Get(strings.ToLower(strings.TrimSpace(in.Engine))) // as normalize looks it up
	return redact.New(inlineSecrets(eng, in.DSN, in.Password, in.SSH, in.TLS)...)
}

// patchSecrets is the set of secrets the patch p carries: its password,
// connection string, SSH secrets and TLS key. The stored settings' secrets
// are left out: no error quotes them, and redacting them from errors that
// echo the caller's input would confirm guesses of them. name is the stored
// connection's engine.
func patchSecrets(name string, p ConnPatch) *redact.Set {
	e, _ := engine.Get(name) // nil when unknown: the string counts as a secret
	var dsn, password string
	if p.DSN != nil {
		dsn = *p.DSN
	}
	if p.Password != nil {
		password = *p.Password
	}
	return redact.New(inlineSecrets(e, dsn, password, p.SSH, p.TLS)...)
}
