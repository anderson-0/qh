package mongodb

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/format"
	"github.com/anderson-0/qh/internal/qerr"
)

var durationRE = regexp.MustCompile(` · [^\n]*`)

func runQ(t *testing.T, c engine.Conn, q string, o engine.QueryOpts) string {
	t.Helper()
	r, err := c.Query(context.Background(), q, o)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return strings.TrimSpace(durationRE.ReplaceAllString(format.Result(r), ""))
}

func expectOut(t *testing.T, q, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s\n got:\n%s\nwant:\n%s", q, got, want)
	}
}

var roOpts = engine.QueryOpts{MaxRows: 100, ReadOnly: true, Timeout: 10 * time.Second}

func TestQueryReads(t *testing.T) {
	for _, image := range []string{"mongo:7", mongo8} {
		t.Run(image, func(t *testing.T) {
			ro := openSpec(t, fixtureSpec(t, image, engine.ModeRO))
			for _, tc := range []struct{ q, want string }{
				{`db.users.find().sort({_id: 1})`,
					"_id\temail\tname\tbio\tavatar\tcreated_at\n" +
						"1\talice@example.com\tAlice\tline1\\nline2\\tx\t<bytes 4B>\t2025-03-14T10:22:31.000Z\n" +
						"2\tzoe@example.com\tZoë 🚀\tNULL\tNULL\tNULL\n" +
						"3\tnobody@example.com\tNULL\tNULL\tNULL\tNULL\n-- 3 rows"},
				{`db.users.find({_id: {$lte: 2}}, {email: 1, _id: 0}).sort({email: 1})`,
					"email\nalice@example.com\nzoe@example.com\n-- 2 rows"},
				{`db.users.findOne({email: "zoe@example.com"}, {name: 1})`, "_id\tname\n2\tZoë 🚀\n-- 1 row"},
				{`db.orders.countDocuments({status: "paid"})`, "count\n125\n-- 1 row"},
				{`db.orders.find({status: "paid"}).count()`, "count\n125\n-- 1 row"},
				{`db.orders.estimatedDocumentCount()`, "count\n250\n-- 1 row"},
				{`db.orders.aggregate([{$group: {_id: "$status", n: {$sum: 1}}}, {$sort: {_id: 1}}])`,
					"_id\tn\npaid\t125\npending\t125\n-- 2 rows"},
				{`db.orders.find({status: "shipped"})`, "_id\n-- 0 rows"},
				{`db.orders.countDocuments({status: "shipped"})`, "count\n0\n-- 1 row"},
				{`db.orders.find({total: {$gt: 370}}).sort({total: -1}).limit(2)`,
					"_id\tuser_id\tstatus\ttotal\n250\t2\tpaid\t375\n249\t1\tpending\t373.5\n-- 2 rows"},
			} {
				expectOut(t, tc.q, runQ(t, ro, tc.q, roOpts), tc.want)
			}
			got := runQ(t, ro, `db.orders.distinct("status")`, roOpts)
			lines := strings.Split(got, "\n")
			sort.Strings(lines[1:3])
			if strings.Join(lines, "\n") != "status\npaid\npending\n-- 2 rows" {
				t.Errorf("distinct: %q", got)
			}
			names := runQ(t, ro, `db.getCollectionNames()`, roOpts)
			for _, n := range []string{"users", "orders", "order_items", "paid_orders"} {
				if !strings.Contains(names, "\n"+n+"\n") {
					t.Errorf("getCollectionNames lacks %s: %q", n, names)
				}
			}
			if strings.Contains(names, "system.") {
				t.Errorf("getCollectionNames lists system collections: %q", names)
			}
		})
	}
}

func TestTruncation(t *testing.T) {
	ro := openSpec(t, fixtureSpec(t, mongo8, engine.ModeRO))
	o := roOpts
	o.MaxRows = 3
	expectOut(t, "find", runQ(t, ro, `db.orders.find({}, {_id: 1}).sort({_id: 1})`, o), "_id\n1\n2\n3\n-- 3 rows shown, more exist (truncated)")
	expectOut(t, "limit", runQ(t, ro, `db.orders.find({}, {_id: 1}).sort({_id: 1}).limit(3)`, o), "_id\n1\n2\n3\n-- 3 rows")
	expectOut(t, "aggregate", runQ(t, ro, `db.orders.aggregate([{$sort: {_id: 1}}, {$project: {_id: 1}}])`, o), "_id\n1\n2\n3\n-- 3 rows shown, more exist (truncated)")
	o.MaxRows = 1
	expectOut(t, "distinct", strings.Split(runQ(t, ro, `db.orders.distinct("status")`, o), "\n")[2], "-- 1 row shown, more exist (truncated)")
}

func TestReadOnlyRefusesWrites(t *testing.T) {
	spec := fixtureSpec(t, mongo8, engine.ModeRO)
	ro := openSpec(t, spec)
	for _, q := range []string{
		`db.users.insertOne({_id: 99, email: "x@example.com"})`,
		`db.users.updateMany({}, {$set: {name: "x"}})`,
		`db.order_items.deleteMany({})`,
		`db.order_items.drop()`,
		`db.users.createIndex({name: 1})`,
		`db.orders.aggregate([{$match: {}}, {$out: "evil"}])`,
		`db.orders.aggregate([{$facet: {a: [{$merge: {into: "evil"}}]}}])`,
		`db.orders.findOneAndDelete({_id: 1})`,
		`db.orders.renameCollection("evil")`,
		`db.createCollection("evil")`,
	} {
		_, err := ro.Query(context.Background(), q, roOpts)
		if e := qerr.From(err); err == nil || e.Category != qerr.ReadOnly {
			t.Errorf("%s on read-only: %v", q, err)
		}
	}
	rw := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: spec.DSN, Mode: engine.ModeRW})
	expectOut(t, "counts", runQ(t, rw, `db.order_items.countDocuments({})`, roOpts), "count\n20\n-- 1 row")
	if got := runQ(t, rw, `db.getCollectionNames()`, roOpts); strings.Contains(got, "evil") {
		t.Errorf("evil collection exists: %q", got)
	}
}

func TestWritesAndAdmin(t *testing.T) {
	rw := openSpec(t, fixtureSpec(t, mongo8, engine.ModeRW))
	w := engine.QueryOpts{MaxRows: 100, Timeout: 10 * time.Second}
	expectOut(t, "insertOne", runQ(t, rw, `db.users.insertOne({_id: 99, email: "new@example.com", address: {city: "Lyon"}, tags: ["a", "b"], at: ISODate("2024-01-02T00:00:00Z"), dec: NumberDecimal("1.10"), big: NumberLong("9007199254740993")})`, w),
		"_id\n99\n-- 1 row")
	expectOut(t, "read back", runQ(t, rw, `db.users.find({_id: 99}, {address: 1, tags: 1, at: 1, dec: 1, big: 1})`, w),
		"_id\taddress\ttags\tat\tdec\tbig\n99\t{\"city\":\"Lyon\"}\t[\"a\",\"b\"]\t2024-01-02T00:00:00.000Z\t1.10\t9007199254740993\n-- 1 row")
	got := runQ(t, rw, `db.users.insertOne({email: "auto@example.com"})`, w)
	if !regexp.MustCompile(`^_id\n[0-9a-f]{24}\n-- 1 row$`).MatchString(got) {
		t.Errorf("generated _id: %q", got)
	}
	expectOut(t, "updateMany", runQ(t, rw, `db.orders.updateMany({status: "pending"}, {$set: {flag: true}})`, w), "-- 125 rows affected")
	expectOut(t, "deleteOne", runQ(t, rw, `db.order_items.deleteOne({order_id: 1})`, w), "-- 1 row affected")
	expectOut(t, "findOneAndUpdate", runQ(t, rw, `db.users.findOneAndUpdate({_id: 3}, {$set: {name: "Nobody"}}, {returnDocument: "after", projection: {name: 1}})`, w),
		"_id\tname\n3\tNobody\n-- 1 row")
	expectOut(t, "createIndex", runQ(t, rw, `db.orders.createIndex({status: 1})`, w), "index\nstatus_1\n-- 1 row")
	expectOut(t, "dropIndex", runQ(t, rw, `db.orders.dropIndex("status_1")`, w), "-- 0 rows affected")
	_, err := rw.Query(context.Background(), `db.users.insertOne({_id: 1, email: "dup@example.com"})`, w)
	if e := qerr.From(err); err == nil || e.Category != qerr.QueryError || !strings.Contains(e.Message, "E11000") {
		t.Errorf("duplicate key: %v", err)
	}
	expectOut(t, "renameCollection", runQ(t, rw, `db.order_items.renameCollection("items")`, w), "-- 0 rows affected")
	expectOut(t, "renamed", runQ(t, rw, `db.items.countDocuments()`, w), "count\n19\n-- 1 row")
}

func TestTimeout(t *testing.T) {
	ro := openSpec(t, fixtureSpec(t, mongo8, engine.ModeRO))
	start := time.Now()
	_, err := ro.Query(context.Background(), `db.orders.find({$where: "sleep(2000) || true"})`,
		engine.QueryOpts{MaxRows: 10, ReadOnly: true, Timeout: 300 * time.Millisecond})
	if e := qerr.From(err); err == nil || e.Category != qerr.Timeout {
		t.Errorf("slow query: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("timeout took %v", d)
	}
}

func TestUnknownCollection(t *testing.T) {
	ro := openSpec(t, fixtureSpec(t, mongo8, engine.ModeRO))
	for _, q := range []string{`db.ordrs.find({status: "paid"})`, `db.ordrs.countDocuments({})`, `db.ordrs.aggregate([{$match: {}}])`, `db.ordrs.getIndexes()`} {
		_, err := ro.Query(context.Background(), q, roOpts)
		if e := qerr.From(err); err == nil || e.Category != qerr.QueryError || e.UnknownTable != "ordrs" || !strings.Contains(e.Message, `collection "ordrs" does not exist`) {
			t.Errorf("%s: %#v", q, e)
		}
	}
}

func TestExplain(t *testing.T) {
	ro := openSpec(t, fixtureSpec(t, mongo8, engine.ModeRO))
	for _, q := range []string{`db.orders.find({status: "paid"})`, `db.orders.aggregate([{$match: {status: "paid"}}])`} {
		r, err := ro.Explain(context.Background(), q, roOpts)
		if err != nil || len(r.Columns) != 1 || r.Columns[0].Name != "plan" || len(r.Rows) != 1 || !strings.Contains(r.Rows[0][0].Text, "stage") {
			t.Errorf("explain %s: %+v %v", q, r, err)
		}
	}
	got := runQ(t, ro, `db.orders.find({status: "paid"}).explain()`, roOpts)
	if !strings.HasPrefix(got, "plan\n") {
		t.Errorf(".explain(): %q", got)
	}
	if _, err := ro.Explain(context.Background(), `db.orders.deleteMany({})`, roOpts); qerr.From(err).Category != qerr.Invalid {
		t.Errorf("explain of a write: %v", err)
	}
}

func TestAllowedNamespacesGuardQueries(t *testing.T) {
	spec := fixtureSpec(t, mongo8, engine.ModeRO)
	free := openSpec(t, spec)
	if _, err := free.Query(context.Background(), `db.getSiblingDB("admin").system.version.find()`, roOpts); err != nil {
		t.Errorf("no allowed_namespaces: %v", err)
	}
	db := spec.DSN[strings.LastIndex(spec.DSN, "/")+1 : strings.Index(spec.DSN, "?")]
	spec.Namespaces = []string{db}
	guarded := openSpec(t, spec)
	_, err := guarded.Query(context.Background(), `db.getSiblingDB("admin").system.version.find()`, roOpts)
	if e := qerr.From(err); err == nil || e.Category != qerr.Forbidden || !strings.Contains(e.Message, `"admin"`) {
		t.Errorf("guarded: %v", err)
	}
	expectOut(t, "own db", runQ(t, guarded, `db.getSiblingDB("`+db+`").users.countDocuments({})`, roOpts), "count\n3\n-- 1 row")
}

func TestHugeDocumentsAreCapped(t *testing.T) {
	spec := fixtureSpec(t, mongo8, engine.ModeRO)
	// Queries are capped at 256 KiB, so the big document goes in through the driver.
	client, err := mongo.Connect(options.Client().ApplyURI(spec.DSN))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	arr := make(bson.A, 20000)
	for i := range arr {
		arr[i] = int32(i)
	}
	big := bson.D{{Key: "_id", Value: int32(1)}, {Key: "s", Value: strings.Repeat("x", 1<<20)}, {Key: "arr", Value: arr}}
	db := spec.DSN[strings.LastIndex(spec.DSN, "/")+1 : strings.Index(spec.DSN, "?")]
	if _, err := client.Database(db).Collection("big").InsertMany(context.Background(), []any{big, bson.D{{Key: "_id", Value: int32(2)}}}); err != nil {
		t.Fatal(err)
	}
	ro := openSpec(t, spec)
	r, err := ro.Query(context.Background(), `db.big.find().sort({_id: 1})`, engine.QueryOpts{MaxRows: 1, ReadOnly: true, Timeout: 20 * time.Second})
	if err != nil || len(r.Rows) != 1 || !r.Truncated {
		t.Fatalf("%+v %v", r, err)
	}
	out := format.Result(r)
	if len(out) > 2000 || strings.Count(out, "…[+") != 2 {
		t.Fatalf("cells not cut (%d bytes): %.300s", len(out), out)
	}
}

func TestReadOnlyHalves(t *testing.T) {
	spec := fixtureSpec(t, mongo8, engine.ModeRO)
	ro := openSpec(t, spec)
	rw := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: spec.DSN, Mode: engine.ModeRW})
	const w = `db.users.updateMany({}, {$set: {name: "x"}})`
	// A read-only connection refuses a write even when the call does not ask for it.
	if _, err := ro.Query(context.Background(), w, engine.QueryOpts{MaxRows: 10, Timeout: 10 * time.Second}); qerr.From(err).Category != qerr.ReadOnly {
		t.Errorf("read-only connection, ReadOnly=false: %v", err)
	}
	// A read-write connection refuses a write when the call asks for read-only.
	if _, err := rw.Query(context.Background(), w, roOpts); qerr.From(err).Category != qerr.ReadOnly {
		t.Errorf("read-write connection, ReadOnly=true: %v", err)
	}
	expectOut(t, "untouched", runQ(t, rw, `db.users.countDocuments({name: "x"})`, roOpts), "count\n0\n-- 1 row")
}

func TestExplainRefusals(t *testing.T) {
	spec := fixtureSpec(t, mongo8, engine.ModeRW)
	rw := openSpec(t, spec)
	w := engine.QueryOpts{MaxRows: 10, Timeout: 10 * time.Second}
	if _, err := rw.Explain(context.Background(), `db.orders.aggregate([{$match: {}}, {$out: "evil"}])`, w); qerr.From(err).Category != qerr.Invalid {
		t.Errorf("explain of $out: %v", err)
	}
	if _, err := rw.Query(context.Background(), `db.orders.deleteMany({}).explain()`, w); qerr.From(err).Category != qerr.Invalid {
		t.Errorf(".explain() on a write: %v", err)
	}
	expectOut(t, "nothing ran", runQ(t, rw, `db.orders.countDocuments({})`, w), "count\n250\n-- 1 row")
	if got := runQ(t, rw, `db.getCollectionNames()`, w); strings.Contains(got, "evil") {
		t.Errorf("evil exists: %q", got)
	}
}

func TestEmptyReadOfAViewIsNotAMissingCollection(t *testing.T) {
	ro := openSpec(t, fixtureSpec(t, mongo8, engine.ModeRO))
	expectOut(t, "find", runQ(t, ro, `db.paid_orders.find({status: "x"})`, roOpts), "_id\n-- 0 rows")
	expectOut(t, "count", runQ(t, ro, `db.paid_orders.countDocuments({status: "x"})`, roOpts), "count\n0\n-- 1 row")
}

func TestPartialInsertSaysWhatWasWritten(t *testing.T) {
	rw := openSpec(t, fixtureSpec(t, mongo8, engine.ModeRW))
	w := engine.QueryOpts{MaxRows: 100, Timeout: 10 * time.Second}
	_, err := rw.Query(context.Background(), `db.users.insertMany([{_id: 1, email: "dup@example.com"}, {_id: 100, email: "a@example.com"}, {_id: 101, email: "b@example.com"}], {ordered: false})`, w)
	if e := qerr.From(err); err == nil || e.Category != qerr.QueryError || !strings.Contains(e.Message, "E11000") ||
		!strings.HasSuffix(e.Message, "; 2 of 3 documents were written before the error") {
		t.Errorf("unordered: %v", err)
	}
	expectOut(t, "written", runQ(t, rw, `db.users.countDocuments({_id: {$gt: 99}})`, w), "count\n2\n-- 1 row")
	_, err = rw.Query(context.Background(), `db.users.insertMany([{_id: 200, email: "c@example.com"}, {_id: 1, email: "dup2@example.com"}, {_id: 201, email: "d@example.com"}])`, w)
	if e := qerr.From(err); err == nil || !strings.HasSuffix(e.Message, "; 1 of 3 documents were written before the error") {
		t.Errorf("ordered: %v", err)
	}
	// Nothing was written: no suffix.
	_, err = rw.Query(context.Background(), `db.users.insertOne({_id: 1, email: "dup3@example.com"})`, w)
	if e := qerr.From(err); err == nil || strings.Contains(e.Message, "written before") {
		t.Errorf("single: %v", err)
	}
}

func TestWriteErrorReplies(t *testing.T) {
	reply := func(d bson.D) bson.Raw { return rawDoc(t, d) }
	wc := bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: 1},
		{Key: "writeConcernError", Value: bson.D{{Key: "code", Value: 64}, {Key: "errmsg", Value: "waiting for replication timed out"}}}}
	err := writeError(reply(wc), &Op{Kind: kInsert, IDs: []any{1}})
	if e := qerr.From(err); err == nil || e.Category != qerr.QueryError ||
		!strings.Contains(e.Message, "applied") || !strings.Contains(e.Message, "not confirmed") || !strings.Contains(e.Message, "waiting for replication timed out") {
		t.Errorf("write concern error: %v", err)
	}
	if err := writeError(reply(bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: 1}}), &Op{Kind: kInsert, IDs: []any{1}}); err != nil {
		t.Errorf("clean reply: %v", err)
	}
}

func TestUpsertCountsAsAffected(t *testing.T) {
	rw := openSpec(t, fixtureSpec(t, mongo8, engine.ModeRW))
	w := engine.QueryOpts{MaxRows: 100, Timeout: 10 * time.Second}
	expectOut(t, "upsert", runQ(t, rw, `db.users.updateOne({_id: 500}, {$set: {email: "up@example.com"}}, {upsert: true})`, w), "-- 1 row affected")
	expectOut(t, "again", runQ(t, rw, `db.users.updateOne({_id: 500}, {$set: {email: "up2@example.com"}}, {upsert: true})`, w), "-- 1 row affected")
	expectOut(t, "unchanged", runQ(t, rw, `db.users.updateOne({_id: 500}, {$set: {email: "up2@example.com"}}, {upsert: true})`, w), "-- 0 rows affected")
}

func TestAllowedIsASCIICaseInsensitive(t *testing.T) {
	c := &conn{namespaces: []string{"shop"}}
	for db, want := range map[string]bool{"shop": true, "SHOP": true, "Shop": true, "ſhop": false, "shop2": false, "": false, "sh0p": false, "KHop": false} {
		if got := c.allowed(db); got != want {
			t.Errorf("allowed(%q) = %v, want %v", db, got, want)
		}
	}
	if !(&conn{}).allowed("anything") {
		t.Error("no namespaces allows everything")
	}
	// The Kelvin sign folds to k in Unicode.
	if (&conn{namespaces: []string{"k"}}).allowed("\u212a") {
		t.Error("Kelvin sign matched k")
	}
}

func TestNamespaceGuardCoversStagesThatNameADatabase(t *testing.T) {
	// A nil client proves the refusal comes before any server contact.
	c := &conn{defaultDB: "shop", namespaces: []string{"shop"}}
	refused := []string{
		`db.orders.aggregate([{$out: {db: "admin", coll: "x"}}])`,
		`db.orders.aggregate([{$merge: {into: {db: "admin", coll: "x"}}}])`,
		`db.orders.aggregate([{$lookup: {from: {db: "admin", coll: "system.users"}, localField: "a", foreignField: "b", as: "u"}}])`,
		`db.orders.aggregate([{$unionWith: {coll: "system.users", db: "admin"}}])`,
		`db.orders.aggregate([{$unionWith: {db: "ADMIN", coll: "c", pipeline: [{$match: {}}]}}])`,
		// nested
		`db.orders.aggregate([{$facet: {a: [{$lookup: {from: {db: "admin", coll: "c"}, as: "u", pipeline: []}}]}}])`,
		`db.orders.aggregate([{$lookup: {from: "items", as: "i", pipeline: [{$unionWith: {coll: "c", db: "admin"}}]}}])`,
		`db.orders.aggregate([{$unionWith: {coll: "c", pipeline: [{$facet: {z: [{$merge: {into: {db: "admin", coll: "x"}}}]}}]}}])`,
		`db.orders.aggregate([{$lookup: {from: {db: "ſhop2", coll: "c"}, as: "u", pipeline: []}}])`,
		`db.orders.aggregate([{$lookup: {from: {db: {$literal: "shop"}, coll: "c"}, as: "u", pipeline: []}}])`, // not a string: cannot be checked
	}
	for _, q := range refused {
		_, err := c.Query(context.Background(), q, roOpts)
		if e := qerr.From(err); err == nil || e.Category != qerr.Forbidden {
			t.Errorf("%s: %v", q, err)
		}
		if _, err := c.Explain(context.Background(), q, roOpts); qerr.From(err).Category != qerr.Forbidden && qerr.From(err).Category != qerr.Invalid {
			t.Errorf("explain %s: %v", q, err)
		}
	}
	// Stages that name only allowed databases, or none, pass the guard.
	for _, q := range []string{
		`db.orders.aggregate([{$lookup: {from: {db: "shop", coll: "c"}, as: "u", pipeline: []}}])`,
		`db.orders.aggregate([{$lookup: {from: "items", as: "u", localField: "a", foreignField: "b"}}])`,
		`db.orders.aggregate([{$unionWith: {coll: "c", db: "SHOP"}}])`,
		`db.orders.aggregate([{$facet: {a: [{$match: {db: "admin"}}]}}])`, // a field called db is not a database
		`db.orders.find({db: "admin"})`,
	} {
		if _, err := c.prepare(q, 10); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	// With no allowed namespaces nothing is refused.
	if _, err := (&conn{defaultDB: "shop"}).prepare(refused[0], 10); err != nil {
		t.Errorf("unrestricted: %v", err)
	}
}
