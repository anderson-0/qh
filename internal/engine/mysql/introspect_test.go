package mysql

import (
	"reflect"
	"testing"
)

func TestAssembleColumnsInOrdinalOrder(t *testing.T) {
	// Catalog rows can arrive interleaved and in any order. Names must not
	// determine the order of columns, including those exposed by views.
	tables := []tableRow{
		{schema: "a", name: "t", kind: "BASE TABLE"},
		{schema: "b", name: "t", kind: "VIEW"},
	}
	columns := []columnRow{
		{schema: "a", table: "t", name: "alpha", ordinal: 3},
		{schema: "b", table: "t", name: "last", ordinal: 2},
		{schema: "a", table: "t", name: "zulu", ordinal: 1},
		{schema: "b", table: "t", name: "first", ordinal: 1},
		{schema: "a", table: "t", name: "middle", ordinal: 2},
	}
	cat := (&conn{}).assemble(tables, columns, nil, nil)
	for i, want := range [][]string{{"zulu", "middle", "alpha"}, {"first", "last"}} {
		var got []string
		for _, f := range cat.Namespaces[i].Objects[0].Fields {
			got = append(got, f.Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("namespace %s columns %v want %v", cat.Namespaces[i].Name, got, want)
		}
	}
}

func TestAssembleIncomingForeignKeyOrder(t *testing.T) {
	tables := []tableRow{
		{schema: "b", name: "child", kind: "BASE TABLE"},
		{schema: "a", name: "zChild", kind: "BASE TABLE"},
		{schema: "a", name: "parent", kind: "BASE TABLE"},
		{schema: "a", name: "Achild", kind: "BASE TABLE"},
	}
	keys := []keyRow{
		{schema: "b", table: "child", constraint: "fk", column: "parent_id", refSchema: "a", refTable: "parent", refColumn: "id"},
		{schema: "a", table: "zChild", constraint: "fk", column: "parent_id", refSchema: "a", refTable: "parent", refColumn: "id"},
		{schema: "a", table: "Achild", constraint: "fk", column: "parent_id", refSchema: "a", refTable: "parent", refColumn: "id"},
		{schema: "a", table: "Achild", constraint: "outside", column: "other_id", refSchema: "outside", refTable: "parent", refColumn: "id"},
	}
	cat := (&conn{}).assemble(tables, nil, keys, nil)
	var got []string
	for _, fk := range cat.Lookup("a.parent")[0].ReferencedBy {
		got = append(got, fk.Namespace+"."+fk.Object)
	}
	if want := []string{"a.Achild", "a.zChild", "b.child"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("incoming references %v want %v", got, want)
	}
	if len(cat.Lookup("a.Achild")[0].ForeignKeys) != 2 {
		t.Fatal("reference to an unlisted table was lost")
	}
}
