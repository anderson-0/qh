package store

import (
	"context"
	"path/filepath"
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
