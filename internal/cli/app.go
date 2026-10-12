package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
)

type app struct {
	svc   *core.Service
	pools *pool.Manager
	st    store.Store
	keys  *secret.Keyring
}

// storeFlag is bound to the --store persistent flag by newRoot.
var storeFlag string

// storeDSN returns the store every command uses: --store, else
// $QH_STORE_DSN, else "" (the SQLite file in the data directory).
func storeDSN() string {
	if storeFlag != "" {
		return storeFlag
	}
	return os.Getenv("QH_STORE_DSN")
}

// storeOpenError classifies a failure of store.Open. The store package
// already keeps the password out of its errors, so err is safe to show.
func storeOpenError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrInvalidDSN):
		return qerr.New(qerr.Invalid, "--store must be a postgres:// URL").WithHint("Leave it unset to use the SQLite file in the data directory")
	}
	return qerr.Wrap(qerr.ConnectionFailed, "cannot open the store: "+err.Error(), err).
		WithHint("Check --store or QH_STORE_DSN, and that the database is reachable")
}

func homeDir(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("QH_HOME"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "qh"), nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "qh"), nil
}

func openApp(home string) (*app, error) { return openAppWith(home, core.Options{}) }

func openAppWith(home string, opts core.Options, oldKeys ...[]byte) (*app, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	var key []byte
	var err error
	if v := os.Getenv("QH_MASTER_KEY"); v != "" {
		key, err = secret.ParseKey(v)
	} else {
		key, err = secret.LoadOrCreateKeyFile(filepath.Join(home, "master.key"))
	}
	if err != nil {
		return nil, err
	}
	keys, err := secret.NewKeyring(key, oldKeys...)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(context.Background(), storeDSN(), home)
	if err != nil {
		return nil, storeOpenError(err)
	}
	pools := pool.New(core.OpenConn, pool.Options{})
	return &app{svc: core.New(st, keys, pools, catalog.New(10*time.Minute), opts), pools: pools, st: st, keys: keys}, nil
}

func (a *app) Close() {
	a.svc.Close()
	a.pools.Close()
	a.st.Close()
}
