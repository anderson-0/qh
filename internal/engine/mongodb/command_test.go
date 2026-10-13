package mongodb

import (
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
		{`db.createCollection()`, `createCollection takes a collection name`, ""},
	} {
		c, err := Parse(tc.q)
		if err == nil {
			_, err = translate(c, "shop", 100)
		}
		e := qerr.From(err)
		if err == nil || e.Category != qerr.Invalid || !strings.Contains(e.Message, tc.msg) || !strings.Contains(e.Hint, tc.hint) {
			t.Errorf("%s: %v (hint %q), want invalid %q hint %q", tc.q, err, e.Hint, tc.msg, tc.hint)
		}
	}
}

func TestClassifyMatchesTranslate(t *testing.T) {
	for _, q := range []string{`db.a.find()`, `db.a.insertOne({})`, `db.a.drop()`, `db.a.aggregate([{$out: "b"}])`} {
		c, _ := Parse(q)
		a, err := classify(c)
		op, _ := translate(c, "x", 10)
		if err != nil || a != op.Access {
			t.Errorf("%s: classify %v %v, translate %v", q, a, err, op.Access)
		}
	}
}
