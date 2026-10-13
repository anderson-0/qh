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
