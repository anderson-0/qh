package mongodb

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/engine"
)

// valueOf renders one BSON value as a result cell.
func valueOf(v bson.RawValue) engine.Value {
	switch v.Type {
	case bson.TypeNull, bson.TypeUndefined:
		return engine.Value{Null: true}
	case bson.TypeString:
		return engine.Value{Text: v.StringValue()}
	case bson.TypeSymbol:
		return engine.Value{Text: v.Symbol()}
	case bson.TypeInt32:
		return engine.Value{Text: strconv.FormatInt(int64(v.Int32()), 10)}
	case bson.TypeInt64:
		return engine.Value{Text: strconv.FormatInt(v.Int64(), 10)}
	case bson.TypeDouble:
		return engine.Value{Text: formatDouble(v.Double())}
	case bson.TypeDecimal128:
		return engine.Value{Text: v.Decimal128().String()}
	case bson.TypeBoolean:
		return engine.Value{Text: strconv.FormatBool(v.Boolean())}
	case bson.TypeDateTime:
		return engine.Value{Text: time.UnixMilli(v.DateTime()).UTC().Format("2006-01-02T15:04:05.000Z")}
	case bson.TypeObjectID:
		id := v.ObjectID()
		return engine.Value{Text: hex.EncodeToString(id[:])}
	case bson.TypeBinary:
		sub, data := v.Binary()
		if sub == 4 && len(data) == 16 {
			h := hex.EncodeToString(data)
			return engine.Value{Text: h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]}
		}
		return engine.Value{Binary: true, Size: len(data)}
	case bson.TypeRegex:
		p, o := v.Regex()
		return engine.Value{Text: "/" + p + "/" + o}
	case bson.TypeTimestamp:
		t, i := v.Timestamp()
		return engine.Value{Text: fmt.Sprintf("Timestamp(%d, %d)", t, i)}
	case bson.TypeMinKey:
		return engine.Value{Text: "MinKey"}
	case bson.TypeMaxKey:
		return engine.Value{Text: "MaxKey"}
	}
	return engine.Value{Text: relaxedJSON(v)}
}

// formatDouble prints f in plain decimal when that is short (zero, or a
// magnitude from 1e-6 up to 1e21), else in exponent form. NaN and the
// infinities print as Go prints them.
func formatDouble(f float64) string {
	if a := math.Abs(f); f == 0 || a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// relaxedJSON renders v as compact relaxed Extended JSON, the form nested
// documents and arrays take in result cells.
func relaxedJSON(v bson.RawValue) string {
	b, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: v}}, false, false)
	if err != nil {
		return v.String()
	}
	s := strings.TrimPrefix(string(b), `{"v":`)
	return strings.TrimSuffix(s, "}")
}

// flatten turns documents into columns and rows: _id first, then every
// top-level key in the order it first appears. A key a document lacks is NULL.
func flatten(docs []bson.Raw) ([]engine.Column, [][]engine.Value) {
	index := map[string]int{}
	var names []string
	add := func(k string) {
		if _, ok := index[k]; !ok {
			index[k] = len(names)
			names = append(names, k)
		}
	}
	for _, d := range docs {
		if _, err := d.LookupErr("_id"); err == nil {
			add("_id")
			break
		}
	}
	for _, d := range docs {
		elems, _ := d.Elements()
		for _, e := range elems {
			add(e.Key())
		}
	}
	cols := make([]engine.Column, len(names))
	for i, n := range names {
		cols[i] = engine.Column{Name: n}
	}
	rows := make([][]engine.Value, len(docs))
	for r, d := range docs {
		row := make([]engine.Value, len(names))
		for i := range row {
			row[i] = engine.Value{Null: true}
		}
		elems, _ := d.Elements()
		for _, e := range elems {
			row[index[e.Key()]] = valueOf(e.Value())
		}
		rows[r] = row
	}
	return cols, rows
}
