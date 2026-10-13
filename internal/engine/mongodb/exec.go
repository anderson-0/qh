package mongodb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

const defaultTimeout = 30 * time.Second

func (c *conn) allowed(db string) bool {
	if len(c.namespaces) == 0 {
		return true
	}
	for _, n := range c.namespaces {
		if strings.EqualFold(n, db) {
			return true
		}
	}
	return false
}

// prepare parses q, resolves its database and translates it.
func (c *conn) prepare(q string, maxRows int) (*Op, error) {
	call, err := Parse(q)
	if err != nil {
		return nil, err
	}
	db := call.DB
	if db == "" {
		db = c.defaultDB
	}
	if !c.allowed(db) {
		return nil, qerr.New(qerr.Forbidden, fmt.Sprintf("database %q is not allowed on this connection", db)).
			WithHint("Allowed: " + strings.Join(c.namespaces, ", "))
	}
	if maxRows <= 0 {
		maxRows = 100
	}
	return translate(call, db, maxRows)
}

func (c *conn) Query(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	op, err := c.prepare(q, o.MaxRows)
	if err != nil {
		return nil, err
	}
	if (o.ReadOnly || c.readOnly) && op.Access != engine.Read {
		return nil, qerr.New(qerr.ReadOnly, "this connection is read-only; only reads are allowed").
			WithHint("Use a read-write connection to change data")
	}
	return c.run(ctx, op, o, op.Explain)
}

func (c *conn) Explain(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	op, err := c.prepare(q, o.MaxRows)
	if err != nil {
		return nil, err
	}
	if op.Access != engine.Read || !methods[methodOf(op)].explain {
		return nil, qerr.New(qerr.Invalid, "explain is supported for reads only")
	}
	return c.run(ctx, op, o, true)
}

// methodOf returns the command name, used to look up explainability.
func methodOf(op *Op) string {
	switch op.Command[0].Key {
	case "find":
		return "find"
	case "aggregate":
		return "aggregate"
	case "distinct":
		return "distinct"
	}
	return ""
}

func (c *conn) run(ctx context.Context, op *Op, o engine.QueryOpts, explain bool) (*engine.Result, error) {
	start := time.Now()
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxRows := o.MaxRows
	if maxRows <= 0 {
		maxRows = 100
	}
	// The driver turns the context's deadline into the command's maxTimeMS
	// (less the round-trip time), so the server stops the work first and the
	// deadline is the backstop. Adding maxTimeMS by hand would duplicate it.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d := c.client.Database(op.DB)
	var res *engine.Result
	var err error
	if explain {
		res, err = c.explain(ctx, d, op)
	} else {
		res, err = c.exec(ctx, d, op, maxRows)
	}
	if err != nil {
		return nil, c.opErr(err, op)
	}
	if !explain && op.Access == engine.Read && op.Collection != "" && emptyRead(op, res) {
		if exists, err := c.collectionExists(ctx, d, op.Collection); err == nil && !exists {
			return nil, unknownCollection(op.Collection)
		}
	}
	res.Duration = time.Since(start)
	return res, nil
}

func unknownCollection(name string) error {
	e := qerr.New(qerr.QueryError, fmt.Sprintf("collection %q does not exist", name))
	e.UnknownTable = name
	return e
}

// emptyRead reports whether a read found nothing, which on MongoDB may mean
// the collection does not exist.
func emptyRead(op *Op, r *engine.Result) bool {
	switch op.Kind {
	case kDocs, kDistinct:
		return len(r.Rows) == 0
	case kCount, kAggCount:
		return len(r.Rows) == 1 && r.Rows[0][0].Text == "0"
	}
	return false
}

func (c *conn) collectionExists(ctx context.Context, d *mongo.Database, name string) (bool, error) {
	names, err := d.ListCollectionNames(ctx, bson.D{{Key: "name", Value: name}})
	return len(names) > 0, err
}

func (c *conn) opErr(err error, op *Op) error {
	var ce mongo.CommandError
	if errors.As(err, &ce) && ce.Code == codeNamespaceNotFound && op.Collection != "" {
		return unknownCollection(op.Collection)
	}
	return c.err(err)
}

func (c *conn) exec(ctx context.Context, d *mongo.Database, op *Op, maxRows int) (*engine.Result, error) {
	cmd := op.Command
	switch op.Kind {
	case kDocs, kNames, kAggCount:
		cur, err := d.RunCommandCursor(ctx, cmd)
		if err != nil {
			return nil, err
		}
		defer cur.Close(context.WithoutCancel(ctx))
		var docs []bson.Raw
		for len(docs) <= maxRows && cur.Next(ctx) {
			if op.Kind == kNames && systemName(cur.Current) {
				continue // system collections do not count against the row cap
			}
			docs = append(docs, append(bson.Raw(nil), cur.Current...))
		}
		if err := cur.Err(); err != nil {
			return nil, err
		}
		switch op.Kind {
		case kNames:
			return names(docs, maxRows), nil
		case kAggCount:
			n := "0"
			if len(docs) > 0 {
				if v, err := docs[0].LookupErr("n"); err == nil {
					n = valueOf(v).Text
				}
			}
			return countResult(n), nil
		}
		return docsResult(docs, maxRows), nil
	}
	reply, err := d.RunCommand(ctx, cmd).Raw()
	if err != nil {
		return nil, err
	}
	if err := writeError(reply); err != nil {
		return nil, err
	}
	switch op.Kind {
	case kCount:
		return countResult(valueOf(reply.Lookup("n")).Text), nil
	case kDistinct:
		var vals []bson.RawValue
		if arr, ok := reply.Lookup("values").ArrayOK(); ok {
			vals, _ = arr.Values()
		}
		r := &engine.Result{Columns: []engine.Column{{Name: op.Key}}, RowsAffected: -1}
		for i, v := range vals {
			if i == maxRows {
				r.Truncated = true
				break
			}
			r.Rows = append(r.Rows, []engine.Value{valueOf(v)})
		}
		return r, nil
	case kInsert:
		r := &engine.Result{Columns: []engine.Column{{Name: "_id"}}, RowsAffected: asInt(reply.Lookup("n"))}
		for _, id := range op.IDs {
			b, _ := bson.Marshal(bson.D{{Key: "v", Value: id}})
			r.Rows = append(r.Rows, []engine.Value{valueOf(bson.Raw(b).Lookup("v"))})
		}
		return r, nil
	case kUpdate:
		return &engine.Result{RowsAffected: asInt(reply.Lookup("nModified"))}, nil
	case kDelete:
		return &engine.Result{RowsAffected: asInt(reply.Lookup("n"))}, nil
	case kFindAndModify:
		v := reply.Lookup("value")
		if doc, ok := v.DocumentOK(); ok {
			return docsResult([]bson.Raw{doc}, maxRows), nil
		}
		return docsResult(nil, maxRows), nil
	case kAdmin:
		if len(op.IndexNames) > 0 {
			r := &engine.Result{Columns: []engine.Column{{Name: "index"}}, RowsAffected: -1}
			for _, n := range op.IndexNames {
				r.Rows = append(r.Rows, []engine.Value{{Text: n}})
			}
			return r, nil
		}
		return &engine.Result{RowsAffected: 0}, nil
	}
	return nil, fmt.Errorf("unknown operation kind %d", op.Kind)
}

func countResult(n string) *engine.Result {
	return &engine.Result{Columns: []engine.Column{{Name: "count"}}, Rows: [][]engine.Value{{{Text: n}}}, RowsAffected: -1}
}

// docsResult flattens docs, keeping at most maxRows. A read that found no
// documents still has an _id column, so it renders as "0 rows".
func docsResult(docs []bson.Raw, maxRows int) *engine.Result {
	r := &engine.Result{RowsAffected: -1}
	if len(docs) > maxRows {
		docs, r.Truncated = docs[:maxRows], true
	}
	r.Columns, r.Rows = flatten(docs)
	if len(r.Columns) == 0 {
		r.Columns = []engine.Column{{Name: "_id"}}
	}
	return r
}

// systemName reports whether a listCollections entry is a system collection.
func systemName(d bson.Raw) bool {
	n, _ := d.Lookup("name").StringValueOK()
	return strings.HasPrefix(n, "system.")
}

// names renders listCollections results as one "name" column. exec has
// already dropped the system collections.
func names(docs []bson.Raw, maxRows int) *engine.Result {
	r := &engine.Result{Columns: []engine.Column{{Name: "name"}}, RowsAffected: -1}
	for _, d := range docs {
		n, _ := d.Lookup("name").StringValueOK()
		if len(r.Rows) == maxRows {
			r.Truncated = true
			break
		}
		r.Rows = append(r.Rows, []engine.Value{{Text: n}})
	}
	return r
}

func asInt(v bson.RawValue) int64 {
	switch v.Type {
	case bson.TypeInt32:
		return int64(v.Int32())
	case bson.TypeInt64:
		return v.Int64()
	case bson.TypeDouble:
		return int64(v.Double())
	}
	return 0
}

// writeError returns the first write error or write-concern error in a write
// command's reply; RunCommand reports neither.
func writeError(reply bson.Raw) error {
	if arr, ok := reply.Lookup("writeErrors").ArrayOK(); ok {
		vals, _ := arr.Values()
		if len(vals) > 0 {
			d := vals[0].Document()
			msg, _ := d.Lookup("errmsg").StringValueOK()
			return qerr.New(qerr.QueryError, fmt.Sprintf("%s (code %d)", msg, asInt(d.Lookup("code"))))
		}
	}
	if d, ok := reply.Lookup("writeConcernError").DocumentOK(); ok {
		msg, _ := d.Lookup("errmsg").StringValueOK()
		return qerr.New(qerr.QueryError, "write concern error: "+msg)
	}
	return nil
}

func (c *conn) explain(ctx context.Context, d *mongo.Database, op *Op) (*engine.Result, error) {
	reply, err := d.RunCommand(ctx, op.explainCommand()).Raw()
	if err != nil {
		return nil, err
	}
	plan, ok := findKey(bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: reply}, "winningPlan")
	if !ok {
		plan = bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: reply}
	}
	return &engine.Result{Columns: []engine.Column{{Name: "plan"}}, Rows: [][]engine.Value{{{Text: relaxedJSON(plan)}}}, RowsAffected: -1}, nil
}

// findKey returns the first value named key anywhere inside v (depth first).
func findKey(v bson.RawValue, key string) (bson.RawValue, bool) {
	var elems []bson.RawElement
	switch v.Type {
	case bson.TypeEmbeddedDocument:
		elems, _ = v.Document().Elements()
	case bson.TypeArray:
		vals, _ := v.Array().Values()
		for _, x := range vals {
			if r, ok := findKey(x, key); ok {
				return r, true
			}
		}
		return bson.RawValue{}, false
	default:
		return bson.RawValue{}, false
	}
	for _, e := range elems {
		if e.Key() == key {
			return e.Value(), true
		}
		if r, ok := findKey(e.Value(), key); ok {
			return r, true
		}
	}
	return bson.RawValue{}, false
}
