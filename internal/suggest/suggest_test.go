package suggest

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
)

func TestDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"kitten", "sitting", 3}, {"Email", "email", 0}, {"", "abc", 3}, {"zoë", "zoe", 1},
	}
	for _, c := range cases {
		if got := Distance(c.a, c.b); got != c.want {
			t.Errorf("Distance(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func cands(names ...string) []Candidate {
	out := make([]Candidate, len(names))
	for i, n := range names {
		out[i] = Candidate{Key: n, Label: n}
	}
	return out
}

func TestClosest(t *testing.T) {
	if got := Closest("emial", cands("email", "name", "id"), 3); !reflect.DeepEqual(got, []string{"email"}) {
		t.Fatalf("got %v", got)
	}
	if got := Closest("user", cands("orders", "user_id", "users"), 3); !reflect.DeepEqual(got, []string{"users", "user_id"}) {
		t.Fatalf("got %v", got)
	}
	if got := Closest("qqqq", cands("orders"), 3); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestClosestShortTargetsAreExact(t *testing.T) {
	if got := Closest("id", cands("ip", "pk", "id"), 3); !reflect.DeepEqual(got, []string{"id"}) {
		t.Fatalf("got %v", got)
	}
	if got := Closest("", cands("ip", "id"), 3); got != nil {
		t.Fatalf("got %v", got)
	}
	if got := Closest("id", cands("id"), 0); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestClosestThreeCharTargetAllowsOneEdit(t *testing.T) {
	if got := Closest("abc", cands("abd", "axy"), 3); !reflect.DeepEqual(got, []string{"abd"}) {
		t.Fatalf("got %v", got)
	}
}

func hintCatalog() *engine.Catalog {
	return &engine.Catalog{Namespaces: []engine.Namespace{
		{Name: "audit", Objects: []*engine.Object{{Namespace: "audit", Name: "AuditLog",
			Fields: []engine.Field{{Name: "id"}, {Name: "EventName"}}}}},
		{Name: "public", Objects: []*engine.Object{
			{Namespace: "public", Name: "orders", Fields: []engine.Field{{Name: "id"}, {Name: "user_id"}, {Name: "status"}}},
			{Namespace: "public", Name: "users", Fields: []engine.Field{{Name: "id"}, {Name: "email"}, {Name: "name"}}},
		}},
	}}
}

func TestColumnHintUsesReferencedTables(t *testing.T) {
	got := ColumnHint(hintCatalog(), "u.emial", []string{"users", "orders"})
	if !strings.HasPrefix(got, "Did you mean users.email? ") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "users has: email") || !strings.Contains(got, "orders has: ") {
		t.Fatalf("got %q", got)
	}
}

func TestColumnHintCaseInsensitive(t *testing.T) {
	got := ColumnHint(hintCatalog(), "eventname", []string{"auditlog"})
	if !strings.HasPrefix(got, "Did you mean AuditLog.EventName? ") {
		t.Fatalf("got %q", got)
	}
}

func TestColumnHintWithoutKnownTables(t *testing.T) {
	if got := ColumnHint(hintCatalog(), "emial", nil); got != "Did you mean public.users.email?" {
		t.Fatalf("got %q", got)
	}
	if got := ColumnHint(hintCatalog(), "zzzzzz", []string{"nope"}); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestColumnHintNoCloseMatchStillListsColumns(t *testing.T) {
	got := ColumnHint(hintCatalog(), "zzzzzz", []string{"users"})
	if strings.Contains(got, "Did you mean") || !strings.HasPrefix(got, "users has: ") {
		t.Fatalf("got %q", got)
	}
}

// manyTables builds one namespace with n tables that all have an "email" column.
func manyTables(n int) *engine.Catalog {
	objs := make([]*engine.Object, n)
	for i := range objs {
		objs[i] = &engine.Object{Namespace: "public", Name: fmt.Sprintf("t%d", i),
			Fields: []engine.Field{{Name: "email"}}}
	}
	return &engine.Catalog{Namespaces: []engine.Namespace{{Name: "public", Objects: objs}}}
}

func TestColumnHintAmbiguousTiesGiveNoHint(t *testing.T) {
	if got := ColumnHint(manyTables(6), "email", nil); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := ColumnHint(manyTables(5), "email", nil); !strings.HasPrefix(got, "Did you mean t0.email, ") {
		t.Fatalf("got %q", got)
	}
}

func TestTableHint(t *testing.T) {
	if got := TableHint(hintCatalog(), "user"); got != "Did you mean public.users?" {
		t.Fatalf("got %q", got)
	}
	if got := TableHint(hintCatalog(), "auditlogs"); got != "Did you mean audit.AuditLog?" {
		t.Fatalf("got %q", got)
	}
	if got := TableHint(hintCatalog(), "qqq"); got != "Use the schema tool to list tables" {
		t.Fatalf("got %q", got)
	}
}
