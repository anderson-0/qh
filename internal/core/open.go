package core

import (
	"context"
	"errors"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/netguard"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/redact"
	"github.com/anderson-0/qh/internal/sshkey"
	"github.com/anderson-0/qh/internal/tunnel"
)

// OpenConn opens spec with its registered engine. It is the production
// pool.Opener. It resolves the spec's secret references, builds its SSH
// tunnel, and returns a connection that closes the tunnel with it.
// Every secret it knows of, inline or resolved, is removed from its errors
// and from the errors of the connection it returns.
func OpenConn(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	e, err := engine.Get(spec.Engine)
	if err != nil {
		return nil, err
	}
	// Every secret is collected before the step that could fail with it.
	secrets := inlineSecrets(e, spec.DSN, spec.Password, spec.SSH, spec.TLS)
	fail := func(err error) (engine.Conn, error) { return nil, redact.New(secrets...).Error(err) }
	creds, err := credentials(e, spec.DSN) // inline string; a dsn_ref one is empty here
	if err != nil {
		return fail(err)
	}
	secrets = append(secrets, creds...)
	resolve := func(ref string) (string, error) {
		if spec.Resolve == nil {
			return "", qerr.New(qerr.ConnectionFailed, "secret reference "+ref+": no resolver")
		}
		v, err := spec.Resolve(ctx, ref)
		secrets = append(secrets, v) // also on error: a resolver may return both
		return v, err
	}
	if spec.DSNRef != "" {
		if spec.DSN, err = resolve(spec.DSNRef); err != nil {
			return fail(err)
		}
		// Extraction is a pure parse, so its secrets are known before the
		// server-mode check can fail.
		var cerr error
		creds, cerr = credentials(e, spec.DSN)
		secrets = append(secrets, creds...)
		// The server-mode check comes before Inspect: parsing a connection
		// string may read the files it names.
		if c, ok := e.(engine.ServerDSNChecker); ok && spec.Server {
			if err := c.CheckServerDSN(spec.DSN); err != nil {
				return fail(err)
			}
		}
		invalid := qerr.New(qerr.ConnectionFailed, "secret reference "+spec.DSNRef+": not a valid "+spec.Engine+" connection string")
		if cerr != nil {
			return fail(invalid)
		}
		if _, err := e.Inspect(spec.DSN); err != nil {
			return fail(invalid)
		}
	}
	if spec.PasswordRef != "" {
		if spec.Password, err = resolve(spec.PasswordRef); err != nil {
			return fail(err)
		}
	}
	var tun *tunnel.Tunnel
	if s := spec.SSH; s != nil {
		if spec.Server && spec.CheckTarget == nil {
			// Fail closed: the bastion would reach any address unchecked.
			return fail(qerr.New(qerr.ConnectionFailed, "ssh: this server has no network policy for tunnel targets"))
		}
		cfg := tunnel.Config{Addr: s.Addr(), User: s.User, HostKey: s.HostKey,
			PrivateKey: s.PrivateKey, Passphrase: s.Passphrase, Password: s.Password,
			DialBastion: spec.Dial, CheckTarget: spec.CheckTarget}
		if spec.Server {
			// The key is a tenant's: bound what parsing and signing cost.
			cfg.MaxKDFRounds, cfg.MaxRSABits, cfg.RefuseDSA = sshkey.ServerMaxKDFRounds, sshkey.ServerMaxRSABits, true
		}
		for _, f := range []struct {
			ref string
			dst *string
		}{{s.PrivateKeyRef, &cfg.PrivateKey}, {s.PassphraseRef, &cfg.Passphrase}, {s.PasswordRef, &cfg.Password}} {
			if f.ref == "" {
				continue
			}
			if *f.dst, err = resolve(f.ref); err != nil {
				return fail(err)
			}
		}
		if s.PrivateKeyRef != "" && cfg.PrivateKey == "" {
			// Never fall back to password authentication with no password.
			return fail(qerr.New(qerr.ConnectionFailed, "ssh: private_key_ref resolved to an empty value"))
		}
		if s.PasswordRef != "" && cfg.Password == "" {
			return fail(qerr.New(qerr.ConnectionFailed, "ssh: password_ref resolved to an empty value"))
		}
		if tun, err = tunnel.New(cfg); err != nil {
			return fail(tunnelErr(err, cfg.Addr))
		}
		if err := tun.Connect(ctx); err != nil {
			tun.Close()
			return fail(tunnelErr(err, cfg.Addr))
		}
		spec.Dial = tun.Dial
	}
	// Every secret is known now. This generation keeps its own set, so a
	// later rotation cannot change what an older pool redacts. The engine
	// sees raw errors; they are redacted only once it returns them.
	set := redact.New(secrets...)
	spec.Redact = set.Text
	c, err := e.Open(ctx, spec)
	if err != nil {
		if tun != nil {
			tun.Close()
		}
		return nil, set.Error(err)
	}
	if tun != nil {
		c = &tunneledConn{Conn: c, tun: tun}
	}
	return &redactedConn{conn: c, set: set}, nil
}

// tunnelErr classifies an error from building or connecting the tunnel to
// the bastion at addr. The tunnel's own failures are already
// connection_failed; a network-policy refusal and an ended context are
// given that category here, naming the stage.
func tunnelErr(err error, addr string) error {
	var qe *qerr.Error
	if errors.As(err, &qe) {
		return err
	}
	var de *netguard.DeniedError
	switch {
	case errors.As(err, &de):
		return qerr.Wrap(qerr.ConnectionFailed, "ssh: cannot reach the bastion "+addr+": "+de.Error(), err)
	case errors.Is(err, context.DeadlineExceeded):
		return qerr.Wrap(qerr.ConnectionFailed, "ssh: timed out reaching the bastion "+addr, err)
	case errors.Is(err, context.Canceled):
		return qerr.Wrap(qerr.ConnectionFailed, "ssh: cancelled while reaching the bastion "+addr, err)
	}
	return qerr.Wrap(qerr.ConnectionFailed, "ssh: "+err.Error(), err)
}

// tunneledConn closes its tunnel after the engine connection.
type tunneledConn struct {
	engine.Conn
	tun *tunnel.Tunnel
}

func (c *tunneledConn) Close() error {
	err := c.Conn.Close()
	c.tun.Close()
	return err
}
