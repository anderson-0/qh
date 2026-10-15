package format

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
)

func schemaCatalog() *engine.Catalog {
	events := &engine.Object{Namespace: "analytics", Name: "events", Kind: engine.KindTable, RowEstimate: 10_000_000,
		Fields:     []engine.Field{{Name: "id", Type: "bigint"}, {Name: "user_id", Type: "bigint", Nullable: true}},
		PrimaryKey: []string{"id"},
		ForeignKeys: []engine.ForeignKey{{Namespace: "analytics", Object: "events", Columns: []string{"user_id"},
			RefNamespace: "public", RefObject: "users", RefColumns: []string{"id"}}}}
	orderItems := &engine.Object{Namespace: "public", Name: "order_items", Kind: engine.KindTable, RowEstimate: 4_800_000,
		Fields:     []engine.Field{{Name: "order_id", Type: "bigint"}, {Name: "sku", Type: "text"}},
		PrimaryKey: []string{"order_id", "sku"},
		ForeignKeys: []engine.ForeignKey{{Namespace: "public", Object: "order_items", Columns: []string{"order_id"},
			RefNamespace: "public", RefObject: "orders", RefColumns: []string{"id"}}},
		Indexes: []engine.Index{{Name: "order_items_pkey", Columns: []string{"order_id", "sku"}, Unique: true}}}
	orders := &engine.Object{Namespace: "public", Name: "orders", Kind: engine.KindTable, RowEstimate: 1_200_000, Comment: "Customer orders",
		Fields: []engine.Field{
			{Name: "id", Type: "bigint", Default: "nextval('orders_id_seq'::regclass)"},
			{Name: "user_id", Type: "bigint"},
			{Name: "status", Type: "order_status", Enum: []string{"pending", "paid", "shipped"}},
			{Name: "total", Type: "numeric(10,2)", Nullable: true},
		},
		PrimaryKey: []string{"id"},
		ForeignKeys: []engine.ForeignKey{{Namespace: "public", Object: "orders", Columns: []string{"user_id"},
			RefNamespace: "public", RefObject: "users", RefColumns: []string{"id"}}},
		Indexes: []engine.Index{{Name: "orders_user_id", Columns: []string{"user_id"}}}}
	paid := &engine.Object{Namespace: "public", Name: "paid_orders", Kind: engine.KindView, RowEstimate: -1,
		Fields: []engine.Field{{Name: "id", Type: "bigint", Nullable: true}}}
	users := &engine.Object{Namespace: "public", Name: "users", Kind: engine.KindTable, RowEstimate: 90_000, Comment: "App users",
		Fields: []engine.Field{
			{Name: "id", Type: "bigint"},
			{Name: "email", Type: "text", Comment: "Login email"},
			{Name: "created_at", Type: "timestamptz", Nullable: true, Default: "now()"},
		},
		PrimaryKey: []string{"id"},
		Indexes: []engine.Index{
			{Name: "users_pkey", Columns: []string{"id"}, Unique: true},
			{Name: "users_email_key", Columns: []string{"email"}, Unique: true},
		}}
	c := &engine.Catalog{Namespaces: []engine.Namespace{
		{Name: "analytics", Objects: []*engine.Object{events}},
		{Name: "public", Objects: []*engine.Object{orderItems, orders, paid, users}},
	}}
	c.Link()
	return c
}

func TestOverview(t *testing.T) {
	got, n := Schema(schemaCatalog(), SchemaOpts{})
	want := "analytics: 1 table\n" +
		"  events ~10M: id PK, user_id→public.users.id\n" +
		"public: 3 tables, 1 view\n" +
		"  order_items ~4.8M: order_id PK→orders.id, sku PK\n" +
		"  orders ~1.2M: id PK, user_id→users.id, status [pending|paid|shipped], total\n" +
		"  paid_orders (view): id\n" +
		"  users ~90k: id PK, email, created_at\n"
	if got != want || n != 5 {
		t.Fatalf("n=%d got:\n%s\nwant:\n%s", n, got, want)
	}
}

func TestOverviewCap(t *testing.T) {
	got, _ := Schema(schemaCatalog(), SchemaOpts{MaxOverview: 3})
	want := "analytics: 1 table\n" +
		"  events ~10M\n" +
		"public: 3 tables, 1 view\n" +
		"  order_items ~4.8M · orders ~1.2M · …\n" +
		"-- showing 3 of 5 objects; use pattern= to narrow\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A schema whose column listing would pass the budget gets names only.
func TestOverviewFallsBackToNames(t *testing.T) {
	c := schemaCatalog()
	var fields []engine.Field
	for i := range RichOverviewBudget / 10 {
		fields = append(fields, engine.Field{Name: fmt.Sprintf("column_%04d", i)})
	}
	c.Namespaces[1].Objects[3].Fields = fields
	got, _ := Schema(c, SchemaOpts{})
	if strings.Contains(got, "column_") || !strings.Contains(got, "order_items ~4.8M · orders ~1.2M") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestEmptyCatalog(t *testing.T) {
	got, n := Schema(&engine.Catalog{}, SchemaOpts{})
	if got != "-- no tables or views found\n" || n != 0 {
		t.Fatalf("got %q n=%d", got, n)
	}
}

func TestDetail(t *testing.T) {
	got, n := Schema(schemaCatalog(), SchemaOpts{Object: "ORDERS"})
	want := "public.orders ~1.2M rows: Customer orders\n" +
		"  id bigint PK =auto\n" +
		"  user_id bigint NN → users.id\n" +
		"  status order_status NN [pending|paid|shipped]\n" +
		"  total numeric(10,2)\n" +
		"  ← order_items.order_id\n" +
		"  idx (user_id)\n"
	if got != want || n != 1 {
		t.Fatalf("n=%d got:\n%s\nwant:\n%s", n, got, want)
	}
}

func TestDetailIncomingAcrossNamespaces(t *testing.T) {
	got, _ := Schema(schemaCatalog(), SchemaOpts{Object: "public.users"})
	want := "public.users ~90k rows: App users\n" +
		"  id bigint PK\n" +
		"  email text NN  # Login email\n" +
		"  created_at timestamptz =now()\n" +
		"  ← analytics.events.user_id, orders.user_id\n" +
		"  idx unique(email)\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDetailCompositeKeyAndView(t *testing.T) {
	got, n := Schema(schemaCatalog(), SchemaOpts{Pattern: "order_items"})
	want := "public.order_items ~4.8M rows\n" +
		"  order_id bigint PK → orders.id\n" +
		"  sku text PK\n"
	if got != want || n != 1 {
		t.Fatalf("n=%d got:\n%s", n, got)
	}
	got, _ = Schema(schemaCatalog(), SchemaOpts{Object: "paid_orders"})
	if got != "public.paid_orders (view)\n  id bigint\n" {
		t.Fatalf("view got %q", got)
	}
	got, _ = Schema(schemaCatalog(), SchemaOpts{Object: "events"})
	if want := "analytics.events ~10M rows\n  id bigint PK\n  user_id bigint → public.users.id\n"; got != want {
		t.Fatalf("cross-namespace FK got %q", got)
	}
}

// Every single-column FK must survive in catalog order, including when a
// composite FK shares the column or the overview folds an inferred child.
func TestSchemaMultipleForeignKeys(t *testing.T) {
	cases := []struct {
		name         string
		object       *engine.Object
		wantOverview string
		wantDetail   string
	}{
		{
			name: "SQL column with a composite reference too",
			object: &engine.Object{Namespace: "public", Name: "orders", Kind: engine.KindTable, RowEstimate: -1,
				Fields:     []engine.Field{{Name: "id", Type: "bigint"}, {Name: "owner_id", Type: "bigint"}, {Name: "tenant_id", Type: "bigint"}},
				PrimaryKey: []string{"id"},
				ForeignKeys: []engine.ForeignKey{
					{Columns: []string{"owner_id"}, RefNamespace: "public", RefObject: "users", RefColumns: []string{"id"}},
					{Columns: []string{"owner_id", "tenant_id"}, RefNamespace: "public", RefObject: "tenant_users", RefColumns: []string{"id", "tenant_id"}},
					{Columns: []string{"owner_id"}, RefNamespace: "archive", RefObject: "users", RefColumns: []string{"id"}},
				}},
			wantOverview: "public: 1 table\n  orders: id PK, owner_id→{users.id,archive.users.id}, tenant_id\n",
			wantDetail: "public.orders\n  id bigint PK\n  owner_id bigint NN → users.id, archive.users.id\n" +
				"  tenant_id bigint NN\n  (owner_id,tenant_id) → tenant_users(id,tenant_id)\n",
		},
		{
			name: "primary key references keep matching column shorthand",
			object: &engine.Object{Namespace: "public", Name: "orders", Kind: engine.KindTable, RowEstimate: -1,
				Fields:     []engine.Field{{Name: "id", Type: "bigint"}},
				PrimaryKey: []string{"id"},
				ForeignKeys: []engine.ForeignKey{
					{Columns: []string{"id"}, RefNamespace: "public", RefObject: "users", RefColumns: []string{"id"}},
					{Columns: []string{"id"}, RefNamespace: "archive", RefObject: "users", RefColumns: []string{"id"}},
				}},
			wantOverview: "public: 1 table\n  orders: id PK→{users,archive.users}\n",
			wantDetail:   "public.orders\n  id bigint PK → users.id, archive.users.id\n",
		},
		{
			name: "three references to different columns of one table",
			object: &engine.Object{Namespace: "public", Name: "orders", Kind: engine.KindTable, RowEstimate: -1,
				Fields: []engine.Field{{Name: "owner_id", Type: "bigint", Nullable: true}},
				ForeignKeys: []engine.ForeignKey{
					{Columns: []string{"owner_id"}, RefNamespace: "public", RefObject: "users", RefColumns: []string{"z_id"}},
					{Columns: []string{"owner_id"}, RefNamespace: "public", RefObject: "users", RefColumns: []string{"a_id"}},
					{Columns: []string{"owner_id"}, RefNamespace: "public", RefObject: "users", RefColumns: []string{"m_id"}},
				}},
			wantOverview: "public: 1 table\n  orders: owner_id→{users.z_id,users.a_id,users.m_id}\n",
			wantDetail:   "public.orders\n  owner_id bigint → users.z_id, users.a_id, users.m_id\n",
		},
		{
			name: "inferred scalar references omit id in overview",
			object: &engine.Object{Namespace: "public", Name: "orders", Kind: engine.KindCollection, RowEstimate: -1,
				Fields: []engine.Field{{Name: "owner_id", Type: "int", Inferred: true}},
				ForeignKeys: []engine.ForeignKey{
					{Columns: []string{"owner_id"}, RefNamespace: "public", RefObject: "users", RefColumns: []string{"_id"}},
					{Columns: []string{"owner_id"}, RefNamespace: "archive", RefObject: "users", RefColumns: []string{"_id"}},
				}},
			wantOverview: "public: 1 collection\n  orders: owner_id→{users,archive.users}\n",
			wantDetail:   "public.orders\n  owner_id int NN → users._id, archive.users._id\n",
		},
		{
			name: "folded inferred child keeps all references when promoted",
			object: &engine.Object{Namespace: "public", Name: "orders", Kind: engine.KindCollection, RowEstimate: -1,
				Fields: inferred("items", "items[].name", "items[].qty", "items[].owner_id"),
				ForeignKeys: []engine.ForeignKey{
					{Columns: []string{"items[].owner_id"}, RefNamespace: "public", RefObject: "users", RefColumns: []string{"_id"}},
					{Columns: []string{"items[].owner_id"}, RefNamespace: "archive", RefObject: "users", RefColumns: []string{"_id"}},
				}},
			wantOverview: "public: 1 collection\n  orders: items{owner_id→{users,archive.users},name,…}\n",
			wantDetail: "public.orders\n  items NN\n  items[].name NN\n  items[].qty NN\n" +
				"  items[].owner_id NN → users._id, archive.users._id\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := &engine.Catalog{Namespaces: []engine.Namespace{{Name: "public", Objects: []*engine.Object{tc.object}}}}
			for _, output := range []struct {
				name string
				opts SchemaOpts
				want string
			}{{"overview", SchemaOpts{}, tc.wantOverview}, {"detail", SchemaOpts{Object: "public.orders"}, tc.wantDetail}} {
				t.Run(output.name, func(t *testing.T) {
					if got, n := Schema(cat, output.opts); got != output.want || n != 1 {
						t.Fatalf("matched=%d got:\n%s\nwant:\n%s", n, got, output.want)
					}
				})
			}
		})
	}
}

func TestPatternMatching(t *testing.T) {
	cases := []struct {
		pattern string
		want    int
	}{
		{"order", 3},   // substring: order_items, orders, paid_orders
		{"order*", 2},  // prefix
		{"*ORDERS", 2}, // suffix, case-insensitive: orders, paid_orders
		{"public.u*", 1},
		{"zzz", 0},
	}
	for _, c := range cases {
		if _, n := Schema(schemaCatalog(), SchemaOpts{Pattern: c.pattern}); n != c.want {
			t.Errorf("pattern %q matched %d, want %d", c.pattern, n, c.want)
		}
	}
}

func TestTooManyMatchesListsNames(t *testing.T) {
	got, n := Schema(schemaCatalog(), SchemaOpts{Pattern: "order*", MaxDetail: 1})
	want := "-- 2 objects match; showing names only. Narrow the pattern or use object=\n" +
		"public.order_items, public.orders\n"
	if got != want || n != 2 {
		t.Fatalf("n=%d got %q", n, got)
	}
}

func TestCompact(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1000: "1k", 1200: "1.2k", 90_000: "90k",
		1_200_000: "1.2M", 10_000_000: "10M", 48_000_000: "48M", 3_400_000_000: "3.4B"}
	for n, want := range cases {
		if got := Compact(n); got != want {
			t.Errorf("Compact(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestMatching(t *testing.T) {
	c := schemaCatalog()
	if got := Matching(c, "", ""); len(got) != 5 {
		t.Fatalf("all: %d", len(got))
	}
	if got := Matching(c, "order*", ""); len(got) != 2 {
		t.Fatalf("pattern: %d", len(got))
	}
	if got := Matching(c, "", "public.users"); len(got) != 1 || got[0].Name != "users" {
		t.Fatalf("object: %+v", got)
	}
}

func TestOverviewFoldsNestedFields(t *testing.T) {
	c := &engine.Catalog{Namespaces: []engine.Namespace{{Name: "shop", Objects: []*engine.Object{
		{Namespace: "shop", Name: "customers", Kind: engine.KindCollection, RowEstimate: 599, PrimaryKey: []string{"_id"},
			Fields: inferred("_id", "name", "address", "address.city",
				"address.country", "address.zip", "items", "items[].product_id", "items[].qty",
				"cast", "cast[].given_nm", "cast[].family_nm", "cast[].performer_key",
				"store_id"),
			ForeignKeys: []engine.ForeignKey{
				{Namespace: "shop", Object: "customers", Columns: []string{"store_id"},
					RefNamespace: "shop", RefObject: "stores", RefColumns: []string{"_id"}},
				{Namespace: "shop", Object: "customers", Columns: []string{"items[].product_id"},
					RefNamespace: "shop", RefObject: "products", RefColumns: []string{"_id"}},
				{Namespace: "shop", Object: "customers", Columns: []string{"cast[].performer_key"},
					RefNamespace: "shop", RefObject: "performer", RefColumns: []string{"_id"}}}},
	}}}}
	out, _ := Schema(c, SchemaOpts{})
	want := "shop: 1 collection\n  customers ~599: _id PK, name, address{city,country,…}, items{product_id→products,qty}, " +
		"cast{performer_key→performer,given_nm,…}, store_id→stores\n"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}

func TestKindCountsListCollectionsBeforeViews(t *testing.T) {
	objs := []*engine.Object{{Kind: engine.KindView}, {Kind: engine.KindCollection}, {Kind: engine.KindCollection}}
	if got := kindCounts(objs); got != "2 collections, 1 view" {
		t.Errorf("kindCounts = %q", got)
	}
}

func inferred(names ...string) []engine.Field {
	out := make([]engine.Field, len(names))
	for i, n := range names {
		out[i] = engine.Field{Name: n, Inferred: true}
	}
	return out
}

// Folding and the →collection form of a reference to _id apply to sampled
// fields only: SQL columns that happen to contain dots, or a column called
// _id, render as before.
func TestOverviewLeavesSQLColumnsAlone(t *testing.T) {
	c := &engine.Catalog{Namespaces: []engine.Namespace{{Name: "public", Objects: []*engine.Object{
		{Namespace: "public", Name: "people", Kind: engine.KindTable, RowEstimate: -1,
			Fields: []engine.Field{{Name: "address"}, {Name: "address.city"}, {Name: "address.zip"}, {Name: "address.country"}, {Name: "owner"}},
			ForeignKeys: []engine.ForeignKey{{Namespace: "public", Object: "people", Columns: []string{"owner"},
				RefNamespace: "public", RefObject: "users", RefColumns: []string{"_id"}}}},
	}}}}
	out, _ := Schema(c, SchemaOpts{})
	want := "public: 1 table\n  people: address, address.city, address.zip, address.country, owner→users._id\n"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}

// A namespace the engine could not list is shown with its reason, even though
// it has no objects; a normal namespace never carries one.
func TestOverviewShowsUnlistedNamespace(t *testing.T) {
	c := schemaCatalog()
	c.Namespaces = append([]engine.Namespace{{Name: "archive", Note: "not listed: query_error: not authorized"}}, c.Namespaces...)
	want := "archive: (not listed: query_error: not authorized)\n" +
		"analytics: 1 table\n" +
		"  events ~10M: id PK, user_id→public.users.id\n" +
		"public: 3 tables, 1 view\n" +
		"  order_items ~4.8M: order_id PK→orders.id, sku PK\n" +
		"  orders ~1.2M: id PK, user_id→users.id, status [pending|paid|shipped], total\n" +
		"  paid_orders (view): id\n" +
		"  users ~90k: id PK, email, created_at\n"
	if got, n := Schema(c, SchemaOpts{}); got != want || n != 5 {
		t.Fatalf("rich n=%d got:\n%s", n, got)
	}
	// The names-only overview shows it too.
	got, _ := Schema(c, SchemaOpts{MaxOverview: 3})
	if !strings.HasPrefix(got, "archive: (not listed: query_error: not authorized)\nanalytics: 1 table\n") {
		t.Errorf("capped:\n%s", got)
	}
	// Alone, it is not reported as an empty catalog.
	only := &engine.Catalog{Namespaces: []engine.Namespace{{Name: "archive", Note: "not listed: timeout"}}}
	if got, n := Schema(only, SchemaOpts{}); got != "archive: (not listed: timeout)\n" || n != 0 {
		t.Errorf("only: n=%d %q", n, got)
	}
	// A namespace that is simply empty is still not shown.
	empty := &engine.Catalog{Namespaces: []engine.Namespace{{Name: "blank"}}}
	if got, _ := Schema(empty, SchemaOpts{}); got != "-- no tables or views found\n" {
		t.Errorf("empty: %q", got)
	}
}
