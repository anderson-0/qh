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

// storeOpenError classifies a failure of store.Open; shared says a store DSN
// was given. The store package already keeps the password out of its errors,
// so err is safe to show. A local SQLite failure is returned as it came.
func storeOpenError(err error, shared bool) error {
	switch {
	case err == nil || !shared:
		return err
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

func openApp(ctx context.Context, home string) (*app, error) {
	return openAppWith(ctx, home, core.Options{})
}

// errSharedStoreNeedsKey is returned when a shared store is selected but
// QH_MASTER_KEY is unset. A shared store holds sealed data for every instance,
// so its key must be the one all of them use. A master.key file in the data
// directory is a per-host key (possibly created by an earlier local run), so
// it is never used or created with a shared store.
func errSharedStoreNeedsKey() error {
	return qerr.New(qerr.Invalid, "a shared store needs QH_MASTER_KEY (the key every instance uses)").
		WithHint("Set QH_MASTER_KEY to the base64 key all instances share; master.key in the data directory is not used with --store or QH_STORE_DSN")
}

// openAppWith opens the store and the service. The previous master keys are
// oldKeys plus those in QH_MASTER_KEY_OLD (comma-separated), so data sealed
// before a rotation stays readable by every command.
func openAppWith(ctx context.Context, home string, opts core.Options, oldKeys ...[]byte) (*app, error) {
	envKey := os.Getenv("QH_MASTER_KEY")
	if storeDSN() != "" && envKey == "" {
		return nil, errSharedStoreNeedsKey()
	}
	fromEnv, err := oldMasterKeys(os.Getenv("QH_MASTER_KEY_OLD"))
	if err != nil {
		return nil, err
	}
	oldKeys = append(append([][]byte(nil), oldKeys...), fromEnv...)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	var key []byte
	if envKey != "" {
		key, err = secret.ParseKey(envKey)
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
	st, err := store.Open(ctx, storeDSN(), home)
	if err != nil {
		return nil, storeOpenError(err, storeDSN() != "")
	}
	pools := pool.New(core.OpenConn, pool.Options{})
	return &app{svc: core.New(st, keys, pools, catalog.New(10*time.Minute), opts), pools: pools, st: st, keys: keys}, nil
}

func (a *app) Close() {
	a.svc.Close()
	a.pools.Close()
	a.st.Close()
}
