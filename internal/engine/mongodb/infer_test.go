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

func TestInferenceArrayTypes(t *testing.T) {
	docs := []bson.Raw{
		rawDoc(t, bson.D{{Key: "a", Value: bson.A{"x"}}, {Key: "e", Value: bson.A{}}, {Key: "m", Value: int32(1)}}),
		rawDoc(t, bson.D{{Key: "a", Value: bson.A{int32(1)}}, {Key: "e", Value: bson.A{}}, {Key: "m", Value: int32(2)}}),
		rawDoc(t, bson.D{{Key: "a", Value: bson.A{}}, {Key: "e", Value: bson.A{nil}}, {Key: "m", Value: bson.A{"s", "t"}}}),
	}
	f := fieldMap(infer(docs).fields)
	for path, want := range map[string]string{"a": "array<int|string>", "e": "array", "m": "int|array<string>"} {
		if f[path].Type != want {
			t.Errorf("%s: type %q, want %q", path, f[path].Type, want)
		}
	}
}

func TestInferencePresenceAndNesting(t *testing.T) {
	var docs []bson.Raw
	for i := 0; i < 20; i++ {
		items := bson.A{bson.D{{Key: "k", Value: "a"}}, bson.D{{Key: "k", Value: "b"}}, bson.D{{Key: "k", Value: "a"}}}
		d := bson.D{{Key: "_id", Value: int32(i)}, {Key: "items", Value: items}}
		if i%2 == 0 {
			d = append(d, bson.E{Key: "addr", Value: bson.D{{Key: "city", Value: "x"}}})
		}
		docs = append(docs, rawDoc(t, d))
	}
	f := fieldMap(infer(docs).fields)
	// 60 values in 20 documents: presence counts documents, so no values
	// (under 50) and not nullable.
	if k := f["items[].k"]; k.Enum != nil || k.Nullable {
		t.Errorf("items[].k: enum %v nullable %v", k.Enum, k.Nullable)
	}
	if !f["addr.city"].Nullable {
		t.Error("addr.city under a parent missing from half the documents is not nullable")
	}
}

func TestInferenceEnumLimits(t *testing.T) {
	long := strings.Repeat("x", 41)
	var docs []bson.Raw
	for i := 0; i < 60; i++ {
		ok := []string{"a", "b"}[i%2]
		if i == 7 {
			ok = long[:40]
		}
		docs = append(docs, rawDoc(t, bson.D{{Key: "nine", Value: fmt.Sprint("v", i%9)}, {Key: "long", Value: []string{"a", long}[i%2]},
			{Key: "ok", Value: ok}}))
	}
	f := fieldMap(infer(docs).fields)
	if f["nine"].Enum != nil {
		t.Errorf("9 distinct values gave %v", f["nine"].Enum)
	}
	if f["long"].Enum != nil {
		t.Errorf("a value over 40 characters gave %v", f["long"].Enum)
	}
	if len(f["ok"].Enum) != 4 {
		t.Errorf("40-character value: %v", f["ok"].Enum)
	}
}

func TestSameIDType(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"int", "long", true}, {"double", "decimal", true}, {"int", "string", false}, {"objectId", "objectId", true},
		{"string", "objectId", false}, {"", "", false}} {
		if got := sameIDType(c.a, c.b); got != c.want {
			t.Errorf("sameIDType(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestRefKeyComparesNumbersByValue(t *testing.T) {
	val := func(v any) bson.RawValue { return rawDoc(t, bson.D{{Key: "v", Value: v}}).Lookup("v") }
	one := refKey(val(int32(1)))
	if refKey(val(int64(1))) != one || refKey(val(1.0)) != one {
		t.Error("1 as int, long and double differ")
	}
	if refKey(val("1")) == one || refKey(val(1.5)) == one {
		t.Error("\"1\" or 1.5 equals 1")
	}
}

func TestEnoughFound(t *testing.T) {
	for _, c := range []struct {
		found, total int
		want         bool
	}{{3, 5, false}, {4, 5, true}, {5, 5, true}, {2, 2, true}, {1, 2, false}, {0, 0, false}} {
		if got := enoughFound(c.found, c.total); got != c.want {
			t.Errorf("enoughFound(%d, %d) = %v", c.found, c.total, got)
		}
	}
}

func TestCollectNamespacesSkipsFailedDatabase(t *testing.T) {
	build := func(db string) (engine.Namespace, error) {
		if db == "b" {
			return engine.Namespace{}, fmt.Errorf("unauthorized")
		}
		return engine.Namespace{Name: db, Objects: []*engine.Object{{Name: "x"}}}, nil
	}
	got, err := collectNamespaces([]string{"a", "b", "c"}, build)
	if err != nil || len(got) != 3 || got[1].Name != "b" || len(got[1].Objects) != 0 || len(got[2].Objects) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := collectNamespaces([]string{"b"}, build); err == nil {
		t.Error("every database failed but no error")
	}
}
