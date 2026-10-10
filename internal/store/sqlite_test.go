package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func openTemp(t *testing.T) (*SQLite, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "with space", "qh.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func sample(name string) *Connection {
	return &Connection{
		ID: NewID(), WorkspaceID: DefaultWorkspace, Name: name, Engine: "postgres",
		Secret: []byte{1, 2, 3}, SecretKeyID: 7, Host: "db.local", Port: 5432,
		Database: "shop", Username: "reader", Description: "Shop DB", Mode: "ro",
		MaxRows: 100, TimeoutMS: 30000, PoolSize: 5, AllowedNamespaces: []string{"public"},
	}
}

func TestCreateGetList(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	c := sample("shop")
	if err := s.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 || c.CreatedAt.IsZero() {
		t.Fatalf("after create: version %d created %v", c.Version, c.CreatedAt)
	}
	got, err := s.GetConnection(ctx, DefaultWorkspace, "shop")
	if err != nil {
		t.Fatal(err)
	}
	got.CreatedAt, got.UpdatedAt = c.CreatedAt, c.UpdatedAt
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("got %#v\nwant %#v", got, c)
	}
	if err := s.CreateConnection(ctx, sample("analytics")); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListConnections(ctx, DefaultWorkspace)
	if err != nil || len(list) != 2 || list[0].Name != "analytics" || list[1].Name != "shop" {
		t.Fatalf("list = %v, %v", list, err)
	}
}

func TestDuplicateAndMissing(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	_ = s.CreateConnection(ctx, sample("shop"))
	if err := s.CreateConnection(ctx, sample("shop")); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate err = %v", err)
	}
	if _, err := s.GetConnection(ctx, DefaultWorkspace, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing err = %v", err)
	}
	if err := s.DeleteConnection(ctx, DefaultWorkspace, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
	if _, err := s.GetConnection(ctx, "other-workspace", "shop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other workspace err = %v", err)
	}
}

func TestUpdateVersioning(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	c := sample("shop")
	_ = s.CreateConnection(ctx, c)
	stale := *c
	c.MaxRows = 500
	if err := s.UpdateConnection(ctx, c); err != nil || c.Version != 2 {
		t.Fatalf("update err %v version %d", err, c.Version)
	}
	got, _ := s.GetConnection(ctx, DefaultWorkspace, "shop")
	if got.MaxRows != 500 || got.Version != 2 {
		t.Fatalf("stored %d v%d", got.MaxRows, got.Version)
	}
	if err := s.UpdateConnection(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update err = %v", err)
	}
	missing := sample("nope")
	missing.Version = 1
	if err := s.UpdateConnection(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update err = %v", err)
	}
}

func TestDeleteAndReopen(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	_ = s.CreateConnection(ctx, sample("keep"))
	_ = s.CreateConnection(ctx, sample("drop"))
	if err := s.DeleteConnection(ctx, DefaultWorkspace, "drop"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	list, _ := s2.ListConnections(ctx, DefaultWorkspace)
	if len(list) != 1 || list[0].Name != "keep" {
		t.Fatalf("after reopen: %v", list)
	}
}

func TestConcurrentFirstOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "with space", "qh.db")
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := OpenSQLite(path)
			if err != nil {
				errs <- err
				return
			}
			errs <- s.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateConnection(ctx, sample("shop")); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListConnections(ctx, DefaultWorkspace)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workspaces WHERE id = ?`, DefaultWorkspace).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("default workspace rows = %d, %v", rows, err)
	}
}
