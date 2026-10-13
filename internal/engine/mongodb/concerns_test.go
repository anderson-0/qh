package mongodb

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/anderson-0/qh/internal/engine"
)

// runPref applies o and returns the read preference it sets.
func runPref(t *testing.T, o *options.RunCmdOptionsBuilder) *readpref.ReadPref {
	t.Helper()
	var args options.RunCmdOptions
	for _, f := range o.List() {
		if err := f(&args); err != nil {
			t.Fatal(err)
		}
	}
	return args.ReadPreference
}

func concernConn(t *testing.T, dsn string) *conn {
	t.Helper()
	opts, _, err := clientOptions(engine.ConnSpec{Engine: "mongodb", DSN: dsn, Server: true})
	if err != nil {
		t.Fatal(err)
	}
	c := &conn{}
	c.setConcerns(opts)
	return c
}

func mustOp(t *testing.T, q string) *Op {
	t.Helper()
	call, err := Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	op, err := translate(call, "db", 10)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestConcernsReachCommands(t *testing.T) {
	c := concernConn(t, "mongodb://h/db?readPreference=secondaryPreferred&readConcernLevel=majority&w=majority&journal=true&wtimeoutMS=100")
	wantRC := bson.D{{Key: "level", Value: "majority"}}
	wantWC := bson.D{{Key: "w", Value: "majority"}, {Key: "j", Value: true}}
	for _, tc := range []struct {
		q         string
		rc, wc    bool
		secondary bool
	}{
		{`db.t.find({a: 1})`, true, false, true},
		{`db.t.findOne()`, true, false, true},
		{`db.t.aggregate([{$match: {}}])`, true, false, true},
		{`db.t.countDocuments({})`, true, false, true},
		{`db.t.estimatedDocumentCount()`, true, false, true},
		{`db.t.distinct("a")`, true, false, true},
		{`db.t.getIndexes()`, false, false, true},
		{`db.getCollectionNames()`, false, false, true},
		{`db.t.insertOne({a: 1})`, false, true, false},
		{`db.t.updateMany({}, {$set: {a: 1}})`, false, true, false},
		{`db.t.deleteOne({})`, false, true, false},
		{`db.t.findOneAndDelete({})`, false, true, false},
		{`db.t.aggregate([{$match: {}}, {$out: "x"}])`, false, true, false},
		{`db.t.createIndex({a: 1})`, false, true, false},
		{`db.t.drop()`, false, true, false},
	} {
		op := mustOp(t, tc.q)
		before := len(op.Command)
		cmd, ro := c.command(op)
		if len(op.Command) != before {
			t.Errorf("%s: op.Command was changed", tc.q)
		}
		rc, hasRC := lookup(cmd, "readConcern")
		wc, hasWC := lookup(cmd, "writeConcern")
		if hasRC != tc.rc || tc.rc && !equalD(rc, wantRC) {
			t.Errorf("%s: readConcern = %v (%v)", tc.q, rc, hasRC)
		}
		if hasWC != tc.wc || tc.wc && !equalD(wc, wantWC) {
			t.Errorf("%s: writeConcern = %v (%v)", tc.q, wc, hasWC)
		}
		rp := runPref(t, ro)
		if tc.secondary != (rp != nil && rp.Mode() == readpref.SecondaryPreferredMode) || !tc.secondary && rp != nil && rp.Mode() != readpref.PrimaryMode {
			t.Errorf("%s: read preference = %v", tc.q, rp)
		}
	}
	// Explain and introspection read with the read preference; explain
	// sends no read concern.
	if rp := runPref(t, c.readOpts()); rp == nil || rp.Mode() != readpref.SecondaryPreferredMode {
		t.Errorf("readOpts: %v", rp)
	}
	if _, ok := lookup(c.readCmd(bson.D{{Key: "count", Value: "t"}}), "readConcern"); !ok {
		t.Error("introspection count has no readConcern")
	}
	if _, ok := lookup(c.readCmd(bson.D{{Key: "listIndexes", Value: "t"}}), "readConcern"); ok {
		t.Error("listIndexes has a readConcern")
	}
}

func TestNoConcernsLeaveCommandsAlone(t *testing.T) {
	c := concernConn(t, "mongodb://h/db")
	for _, q := range []string{`db.t.find({a: 1})`, `db.t.insertOne({a: 1})`} {
		op := mustOp(t, q)
		cmd, ro := c.command(op)
		if !equalD(cmd, op.Command) || runPref(t, ro) != nil {
			t.Errorf("%s: %v %v", q, cmd, runPref(t, ro))
		}
	}
	// w=2 alone: no j.
	c = concernConn(t, "mongodb://h/db?w=2")
	cmd, _ := c.command(mustOp(t, `db.t.insertOne({a: 1})`))
	if wc, _ := lookup(cmd, "writeConcern"); !equalD(wc, bson.D{{Key: "w", Value: 2}}) {
		t.Errorf("w=2: %v", wc)
	}
}

func equalD(a any, b bson.D) bool {
	x, err1 := bson.MarshalExtJSON(bson.D{{Key: "v", Value: a}}, false, false)
	y, err2 := bson.MarshalExtJSON(bson.D{{Key: "v", Value: b}}, false, false)
	return err1 == nil && err2 == nil && string(x) == string(y)
}
