package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUpgradeFromVersion1(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	s.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DROP TABLE query_log", "DROP TABLE api_keys",
		"ALTER TABLE connections DROP COLUMN ssh", "ALTER TABLE connections DROP COLUMN refs", "PRAGMA user_version = 1"} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	s2, err := OpenSQLite(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.CreateKey(ctx, &APIKey{ID: NewID(), Prefix: "upgr0001", Hash: []byte{1}, Scopes: []string{"admin"}}); err != nil {
		t.Fatalf("v1 store not upgraded: %v", err)
	}
	c := sample("upgraded")
	c.SSH = "{\"host\":\"b\"}"
	if err := s2.CreateConnection(ctx, c); err != nil {
		t.Fatalf("ssh column missing after upgrade: %v", err)
	}
}
