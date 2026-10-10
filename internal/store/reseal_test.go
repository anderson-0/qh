package store

import (
	"context"
	"errors"
	"testing"
)

func TestResealConnections(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.CreateWorkspace(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	a, b, c := sample("a"), sample("b"), sample("c")
	c.WorkspaceID = "team"
	for _, x := range []*Connection{a, b, c} {
		if err := s.CreateConnection(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	seen := 0
	n, err := s.ResealConnections(ctx, func(x *Connection) ([]byte, uint32, error) {
		seen++
		if x.Name == "b" {
			return nil, 0, nil // unchanged
		}
		return []byte("new-" + x.Name), 9, nil
	})
	if err != nil || n != 2 || seen != 3 {
		t.Fatalf("reseal: n=%d seen=%d %v", n, seen, err)
	}
	got, _ := s.GetConnection(ctx, "team", "c")
	if string(got.Secret) != "new-c" || got.SecretKeyID != 9 || got.Version != 1 {
		t.Fatalf("c: %q %d v%d", got.Secret, got.SecretKeyID, got.Version)
	}
	if got, _ := s.GetConnection(ctx, DefaultWorkspace, "b"); string(got.Secret) != "\x01\x02\x03" || got.SecretKeyID != 7 {
		t.Fatalf("b changed: %q %d", got.Secret, got.SecretKeyID)
	}

	boom := errors.New("boom")
	_, err = s.ResealConnections(ctx, func(x *Connection) ([]byte, uint32, error) {
		if x.Name == "c" {
			return nil, 0, boom
		}
		return []byte("second"), 10, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error: %v", err)
	}
	if got, _ := s.GetConnection(ctx, DefaultWorkspace, "a"); string(got.Secret) != "new-a" {
		t.Fatalf("failed reseal changed a: %q", got.Secret)
	}
}
