package mongodb

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/engine"
)

func fieldMap(fs []engine.Field) map[string]engine.Field {
	m := map[string]engine.Field{}
	for _, f := range fs {
		m[f.Name] = f
	}
	return m
}

func TestInferenceCapsAndUnions(t *testing.T) {
	var docs []bson.Raw
	for i := 0; i < 60; i++ {
		var v any = int32(i)
		if i%3 == 0 {
			v = fmt.Sprint(i)
		}
		d := bson.D{{Key: "_id", Value: int32(i)}, {Key: "v", Value: v}, {Key: "status", Value: []string{"a", "b", "c"}[i%3]},
			{Key: "deep", Value: bson.D{{Key: "b", Value: bson.D{{Key: "c", Value: bson.D{{Key: "d", Value: bson.D{{Key: "e", Value: int32(1)}}}}}}}}},
			{Key: "items", Value: bson.A{bson.D{{Key: "sku", Value: "x"}, {Key: "qty", Value: int32(1)}}}},
			{Key: "tags", Value: bson.A{"t1", "t2"}}}
		if i%2 == 0 {
			d = append(d, bson.E{Key: "sometimes", Value: "s"})
		}
		if i == 0 {
			d = append(d, bson.E{Key: "nul", Value: nil})
		}
		docs = append(docs, rawDoc(t, d))
	}
	got := infer(docs)
	f := fieldMap(got.fields)
	checks := map[string]string{
		"_id": "int", "v": "int|string", "status": "string", "deep": "object", "deep.b": "object", "deep.b.c": "object",
		"deep.b.c.d": "object", "items": "array<object>", "items[].sku": "string", "items[].qty": "int", "tags": "array<string>",
		"sometimes": "string",
	}
	for path, typ := range checks {
		if f[path].Type != typ {
			t.Errorf("%s: type %q, want %q", path, f[path].Type, typ)
		}
		if !f[path].Inferred {
			t.Errorf("%s: not marked inferred", path)
		}
	}
	if _, ok := f["deep.b.c.d.e"]; ok {
		t.Error("descended deeper than 4 levels")
	}
	if f["v"].Nullable || !f["sometimes"].Nullable || !f["nul"].Nullable {
		t.Errorf("nullability: v %v sometimes %v nul %v", f["v"].Nullable, f["sometimes"].Nullable, f["nul"].Nullable)
	}
	if !reflect.DeepEqual(f["status"].Enum, []string{"a", "b", "c", "…"}) {
		t.Errorf("status enum: %v", f["status"].Enum)
	}
	if f["v"].Enum != nil {
		t.Errorf("mixed-type field got values: %v", f["v"].Enum)
	}
	if got.fields[0].Name != "_id" || got.fields[1].Name != "v" {
		t.Errorf("order: %v, %v", got.fields[0].Name, got.fields[1].Name)
	}

	wide := bson.D{{Key: "_id", Value: 1}}
	for i := 0; i < 250; i++ {
		wide = append(wide, bson.E{Key: fmt.Sprintf("k%03d", i), Value: int32(i)})
	}
	w := infer([]bson.Raw{rawDoc(t, wide)})
	if len(w.fields) != 200 || w.comment != "…(+51 fields)" {
		t.Errorf("wide: %d fields, comment %q", len(w.fields), w.comment)
	}
}

func TestInferenceFewDocsHaveNoValues(t *testing.T) {
	var docs []bson.Raw
	for i := 0; i < 10; i++ {
		docs = append(docs, rawDoc(t, bson.D{{Key: "_id", Value: int32(i)}, {Key: "status", Value: "a"}}))
	}
	if e := fieldMap(infer(docs).fields)["status"].Enum; e != nil {
		t.Errorf("enum from 10 documents: %v", e)
	}
}

func TestInferenceSamples(t *testing.T) {
	var docs []bson.Raw
	for i := 0; i < 8; i++ {
		docs = append(docs, rawDoc(t, bson.D{{Key: "_id", Value: int32(i)}, {Key: "user_id", Value: int32(i % 3)}}))
	}
	s := infer(docs).samples["user_id"]
	if len(s) != 3 {
		t.Fatalf("distinct samples: %d", len(s))
	}
	if !strings.Contains(fmt.Sprint(infer(docs).types["user_id"]), "int") {
		t.Errorf("types: %v", infer(docs).types["user_id"])
	}
}

func TestPickTarget(t *testing.T) {
	coll := func(name string, kind engine.Kind) sampled {
		return sampled{obj: &engine.Object{Name: name, Kind: kind}}
	}
	byName := map[string]sampled{}
	for _, s := range []sampled{coll("catalog_item", engine.KindCollection), coll("order_items", engine.KindCollection),
		coll("orders", engine.KindCollection), coll("user", engine.KindView), coll("users", engine.KindCollection),
		coll("a_box", engine.KindCollection), coll("b_box", engine.KindCollection)} {
		byName[s.obj.Name] = s
	}
	for base, want := range map[string]string{"item": "catalog_item", "order": "orders", "user": "users", "box": "", "nope": ""} {
		got, ok := pickTarget(byName, base)
		if ok != (want != "") || ok && got.obj.Name != want {
			t.Errorf("%s: got %v %v, want %q", base, got.obj, ok, want)
		}
	}
}
