package cli

import (
	"os"
	"path/filepath"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/core"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
)

type app struct {
	svc   *core.Service
	pools *pool.Manager
	st    *store.SQLite
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

func openApp(home string) (*app, error) {
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
	keys, err := secret.NewKeyring(key)
	if err != nil {
		return nil, err
	}
	st, err := store.OpenSQLite(filepath.Join(home, "qh.db"))
	if err != nil {
		return nil, err
	}
	pools := pool.New(core.OpenConn, pool.Options{})
	return &app{svc: core.New(st, keys, pools, catalog.New(10*time.Minute)), pools: pools, st: st}, nil
}

func (a *app) Close() {
	a.pools.Close()
	a.st.Close()
}
