package core

import (
	"context"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/tunnel"
)

// OpenConn opens spec with its registered engine. It is the production
// pool.Opener. It resolves the spec's secret references, builds its SSH
// tunnel, and returns a connection that closes the tunnel with it.
// Resolved values never reach errors.
func OpenConn(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	e, err := engine.Get(spec.Engine)
	if err != nil {
		return nil, err
	}
	var secrets []string
	fail := func(err error) (engine.Conn, error) {
		if len(secrets) == 0 {
			return nil, err
		}
		return nil, scrub(err, secrets...)
	}
	resolve := func(ref string) (string, error) {
		if spec.Resolve == nil {
			return "", qerr.New(qerr.ConnectionFailed, "secret reference "+ref+": no resolver")
		}
		v, err := spec.Resolve(ctx, ref)
		if err == nil {
			secrets = append(secrets, v)
		}
		return v, err
	}
	if spec.DSNRef != "" {
		if spec.DSN, err = resolve(spec.DSNRef); err != nil {
			return fail(err)
		}
		// The server-mode check comes first: parsing a connection string
		// (Inspect) may read the files it names.
		if c, ok := e.(engine.ServerDSNChecker); ok && spec.Server {
			if err := c.CheckServerDSN(spec.DSN); err != nil {
				return fail(err)
			}
		}
		info, err := e.Inspect(spec.DSN)
		if err != nil {
			return fail(qerr.New(qerr.ConnectionFailed, "secret reference "+spec.DSNRef+": not a valid "+spec.Engine+" connection string"))
		}
		secrets = append(secrets, info.Password)
	}
	if spec.PasswordRef != "" {
		if spec.Password, err = resolve(spec.PasswordRef); err != nil {
			return fail(err)
		}
	}
	var tun *tunnel.Tunnel
	if s := spec.SSH; s != nil {
		cfg := tunnel.Config{Addr: s.Addr(), User: s.User, HostKey: s.HostKey,
			PrivateKey: s.PrivateKey, Passphrase: s.Passphrase, Password: s.Password,
			DialBastion: spec.Dial, CheckTarget: spec.CheckTarget}
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
		secrets = append(secrets, cfg.Password, cfg.Passphrase)
		if tun, err = tunnel.New(cfg); err != nil {
			return fail(err)
		}
		if err := tun.Connect(ctx); err != nil {
			tun.Close()
			return fail(err)
		}
		spec.Dial = tun.Dial
	}
	c, err := e.Open(ctx, spec)
	if err != nil {
		if tun != nil {
			tun.Close()
		}
		return fail(err)
	}
	if tun == nil {
		return c, nil
	}
	return &tunneledConn{Conn: c, tun: tun}, nil
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
