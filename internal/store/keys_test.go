package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestWorkspaces(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWorkspace(ctx, "team"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	list, err := s.ListWorkspaces(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "default" || list[1].ID != "team" || list[1].CreatedAt.IsZero() {
		t.Fatalf("list: %+v %v", list, err)
	}
	c := sample("shop")
	c.WorkspaceID = "team"
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	k := &APIKey{ID: NewID(), WorkspaceID: "team", Prefix: "abcd1234", Hash: []byte{1}, Scopes: []string{"query"}}
	if err := s.CreateKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConnection(ctx, "team", "shop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("connection survived its workspace: %v", err)
	}
	if _, err := s.GetKeyByPrefix(ctx, "abcd1234"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("key survived its workspace: %v", err)
	}
	if err := s.DeleteWorkspace(ctx, "team"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestKeys(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if n, err := s.CountKeys(ctx); err != nil || n != 0 {
		t.Fatalf("count = %d %v", n, err)
	}
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	admin := &APIKey{ID: NewID(), Prefix: "admin001", Hash: []byte{9, 9}, Scopes: []string{"admin"}, Name: "bootstrap"}
	ws := &APIKey{ID: NewID(), WorkspaceID: "default", Prefix: "work0001", Hash: []byte{1, 2}, Scopes: []string{"query", "manage"}, Name: "ci", ExpiresAt: &exp}
	for _, k := range []*APIKey{admin, ws} {
		if err := s.CreateKey(ctx, k); err != nil {
			t.Fatal(err)
		}
		if k.CreatedAt.IsZero() {
			t.Fatal("CreatedAt not set")
		}
	}
	dup := *ws
	dup.ID = NewID()
	if err := s.CreateKey(ctx, &dup); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate prefix: %v", err)
	}
	orphan := &APIKey{ID: NewID(), WorkspaceID: "nope", Prefix: "orphan01", Hash: []byte{1}, Scopes: []string{"query"}}
	if err := s.CreateKey(ctx, orphan); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown workspace: %v", err)
	}
	got, err := s.GetKeyByPrefix(ctx, "work0001")
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "default" || !reflect.DeepEqual(got.Scopes, []string{"query", "manage"}) || got.Name != "ci" ||
		got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || got.LastUsedAt != nil || !reflect.DeepEqual(got.Hash, []byte{1, 2}) {
		t.Fatalf("got %+v", got)
	}
	used := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if err := s.TouchKey(ctx, got.ID, used); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetKeyByPrefix(ctx, "work0001")
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(used) {
		t.Fatalf("last used %v", got.LastUsedAt)
	}
	if l, _ := s.ListKeys(ctx, ""); len(l) != 1 || l[0].Prefix != "admin001" || l[0].WorkspaceID != "" {
		t.Fatalf("admin keys %+v", l)
	}
	if l, _ := s.ListKeys(ctx, "default"); len(l) != 1 || l[0].Prefix != "work0001" {
		t.Fatalf("workspace keys %+v", l)
	}
	if l, _ := s.ListAllKeys(ctx); len(l) != 2 {
		t.Fatalf("all keys %+v", l)
	}
	if n, _ := s.CountKeys(ctx); n != 2 {
		t.Fatalf("count %d", n)
	}
	if err := s.DeleteKey(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKey(ctx, admin.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

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
