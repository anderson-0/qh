package core

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/catalog"
	"github.com/anderson-0/qh/internal/pool"
	"github.com/anderson-0/qh/internal/secret"
	"github.com/anderson-0/qh/internal/store"
	"github.com/anderson-0/qh/internal/testutil"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, secret.KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func keyring(t *testing.T, primary []byte, old ...[]byte) *secret.Keyring {
	t.Helper()
	k, err := secret.NewKeyring(primary, old...)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func serviceWith(t *testing.T, st *store.SQLite, keys *secret.Keyring) *Service {
	t.Helper()
	pools := pool.New(OpenConn, pool.Options{})
	t.Cleanup(pools.Close)
	return New(st, keys, pools, catalog.New(time.Minute))
}

// insertRaw stores a connection sealed in the pre-Plan-4 format.
func insertRaw(t *testing.T, st *store.SQLite, keys *secret.Keyring, name, dsn string) {
	t.Helper()
	rec := &store.Connection{ID: store.NewID(), WorkspaceID: ws, Name: name, Engine: "sqlite", Database: dsn,
		Mode: "ro", MaxRows: 100, TimeoutMS: 30000, PoolSize: 5}
	sealed, err := keys.Seal([]byte(dsn), []byte(rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	rec.Secret, rec.SecretKeyID = sealed, keys.PrimaryID()
	if err := st.CreateConnection(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

func TestRotateMasterKey(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	db := testutil.SQLiteFixture(t)
	oldKey, newKey := randomKey(t), randomKey(t)
	old := keyring(t, oldKey)
	if _, _, err := serviceWith(t, st, old).CreateConnection(ctx, ws, ConnInput{Name: "shop", Engine: "sqlite", DSN: db}, false); err != nil {
		t.Fatal(err)
	}
	insertRaw(t, st, old, "legacy", db)

	n, cur, err := serviceWith(t, st, keyring(t, newKey, oldKey)).RotateMasterKey(ctx)
	if err != nil || n != 2 || cur != 0 {
		t.Fatalf("rotate: %d %d %v", n, cur, err)
	}
	fresh := serviceWith(t, st, keyring(t, newKey))
	for _, name := range []string{"shop", "legacy"} {
		rec, _ := st.GetConnection(ctx, ws, name)
		if rec.SecretKeyID != secret.KeyID(newKey) || rec.Version != 1 {
			t.Fatalf("%s: key %08x version %d", name, rec.SecretKeyID, rec.Version)
		}
		if out, err := fresh.Query(ctx, ws, QueryInput{Source: name, Query: "SELECT count(*) AS n FROM orders"}); err != nil || !strings.HasPrefix(out, "n\n250\n") {
			t.Fatalf("%s with only the new key: %q %v", name, out, err)
		}
	}
	if n, cur, err := serviceWith(t, st, keyring(t, newKey, oldKey)).RotateMasterKey(ctx); err != nil || n != 0 || cur != 2 {
		t.Fatalf("second rotation: %d %d %v", n, cur, err)
	}

	// A connection sealed with a key the keyring lacks stops the rotation, and nothing changes.
	stranger := keyring(t, randomKey(t))
	insertRaw(t, st, stranger, "stray", db)
	insertRaw(t, st, old, "later", db)
	_, _, err = serviceWith(t, st, keyring(t, newKey, oldKey)).RotateMasterKey(ctx)
	if !strings.HasPrefix(errText(err), `error[invalid]: connection "stray" in workspace "default" cannot be opened with the current or the old master key`) {
		t.Fatalf("stray: %q", errText(err))
	}
	if rec, _ := st.GetConnection(ctx, ws, "later"); rec.SecretKeyID != secret.KeyID(oldKey) {
		t.Fatal("a failed rotation changed a connection")
	}
}
