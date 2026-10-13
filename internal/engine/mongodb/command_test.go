package mongodb

import (
	"sort"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func relaxed(t *testing.T, d bson.D) string {
	t.Helper()
	b, err := bson.MarshalExtJSON(d, false, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustTranslate(t *testing.T, q string) *Op {
	t.Helper()
	c, err := Parse(q)
	if err != nil {
		t.Fatalf("Parse(%q): %v", q, err)
	}
	op, err := translate(c, "shop", 100)
	if err != nil {
		t.Fatalf("translate(%q): %v", q, err)
	}
	return op
}

func TestTranslate(t *testing.T) {
	for _, tc := range []struct {
		q      string
		access engine.Access
		kind   opKind
		db     string
		want   string
	}{
		{`db.orders.find({status: "paid"}).sort({total: -1}).limit(5)`, engine.Read, kDocs, "shop",
			`{"find":"orders","filter":{"status":"paid"},"sort":{"total":-1},"limit":5,"batchSize":5}`},
		{`db.orders.find()`, engine.Read, kDocs, "shop", `{"find":"orders","filter":{},"limit":101,"batchSize":101}`},
		{`db.orders.find({}, {total: 1}).skip(10).limit(500)`, engine.Read, kDocs, "shop",
			`{"find":"orders","filter":{},"projection":{"total":1},"skip":10,"limit":101,"batchSize":101}`},
		{`db.orders.find().project({a: 1}).hint({a: 1}).collation({locale: "fr"})`, engine.Read, kDocs, "shop",
			`{"find":"orders","filter":{},"projection":{"a":1},"hint":{"a":1},"collation":{"locale":"fr"},"limit":101,"batchSize":101}`},
		{`db.orders.findOne({a: 1})`, engine.Read, kDocs, "shop", `{"find":"orders","filter":{"a":1},"limit":1,"batchSize":1}`},
		{`db.orders.find({a: 1}).count()`, engine.Read, kAggCount, "shop",
			`{"aggregate":"orders","pipeline":[{"$match":{"a":1}},{"$group":{"_id":1,"n":{"$sum":1}}}],"cursor":{}}`},
		{`db.orders.aggregate([{$match: {a: 1}}])`, engine.Read, kDocs, "shop",
			`{"aggregate":"orders","pipeline":[{"$match":{"a":1}},{"$limit":101}],"cursor":{"batchSize":101}}`},
		{`db.orders.aggregate([{$match: {a: 1}}, {$out: "copy"}])`, engine.Write, kDocs, "shop",
			`{"aggregate":"orders","pipeline":[{"$match":{"a":1}},{"$out":"copy"}],"cursor":{}}`},
		{`db.orders.aggregate([{$facet: {x: [{$merge: {into: "y"}}]}}])`, engine.Write, kDocs, "shop",
			`{"aggregate":"orders","pipeline":[{"$facet":{"x":[{"$merge":{"into":"y"}}]}},{"$limit":101}],"cursor":{"batchSize":101}}`},
		{`db.orders.aggregate([{$group: {_id: "$s"}}], {allowDiskUse: true})`, engine.Read, kDocs, "shop",
			`{"aggregate":"orders","pipeline":[{"$group":{"_id":"$s"}},{"$limit":101}],"cursor":{"batchSize":101},"allowDiskUse":true}`},
		{`db.orders.countDocuments({a: 1})`, engine.Read, kAggCount, "shop",
			`{"aggregate":"orders","pipeline":[{"$match":{"a":1}},{"$group":{"_id":1,"n":{"$sum":1}}}],"cursor":{}}`},
		{`db.orders.countDocuments()`, engine.Read, kAggCount, "shop",
			`{"aggregate":"orders","pipeline":[{"$match":{}},{"$group":{"_id":1,"n":{"$sum":1}}}],"cursor":{}}`},
		{`db.orders.estimatedDocumentCount()`, engine.Read, kCount, "shop", `{"count":"orders"}`},
		{`db.orders.distinct("status", {a: 1})`, engine.Read, kDistinct, "shop", `{"distinct":"orders","key":"status","query":{"a":1}}`},
		{`db.orders.getIndexes()`, engine.Read, kDocs, "shop", `{"listIndexes":"orders"}`},
		{`db.getCollectionNames()`, engine.Read, kNames, "shop", `{"listCollections":1,"nameOnly":true,"authorizedCollections":true}`},
		{`db.orders.insertOne({_id: 1, a: 2})`, engine.Write, kInsert, "shop", `{"insert":"orders","documents":[{"_id":1,"a":2}]}`},
		{`db.orders.insertMany([{_id: 1}, {_id: 2}], {ordered: false})`, engine.Write, kInsert, "shop",
			`{"insert":"orders","documents":[{"_id":1},{"_id":2}],"ordered":false}`},
		{`db.orders.updateMany({a: 1}, {$set: {b: 2}})`, engine.Write, kUpdate, "shop",
			`{"update":"orders","updates":[{"q":{"a":1},"u":{"$set":{"b":2}},"multi":true}]}`},
		{`db.orders.updateOne({a: 1}, [{$set: {b: 2}}], {upsert: true})`, engine.Write, kUpdate, "shop",
			`{"update":"orders","updates":[{"q":{"a":1},"u":[{"$set":{"b":2}}],"multi":false,"upsert":true}]}`},
		{`db.orders.replaceOne({_id: 1}, {a: 3})`, engine.Write, kUpdate, "shop",
			`{"update":"orders","updates":[{"q":{"_id":1},"u":{"a":3},"multi":false}]}`},
		{`db.orders.deleteOne({a: 1})`, engine.Write, kDelete, "shop", `{"delete":"orders","deletes":[{"q":{"a":1},"limit":1}]}`},
		{`db.orders.deleteMany({})`, engine.Write, kDelete, "shop", `{"delete":"orders","deletes":[{"q":{},"limit":0}]}`},
		{`db.orders.findOneAndUpdate({a: 1}, {$set: {b: 2}}, {returnDocument: "after", sort: {a: 1}})`, engine.Write, kFindAndModify, "shop",
			`{"findAndModify":"orders","query":{"a":1},"update":{"$set":{"b":2}},"new":true,"sort":{"a":1}}`},
		{`db.orders.findOneAndDelete({a: 1})`, engine.Write, kFindAndModify, "shop",
			`{"findAndModify":"orders","query":{"a":1},"remove":true}`},
		{`db.users.createIndex({email: 1, n: -1}, {unique: true})`, engine.DDL, kAdmin, "shop",
			`{"createIndexes":"users","indexes":[{"key":{"email":1,"n":-1},"name":"email_1_n_-1","unique":true}]}`},
		{`db.users.createIndex({bio: "text"}, {name: "bio_text_idx"})`, engine.DDL, kAdmin, "shop",
			`{"createIndexes":"users","indexes":[{"key":{"bio":"text"},"name":"bio_text_idx"}]}`},
		{`db.users.dropIndex("email_1")`, engine.DDL, kAdmin, "shop", `{"dropIndexes":"users","index":"email_1"}`},
		{`db.users.dropIndexes()`, engine.DDL, kAdmin, "shop", `{"dropIndexes":"users","index":"*"}`},
		{`db.users.drop()`, engine.DDL, kAdmin, "shop", `{"drop":"users"}`},
		{`db.users.renameCollection("people")`, engine.DDL, kAdmin, "admin", `{"renameCollection":"shop.users","to":"shop.people"}`},
		{`db.createCollection("logs", {capped: true, size: 4096})`, engine.DDL, kAdmin, "shop", `{"create":"logs","capped":true,"size":4096}`},
	} {
		op := mustTranslate(t, tc.q)
		if op.Access != tc.access || op.Kind != tc.kind || op.DB != tc.db {
			t.Errorf("%s: access %v kind %v db %q, want %v %v %q", tc.q, op.Access, op.Kind, op.DB, tc.access, tc.kind, tc.db)
		}
		if got := relaxed(t, op.Command); got != tc.want {
			t.Errorf("%s\n got %s\nwant %s", tc.q, got, tc.want)
		}
	}
}

func TestInsertAddsIDs(t *testing.T) {
	op := mustTranslate(t, `db.orders.insertOne({a: 1})`)
	docs := op.Command[1].Value.(bson.A)
	first := docs[0].(bson.D)[0]
	if first.Key != "_id" || len(op.IDs) != 1 {
		t.Fatalf("command %v, ids %v", op.Command, op.IDs)
	}
	if _, ok := first.Value.(bson.ObjectID); !ok || first.Value != op.IDs[0] {
		t.Fatalf("_id = %#v, ids %v", first.Value, op.IDs)
	}
}

func TestExplainCommand(t *testing.T) {
	op := mustTranslate(t, `db.orders.find({a: 1}).explain()`)
	if !op.Explain {
		t.Fatal("explain modifier not recorded")
	}
	want := `{"explain":{"find":"orders","filter":{"a":1},"limit":101,"batchSize":101},"verbosity":"queryPlanner"}`
	if got := relaxed(t, op.explainCommand()); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestTranslateErrors(t *testing.T) {
	for _, tc := range []struct{ q, msg, hint string }{
		{`db.orders.findd({})`, `unsupported method "findd"`, "did you mean find?"},
		{`db.orders.find().sortt({a: 1})`, `unsupported cursor method "sortt"`, "did you mean sort?"},
		{`db.a.getSiblingDB("x")`, `unsupported method "getSiblingDB"`, ""},
		{`db.orders.mapReduce()`, `unsupported method "mapReduce"`, ""},
		{`db.orders.insertOne()`, `insertOne takes a document`, ""},
		{`db.orders.find({}, {}, {})`, `find takes at most 2 arguments`, ""},
		{`db.orders.find("x")`, `find takes a filter document`, ""},
		{`db.orders.aggregate({$match: {}})`, `aggregate takes an array of stages`, ""},
		{`db.orders.insertOne({a: 1}).explain()`, `explain is supported for reads only`, ""},
		{`db.orders.countDocuments().sort({a: 1})`, `countDocuments does not take .sort()`, ""},
		{`db.orders.replaceOne({}, {$set: {a: 1}})`, `replaceOne takes a replacement document without $ operators`, ""},
		{`db.orders.updateOne({}, {a: 1})`, `updateOne takes an update with $ operators or a pipeline`, ""},
		{`db.orders.find().limit("x")`, `limit takes a number`, ""},
		{`db.a.getCollectionNames()`, `getCollectionNames is called on db, as in db.getCollectionNames()`, ""},
		{`db.a.createCollection("b")`, `createCollection is called on db, as in db.createCollection("name")`, ""},
		{`db.a.getIndexes({})`, `getIndexes takes no arguments`, ""},
		{`db.a.drop(1)`, `drop takes no arguments`, ""},
		{`db.a.find().skip(-1)`, `skip takes a non-negative number`, ""},
		{`db.a.createIndexes([{a: 1}, {b: 1}], {name: "x"})`, `name applies to one index; use createIndex`, ""},
		{`db.createCollection()`, `createCollection takes a collection name`, ""},
	} {
		c, err := Parse(tc.q)
		if err == nil {
			_, err = translate(c, "shop", 100)
		}
		if err == nil {
			t.Errorf("%s: no error, want invalid %q", tc.q, tc.msg)
			continue
		}
		e := qerr.From(err)
		if e.Category != qerr.Invalid || !strings.Contains(e.Message, tc.msg) || !strings.Contains(e.Hint, tc.hint) {
			t.Errorf("%s: %v (hint %q), want invalid %q hint %q", tc.q, err, e.Hint, tc.msg, tc.hint)
		}
	}
}

func TestClassifyMatchesTranslate(t *testing.T) {
	for _, q := range []string{`db.a.find()`, `db.a.insertOne({})`, `db.a.drop()`, `db.a.aggregate([{$out: "b"}])`} {
		c, err := Parse(q)
		if err != nil {
			t.Fatalf("Parse(%q): %v", q, err)
		}
		a, err := classify(c)
		if err != nil {
			t.Fatalf("classify(%q): %v", q, err)
		}
		op, err := translate(c, "x", 10)
		if err != nil {
			t.Fatalf("translate(%q): %v", q, err)
		}
		if a != op.Access {
			t.Errorf("%s: classify %v, translate %v", q, a, op.Access)
		}
	}
}

func TestReadOnlyBoundary(t *testing.T) {
	for _, tc := range []struct {
		q      string
		access engine.Access
	}{
		{`db.a.aggregate([{$lookup: {from: "b", pipeline: [{$merge: {into: "c"}}], as: "x"}}])`, engine.Write},
		{`db.a.aggregate([{$unionWith: {coll: "b", pipeline: [{$out: "c"}]}}])`, engine.Write},
		{`db.a.aggregate([{$facet: {"$out": [{$match: {}}]}}])`, engine.Write},
		{`db.a.aggregate([{$limit: 1, $out: "z"}])`, engine.Write},
		{`db.a.aggregate([{$out: "z"}, {$match: {}}])`, engine.Write},
		{`db.a.aggregate([{$lookup: {from: "b", localField: "x", foreignField: "y", as: "z"}}])`, engine.Read},
		{`db.a.aggregate([{$unionWith: {coll: "b", pipeline: [{$match: {}}]}}])`, engine.Read},
		{`db.a.aggregate([{$facet: {x: [{$match: {}}]}}])`, engine.Read},
	} {
		c, err := Parse(tc.q)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.q, err)
		}
		a, err := classify(c)
		if err != nil || a != tc.access {
			t.Errorf("%s: classify %v %v, want %v", tc.q, a, err, tc.access)
		}
		op, err := translate(c, "x", 10)
		if err != nil || op.Access != tc.access {
			t.Errorf("%s: translate %v, want %v", tc.q, err, tc.access)
		}
	}

	// explain is for reads only, and cursor methods are limited per method.
	for _, q := range []string{
		`db.a.insertOne({}).explain()`, `db.a.insertMany([{}]).explain()`,
		`db.a.updateOne({}, {$set: {a: 1}}).explain()`, `db.a.updateMany({}, {$set: {a: 1}}).explain()`,
		`db.a.replaceOne({}, {a: 1}).explain()`, `db.a.deleteOne({}).explain()`, `db.a.deleteMany({}).explain()`,
		`db.a.findOneAndUpdate({}, {$set: {a: 1}}).explain()`, `db.a.findOneAndReplace({}, {a: 1}).explain()`,
		`db.a.findOneAndDelete({}).explain()`, `db.a.createIndex({a: 1}).explain()`,
		`db.a.createIndexes([{a: 1}]).explain()`, `db.a.dropIndex("a_1").explain()`,
		`db.a.dropIndexes().explain()`, `db.a.drop().explain()`, `db.a.renameCollection("b").explain()`,
		`db.createCollection("b").explain()`,
		`db.a.findOne().limit(2)`, `db.a.findOne().skip(1)`,
		`db.a.getIndexes({})`, `db.a.distinct(1)`, `db.a.distinct("")`,
	} {
		c, err := Parse(q)
		if err != nil {
			t.Fatalf("Parse(%q): %v", q, err)
		}
		if _, err := translate(c, "x", 10); err == nil || qerr.From(err).Category != qerr.Invalid {
			t.Errorf("%s: err = %v, want invalid", q, err)
		}
	}
}

func TestExplainVerbosityIsFixed(t *testing.T) {
	op := mustTranslate(t, `db.a.find().explain("executionStats")`)
	if got := relaxed(t, op.explainCommand()); !strings.Contains(got, `"verbosity":"queryPlanner"`) || strings.Contains(got, "executionStats") {
		t.Fatalf("got %s", got)
	}
}

func TestAggregateOptionsAreAllowlisted(t *testing.T) {
	op := mustTranslate(t, `db.a.aggregate([{$match: {}}], {allowDiskUse: true, writeConcern: {w: 0}, readConcern: {level: "linearizable"}, bypassDocumentValidation: true, explain: true, cursor: {batchSize: 5}, maxTimeMS: 1, comment: "c"})`)
	want := `{"aggregate":"a","pipeline":[{"$match":{}},{"$limit":101}],"cursor":{"batchSize":101},"allowDiskUse":true,"comment":"c"}`
	if got := relaxed(t, op.Command); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestReadMethodsAreAllowlisted(t *testing.T) {
	want := []string{"aggregate", "countDocuments", "distinct", "estimatedDocumentCount", "find", "findOne", "getCollectionNames", "getIndexes"}
	var got []string
	for name, m := range methods {
		if m.access == engine.Read {
			got = append(got, name)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Read methods are %v, want %v; review the read-only boundary before changing this list", got, want)
	}
}

func TestExplainOfAggregateDropsCursor(t *testing.T) {
	op := mustTranslate(t, `db.a.aggregate([{$match: {}}]).explain()`)
	want := `{"explain":{"aggregate":"a","pipeline":[{"$match":{}},{"$limit":101}],"cursor":{}},"verbosity":"queryPlanner"}`
	if got := relaxed(t, op.explainCommand()); got != want {
		t.Fatalf("got %s", got)
	}
	if got := relaxed(t, op.Command); !strings.Contains(got, `"cursor":{"batchSize":101}`) {
		t.Fatalf("op.Command was mutated: %s", got)
	}
}

func TestTranslateOptionHandling(t *testing.T) {
	for _, tc := range []struct{ q, want string }{
		{`db.createCollection("logs", {create: "other", capped: true})`, `{"create":"logs","capped":true}`},
		{`db.a.createIndex({x: 1}, {key: {y: 1}, unique: true})`,
			`{"createIndexes":"a","indexes":[{"key":{"x":1},"name":"x_1","unique":true}]}`},
		{`db.a.createIndexes([{x: 1}], {name: "mine", key: {y: 1}})`,
			`{"createIndexes":"a","indexes":[{"key":{"x":1},"name":"mine"}]}`},
	} {
		if got := relaxed(t, mustTranslate(t, tc.q).Command); got != tc.want {
			t.Errorf("%s\n got %s\nwant %s", tc.q, got, tc.want)
		}
	}
}
