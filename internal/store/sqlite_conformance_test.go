package store_test

import (
	"path/filepath"
	"testing"

	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/store/storetest"
)

func TestSQLiteConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "qh.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}
