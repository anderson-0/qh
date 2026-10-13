package mongodb

import (
	"math"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/engine"
)

func rawDoc(t *testing.T, d bson.D) bson.Raw {
	t.Helper()
	b, err := bson.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestValueOf(t *testing.T) {
	oid, _ := bson.ObjectIDFromHex("507f1f77bcf86cd799439011")
	dec, _ := bson.ParseDecimal128("1.10")
	at := time.Date(2024, 1, 2, 3, 4, 5, 6e6, time.UTC)
	doc := rawDoc(t, bson.D{
		{Key: "s", Value: "x"}, {Key: "i", Value: int32(7)}, {Key: "l", Value: int64(9007199254740993)},
		{Key: "f", Value: 1.5}, {Key: "whole", Value: 375.0}, {Key: "dec", Value: dec}, {Key: "b", Value: true},
		{Key: "at", Value: bson.NewDateTimeFromTime(at)}, {Key: "oid", Value: oid}, {Key: "nul", Value: nil},
		{Key: "obj", Value: bson.D{{Key: "city", Value: "Lyon"}, {Key: "n", Value: int32(1)}}},
		{Key: "arr", Value: bson.A{"a", int32(2), bson.D{{Key: "x", Value: true}}}},
		{Key: "bin", Value: bson.Binary{Data: []byte{1, 2, 3}}},
		{Key: "uuid", Value: bson.Binary{Subtype: 4, Data: []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}}},
		{Key: "uuid3", Value: bson.Binary{Subtype: 3, Data: []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}}},
		{Key: "re", Value: bson.Regex{Pattern: "^a", Options: "i"}},
		{Key: "ts", Value: bson.Timestamp{T: 1, I: 2}},
	})
	want := map[string]engine.Value{
		"s": {Text: "x"}, "i": {Text: "7"}, "l": {Text: "9007199254740993"}, "f": {Text: "1.5"}, "whole": {Text: "375"},
		"dec": {Text: "1.10"}, "b": {Text: "true"}, "at": {Text: "2024-01-02T03:04:05.006Z"},
		"oid": {Text: "507f1f77bcf86cd799439011"}, "nul": {Null: true},
		"obj": {Text: `{"city":"Lyon","n":1}`}, "arr": {Text: `["a",2,{"x":true}]`},
		"bin": {Binary: true, Size: 3}, "uuid": {Text: "01234567-89ab-cdef-0123-456789abcdef"},
		"uuid3": {Binary: true, Size: 16},
		"re":    {Text: "/^a/i"}, "ts": {Text: "Timestamp(1, 2)"},
	}
	elems, _ := doc.Elements()
	for _, e := range elems {
		if got := valueOf(e.Value()); got != want[e.Key()] {
			t.Errorf("%s: got %+v, want %+v", e.Key(), got, want[e.Key()])
		}
	}
}

func TestFlattenColumnOrder(t *testing.T) {
	docs := []bson.Raw{
		rawDoc(t, bson.D{{Key: "b", Value: 1}, {Key: "_id", Value: 1}}),
		rawDoc(t, bson.D{{Key: "_id", Value: 2}, {Key: "c", Value: 3}, {Key: "b", Value: 2}}),
	}
	cols, rows := flatten(docs)
	if len(cols) != 3 || cols[0].Name != "_id" || cols[1].Name != "b" || cols[2].Name != "c" {
		t.Fatalf("cols = %+v", cols)
	}
	if !rows[0][2].Null || rows[1][2].Text != "3" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestValueOfDoubles(t *testing.T) {
	for _, tc := range []struct {
		f    float64
		want string
	}{
		{0, "0"}, {1.5, "1.5"}, {375, "375"}, {-2.25, "-2.25"},
		{1e-6, "0.000001"}, {123456789012345680000, "123456789012345680000"}, // plain below 1e21
		{1e-7, "1e-07"}, {1e21, "1e+21"}, {-1.5e30, "-1.5e+30"}, {5e-324, "5e-324"}, // exponent form outside it
		{math.NaN(), "NaN"}, {math.Inf(1), "+Inf"}, {math.Inf(-1), "-Inf"},
	} {
		raw := rawDoc(t, bson.D{{Key: "f", Value: tc.f}})
		if got := valueOf(raw.Lookup("f")); got != (engine.Value{Text: tc.want}) {
			t.Errorf("%v: got %+v, want %q", tc.f, got, tc.want)
		}
	}
}

func TestValueOfDatesAndDecimals(t *testing.T) {
	big, err := bson.ParseDecimal128("1.23456789012345678901234567890123E+6000")
	if err != nil {
		t.Fatal(err)
	}
	doc := rawDoc(t, bson.D{
		{Key: "before1970", Value: bson.DateTime(-1500)},
		{Key: "after9999", Value: bson.NewDateTimeFromTime(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))},
		{Key: "big", Value: big},
		{Key: "nested", Value: bson.D{{Key: "d", Value: bson.NewDateTimeFromTime(time.Date(2024, 1, 2, 3, 4, 5, 6e6, time.UTC))}}},
		{Key: "list", Value: bson.A{bson.NewDateTimeFromTime(time.Date(1969, 12, 31, 23, 59, 58, 0, time.UTC))}},
		{Key: "nan", Value: bson.D{{Key: "x", Value: math.NaN()}}},
	})
	want := map[string]string{
		"before1970": "1969-12-31T23:59:58.500Z",
		"after9999":  "10000-01-01T00:00:00.000Z",
		"big":        "1.23456789012345678901234567890123E+6000",
		"nested":     `{"d":{"$date":"2024-01-02T03:04:05.006Z"}}`,
		"list":       `[{"$date":{"$numberLong":"-2000"}}]`,
		"nan":        `{"x":{"$numberDouble":"NaN"}}`,
	}
	elems, _ := doc.Elements()
	for _, e := range elems {
		if got := valueOf(e.Value()); got != (engine.Value{Text: want[e.Key()]}) {
			t.Errorf("%s: got %+v, want %q", e.Key(), got, want[e.Key()])
		}
	}
}
