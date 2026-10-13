package mongodb

import (
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/qerr"
)

// render prints a call with canonical Extended JSON arguments, so tests see
// exact BSON types.
func render(t *testing.T, c *Call) string {
	t.Helper()
	args := func(a []any) string {
		b, err := bson.MarshalExtJSON(bson.D{{Key: "a", Value: bson.A(a)}}, true, false)
		if err != nil {
			t.Fatalf("marshal %v: %v", a, err)
		}
		s := string(b)
		return strings.TrimSuffix(strings.TrimPrefix(s, `{"a":`), "}")
	}
	s := ""
	if c.DB != "" {
		s += "[" + c.DB + "]"
	}
	if c.Collection != "" {
		s += c.Collection + "."
	}
	s += c.Method + args(c.Args)
	for _, m := range c.Modifiers {
		s += "." + m.Name + args(m.Args)
	}
	return s
}

func TestParse(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`db.orders.find()`, `orders.find[]`},
		{`db.orders.find({status: "paid"})`, `orders.find[{"status":"paid"}]`},
		{`db.orders.find({'status': 'paid', "n": 1,})`, `orders.find[{"status":"paid","n":{"$numberInt":"1"}}]`},
		{`db.orders.find({total: {$gt: 100}}, {_id: 0, total: 1})`,
			`orders.find[{"total":{"$gt":{"$numberInt":"100"}}},{"_id":{"$numberInt":"0"},"total":{"$numberInt":"1"}}]`},
		{`db.orders.find().sort({total: -1}).limit(5).skip(2)`,
			`orders.find[].sort[{"total":{"$numberInt":"-1"}}].limit[{"$numberInt":"5"}].skip[{"$numberInt":"2"}]`},
		{`db.t.find({n: 3000000000, x: 1.5, e: 1e3, neg: -2})`,
			`t.find[{"n":{"$numberLong":"3000000000"},"x":{"$numberDouble":"1.5"},"e":{"$numberDouble":"1000.0"},"neg":{"$numberInt":"-2"}}]`},
		{`db.t.find({a: true, b: false, c: null})`, `t.find[{"a":true,"b":false,"c":null}]`},
		{`db.t.find({name: /^ab/i})`, `t.find[{"name":{"$regularExpression":{"pattern":"^ab","options":"i"}}}]`},
		{`db.t.find({_id: ObjectId("507f1f77bcf86cd799439011")})`, `t.find[{"_id":{"$oid":"507f1f77bcf86cd799439011"}}]`},
		{`db.t.find({at: {$gte: ISODate("2024-01-02T03:04:05Z")}})`,
			`t.find[{"at":{"$gte":{"$date":{"$numberLong":"1704164645000"}}}}]`},
		{`db.t.find({at: new Date("2024-01-02")})`, `t.find[{"at":{"$date":{"$numberLong":"1704153600000"}}}]`},
		{`db.t.find({n: NumberLong("9007199254740993"), i: NumberInt(7), d: NumberDecimal("1.10")})`,
			`t.find[{"n":{"$numberLong":"9007199254740993"},"i":{"$numberInt":"7"},"d":{"$numberDecimal":"1.10"}}]`},
		{`db.t.find({u: UUID("0123456789abcdef0123456789abcdef")})`,
			`t.find[{"u":{"$binary":{"base64":"ASNFZ4mrze8BI0VniavN7w==","subType":"04"}}}]`},
		{`db.t.find({ts: Timestamp(1, 2), lo: MinKey(), hi: MaxKey()})`,
			`t.find[{"ts":{"$timestamp":{"t":1,"i":2}},"lo":{"$minKey":1},"hi":{"$maxKey":1}}]`},
		{`db.t.aggregate([{$match: {a: 1}}, {$group: {_id: "$b", n: {$sum: 1}}}])`,
			`t.aggregate[[{"$match":{"a":{"$numberInt":"1"}}},{"$group":{"_id":"$b","n":{"$sum":{"$numberInt":"1"}}}}]]`},
		{`db.t.find({s: "a\"b\né"})`, `t.find[{"s":"a\"b\né"}]`},
		{"db.t.find( // comment\n {a: /* inline */ 1} );", `t.find[{"a":{"$numberInt":"1"}}]`},
		{`db["odd-name"].find()`, `odd-name.find[]`},
		{`db.getCollection("odd name").countDocuments({})`, `odd name.countDocuments[{}]`},
		{`db.system.views.find()`, `system.views.find[]`},
		{`db.getSiblingDB("other").orders.distinct("status")`, `[other]orders.distinct["status"]`},
		{`db.getCollectionNames()`, `getCollectionNames[]`},
		{`db.createCollection("logs", {capped: true, size: 4096})`,
			`createCollection["logs",{"capped":true,"size":{"$numberInt":"4096"}}]`},
		{`db.orders.find().toArray()`, `orders.find[].toArray[]`},
	} {
		c, err := Parse(tc.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		if got := render(t, c); got != tc.want {
			t.Errorf("Parse(%q)\n got %s\nwant %s", tc.in, got, tc.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{``, `expected "db"`},
		{`orders.find()`, `expected "db"`},
		{`db.orders.find({a: 1}`, `unexpected end of query at 1:22`},
		{`db.orders.find({a: 1}})`, `unexpected "}" at 1:22`},
		{`db.orders.find({a: x})`, `unsupported expression "x"`},
		{`db.orders.find({"a\u0000b": 1})`, `cannot contain a NUL character`},
		{`db.orders.find({a: 1 + 2})`, `unexpected "+" at 1:22`},
		{`db.orders.find({a: new Date()})`, `new Date() needs an argument`},
		{`db.orders.find({a: ObjectId("xyz")})`, `invalid ObjectId`},
		{`db.orders.find({a: /x/g})`, `unsupported regex flag "g"`},
		{`db.orders.find(); db.users.find()`, `only one statement per query`},
		{`db.orders.find({a: function() { return 1 }})`, `unsupported expression "function"`},
		{`db.orders.find({a: ` + "`x`" + `})`, "unexpected \"`\""},
		{`db.orders.find({a: 'unterminated})`, `unterminated string`},
		{"db.orders.find({\n  a: ]\n})", `unexpected "]" at 2:6`},
	} {
		_, err := Parse(tc.in)
		e := qerr.From(err)
		if err == nil || e.Category != qerr.Invalid || !strings.Contains(e.Message, tc.want) {
			t.Errorf("Parse(%q) = %v, want invalid containing %q", tc.in, err, tc.want)
		}
	}
}

func TestParseLimits(t *testing.T) {
	deep := "db.t.find(" + strings.Repeat("[", 101) + strings.Repeat("]", 101) + ")"
	if _, err := Parse(deep); err == nil || !strings.Contains(err.Error(), "nested more than 100 levels") {
		t.Errorf("deep: %v", err)
	}
	long := "db.t.find({s: \"" + strings.Repeat("x", 256<<10) + "\"})"
	if _, err := Parse(long); err == nil || !strings.Contains(err.Error(), "longer than 256 KiB") {
		t.Errorf("long: %v", err)
	}
}

func TestParseErrorShowsCaret(t *testing.T) {
	_, err := Parse(`db.orders.find({a: 1}})`)
	if e := qerr.From(err); !strings.Contains(e.Hint, "db.orders.find({a: 1}})\n                     ^") {
		t.Errorf("hint = %q", e.Hint)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{`db.t.find({a: 1})`, `db.t.aggregate([{$match: {b: /x/i}}])`,
		`db.getSiblingDB("x").t.find().sort({a: -1}).limit(3)`, `db["a"].insertOne({d: ISODate("2024-01-01")})`,
		`db.t.find({u: UUID("0123456789abcdef0123456789abcdef"), n: NumberDecimal("1.5")})`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, q string) {
		c, err := Parse(q)
		if err != nil {
			if qerr.From(err).Category != qerr.Invalid {
				t.Fatalf("Parse(%q): non-invalid error %v", q, err)
			}
			return
		}
		doc := bson.D{{Key: "args", Value: bson.A(c.Args)}}
		for _, m := range c.Modifiers {
			doc = append(doc, bson.E{Key: m.Name, Value: bson.A(m.Args)})
		}
		if _, err := bson.Marshal(doc); err != nil {
			t.Fatalf("Parse(%q) produced unmarshalable BSON: %v", q, err)
		}
	})
}
