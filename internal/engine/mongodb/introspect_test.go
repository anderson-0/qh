package mongodb

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/engine/enginetest"
)

func TestCatalog(t *testing.T) {
	for _, image := range []string{"mongo:7", mongo8} {
		t.Run(image, func(t *testing.T) {
			uri := mongoURI(t, image)
			db := loadFixtureNamed(t, uri, "shop")
			enginetest.RunCatalog(t, enginetest.CatalogHarness{Engine: Engine{},
				Spec:     engine.ConnSpec{Engine: "mongodb", DSN: withDB(uri, db), Mode: engine.ModeRO},
				IDColumn: "_id", GoldenPrefix: "mongodb"})
		})
	}
}

// insert adds docs to db.coll through the driver.
func insert(t *testing.T, uri, db, coll string, docs ...any) {
	t.Helper()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	if _, err := client.Database(db).Collection(coll).InsertMany(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
}

func objectNamed(t *testing.T, cat *engine.Catalog, name string) *engine.Object {
	t.Helper()
	objs := cat.Lookup(name)
	if len(objs) != 1 {
		t.Fatalf("%s: %d objects", name, len(objs))
	}
	return objs[0]
}

func TestReferences(t *testing.T) {
	uri := mongoURI(t, mongo8)
	db := loadFixture(t, uri)
	insert(t, uri, db, "ghost_refs", bson.D{{Key: "user_id", Value: int32(100)}}, bson.D{{Key: "user_id", Value: int32(101)}},
		bson.D{{Key: "user_id", Value: int32(102)}}, bson.D{{Key: "user_id", Value: int32(103)}}, bson.D{{Key: "user_id", Value: int32(1)}})
	insert(t, uri, db, "catalog_item", bson.D{{Key: "_id", Value: int32(1)}}, bson.D{{Key: "_id", Value: int32(2)}})
	insert(t, uri, db, "loan", bson.D{{Key: "item_key", Value: int32(1)}, {Key: "lines", Value: bson.A{bson.D{{Key: "order_id", Value: int32(2)}}}}},
		bson.D{{Key: "item_key", Value: int32(2)}})
	c := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: withDB(uri, db), Mode: engine.ModeRO})
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fks := objectNamed(t, cat, "ghost_refs").ForeignKeys; len(fks) != 0 {
		t.Errorf("unverified reference kept: %+v", fks)
	}
	loan := objectNamed(t, cat, "loan")
	has := func(col, ref string) bool {
		for _, fk := range loan.ForeignKeys {
			if fk.Columns[0] == col && fk.RefObject == ref && fk.RefColumns[0] == "_id" {
				return true
			}
		}
		return false
	}
	if !has("item_key", "catalog_item") || !has("lines[].order_id", "orders") {
		t.Errorf("loan FKs: %+v", loan.ForeignKeys)
	}
}

func TestIntrospectSurvivesBrokenView(t *testing.T) {
	uri := mongoURI(t, mongo8)
	db := loadFixture(t, uri)
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	broken := bson.A{bson.D{{Key: "$project", Value: bson.D{{Key: "x", Value: bson.D{{Key: "$divide", Value: bson.A{1, 0}}}}}}}}
	if err := client.Database(db).CreateView(context.Background(), "broken_view", "orders", broken); err != nil {
		t.Fatal(err)
	}
	c := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: withDB(uri, db), Mode: engine.ModeRO})
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v := objectNamed(t, cat, "broken_view")
	if v.Kind != engine.KindView || len(v.Fields) != 0 {
		t.Errorf("broken view: %+v", v)
	}
	if len(objectNamed(t, cat, "orders").Fields) == 0 {
		t.Error("orders lost its fields")
	}
}

func TestIntrospectDatabases(t *testing.T) {
	uri := mongoURI(t, mongo8)
	a, b := loadFixture(t, uri), loadFixture(t, uri)
	all := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: uri, Mode: engine.ModeRO})
	cat, err := all.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, ns := range cat.Namespaces {
		names[ns.Name] = true
	}
	if !names[a] || !names[b] || names["admin"] || names["local"] || names["config"] {
		t.Errorf("namespaces: %v", names)
	}
	only := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: uri, Mode: engine.ModeRO, Namespaces: []string{a}})
	cat, err = only.Introspect(context.Background())
	if err != nil || len(cat.Namespaces) != 1 || cat.Namespaces[0].Name != a {
		t.Fatalf("allowed: %+v %v", cat, err)
	}
	if !strings.HasPrefix(a, "fx_") {
		t.Fatal(a)
	}
}

func TestReferenceBoundaryAndNumericIDs(t *testing.T) {
	uri := mongoURI(t, mongo8)
	db := loadFixture(t, uri)
	refs := func(ids ...int32) []any {
		var out []any
		for _, id := range ids {
			out = append(out, bson.D{{Key: "order_id", Value: id}})
		}
		return out
	}
	insert(t, uri, db, "four_of_five", refs(1, 2, 3, 4, 999)...)
	insert(t, uri, db, "three_of_five", refs(1, 2, 3, 998, 999)...)
	insert(t, uri, db, "accounts", bson.D{{Key: "_id", Value: int64(1)}}, bson.D{{Key: "_id", Value: int64(2)}})
	insert(t, uri, db, "ledger", bson.D{{Key: "account_id", Value: int32(1)}}, bson.D{{Key: "account_id", Value: int32(2)}})
	c := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: withDB(uri, db), Mode: engine.ModeRO})
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fks := objectNamed(t, cat, "four_of_five").ForeignKeys; len(fks) != 1 || fks[0].RefObject != "orders" {
		t.Errorf("4 of 5 found: %+v", fks)
	}
	if fks := objectNamed(t, cat, "three_of_five").ForeignKeys; len(fks) != 0 {
		t.Errorf("3 of 5 found: %+v", fks)
	}
	if fks := objectNamed(t, cat, "ledger").ForeignKeys; len(fks) != 1 || fks[0].RefObject != "accounts" {
		t.Errorf("int field to long _id: %+v", fks)
	}
}

func TestIndexLabels(t *testing.T) {
	uri := mongoURI(t, mongo8)
	db := loadFixture(t, uri)
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	_, err = client.Database(db).Collection("places").Indexes().CreateMany(context.Background(), []mongo.IndexModel{
		{Keys: bson.D{{Key: "bio", Value: "text"}}},
		{Keys: bson.D{{Key: "loc", Value: "2dsphere"}}},
		{Keys: bson.D{{Key: "k", Value: "hashed"}}},
		{Keys: bson.D{{Key: "created_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(3600)},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: withDB(uri, db), Mode: engine.ModeRO})
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ix := range objectNamed(t, cat, "places").Indexes {
		got = append(got, strings.Join(ix.Columns, ","))
	}
	want := map[string]bool{"_id": true, "bio:text": true, "loc:2dsphere": true, "k:hashed": true, "created_at ttl": true}
	if len(got) != len(want) {
		t.Errorf("indexes: %q", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("index %q not expected; all: %q", g, got)
		}
	}
}

// TestIntrospectCollectionPrivileges connects as a user whose only privilege
// is find on one collection, with no database in the DSN, while another
// database exists.
func TestIntrospectCollectionPrivileges(t *testing.T) {
	uri := mongoURI(t, mongo8)
	a, b := loadFixture(t, uri), loadFixture(t, uri)
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	admin := client.Database("admin")
	role, user := "r_"+a, "u_"+a
	run := func(cmd bson.D) {
		t.Helper()
		if err := admin.RunCommand(context.Background(), cmd).Err(); err != nil {
			t.Fatal(err)
		}
	}
	run(bson.D{{Key: "createRole", Value: role}, {Key: "roles", Value: bson.A{}}, {Key: "privileges", Value: bson.A{
		bson.D{{Key: "resource", Value: bson.D{{Key: "db", Value: a}, {Key: "collection", Value: "orders"}}},
			{Key: "actions", Value: bson.A{"find"}}}}}})
	run(bson.D{{Key: "createUser", Value: user}, {Key: "pwd", Value: "limited-pw-123"},
		{Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: role}, {Key: "db", Value: "admin"}}}}})
	t.Cleanup(func() {
		_ = admin.RunCommand(context.Background(), bson.D{{Key: "dropUser", Value: user}}).Err()
		_ = admin.RunCommand(context.Background(), bson.D{{Key: "dropRole", Value: role}}).Err()
	})
	limited := strings.Replace(uri, "root:"+testPassword+"@", user+":limited-pw-123@", 1)
	c := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: limited, Mode: engine.ModeRO})
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, ns := range cat.Namespaces {
		names = append(names, ns.Name)
		if ns.Name == b {
			t.Errorf("database %s, which the user cannot read, is listed", b)
		}
	}
	if len(cat.Namespaces) != 1 || cat.Namespaces[0].Name != a {
		t.Fatalf("namespaces: %v", names)
	}
	objs := cat.Namespaces[0].Objects
	if len(objs) != 1 || objs[0].Name != "orders" || len(objs[0].Fields) == 0 {
		t.Fatalf("objects: %+v", objs)
	}
}
