package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/sqlx"
)

type fakeEngine struct{ name string }

func (f fakeEngine) Name() string                      { return f.name }
func (f fakeEngine) Dialect() sqlx.Dialect             { return sqlx.Postgres }
func (f fakeEngine) Classify(q string) (Access, error) { return sqlx.Classify(q, sqlx.Postgres) }
func (f fakeEngine) ReferencedObjects(q string) []string {
	return sqlx.ReferencedTables(q, sqlx.Postgres)
}
func (f fakeEngine) Inspect(string) (DSNInfo, error)              { return DSNInfo{}, nil }
func (f fakeEngine) Open(context.Context, ConnSpec) (Conn, error) { return nil, nil }

func TestRegistry(t *testing.T) {
	Register(fakeEngine{"fake-a"})
	if e, err := Get("fake-a"); err != nil || e.Name() != "fake-a" {
		t.Fatalf("Get = %v, %v", e, err)
	}
	_, err := Get("nope")
	if err == nil || !strings.Contains(err.Error(), "error[invalid]: unknown engine \"nope\"") || !strings.Contains(err.Error(), "fake-a") {
		t.Fatalf("Get(nope) err = %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register did not panic")
		}
	}()
	Register(fakeEngine{"fake-a"})
}

func testCatalog() *Catalog {
	users := &Object{Namespace: "public", Name: "users", Kind: KindTable, PrimaryKey: []string{"id"}}
	orders := &Object{Namespace: "public", Name: "orders", Kind: KindTable,
		ForeignKeys: []ForeignKey{{Namespace: "public", Object: "orders", Columns: []string{"user_id"},
			RefNamespace: "public", RefObject: "users", RefColumns: []string{"id"}}}}
	events := &Object{Namespace: "analytics", Name: "Events", Kind: KindTable}
	return &Catalog{Namespaces: []Namespace{
		{Name: "analytics", Objects: []*Object{events}},
		{Name: "public", Objects: []*Object{orders, users}},
	}}
}

func TestLinkAndLookup(t *testing.T) {
	c := testCatalog()
	c.Link()
	c.Link() // idempotent
	users := c.Lookup("users")
	if len(users) != 1 || len(users[0].ReferencedBy) != 1 || users[0].ReferencedBy[0].Object != "orders" {
		t.Fatalf("users.ReferencedBy = %#v", users)
	}
	if got := c.Lookup("PUBLIC.Users"); len(got) != 1 {
		t.Fatalf("case-insensitive qualified lookup = %v", got)
	}
	if got := c.Lookup("events"); len(got) != 1 || got[0].Name != "Events" {
		t.Fatalf("case-insensitive lookup = %v", got)
	}
	if got := c.Lookup("missing"); len(got) != 0 {
		t.Fatalf("missing lookup = %v", got)
	}
	if c.Count() != 3 {
		t.Fatalf("Count = %d", c.Count())
	}
}

func TestOnly(t *testing.T) {
	c := testCatalog()
	if got := c.Only(nil); got != c {
		t.Fatal("Only(nil) should return the catalog unchanged")
	}
	got := c.Only([]string{"Public"})
	if len(got.Namespaces) != 1 || got.Namespaces[0].Name != "public" {
		t.Fatalf("Only(public) = %#v", got.Namespaces)
	}
}
