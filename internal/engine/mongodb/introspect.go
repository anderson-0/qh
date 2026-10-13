package mongodb

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/anderson-0/qh/internal/engine"
)

const (
	sampleParallel = 4
	sampleTimeout  = 5 * time.Second
)

var systemDBs = map[string]bool{"admin": true, "local": true, "config": true}

// Introspect samples every collection and view of the DSN's database, or of
// every database the user may see when the DSN names none.
func (c *conn) Introspect(ctx context.Context) (*engine.Catalog, error) {
	dbs := []string{c.defaultDB}
	if !c.pinned {
		names, err := c.client.ListDatabaseNames(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
		if err != nil {
			return nil, c.err(err)
		}
		dbs = dbs[:0]
		for _, n := range names {
			if !systemDBs[n] {
				dbs = append(dbs, n)
			}
		}
		sort.Strings(dbs)
	}
	cat := &engine.Catalog{}
	for _, db := range dbs {
		if !c.allowed(db) {
			continue
		}
		ns, err := c.namespace(ctx, db)
		if err != nil {
			return nil, c.err(err)
		}
		cat.Namespaces = append(cat.Namespaces, ns)
	}
	if err := ctx.Err(); err != nil {
		return nil, c.err(err)
	}
	cat.Link()
	return cat, nil
}

type collInfo struct {
	name string
	view bool
}

// sampled is one collection's object plus what references need.
type sampled struct {
	obj *engine.Object
	inf *inferred
}

func (c *conn) namespace(ctx context.Context, db string) (engine.Namespace, error) {
	d := c.client.Database(db)
	cur, err := d.RunCommandCursor(ctx, bson.D{{Key: "listCollections", Value: 1}, {Key: "authorizedCollections", Value: true}})
	if err != nil {
		return engine.Namespace{}, err
	}
	var colls []collInfo
	for cur.Next(ctx) {
		name, _ := cur.Current.Lookup("name").StringValueOK()
		typ, _ := cur.Current.Lookup("type").StringValueOK()
		if name == "" || strings.HasPrefix(name, "system.") {
			continue
		}
		colls = append(colls, collInfo{name: name, view: typ == "view"})
	}
	err = cur.Err()
	closeCursor(ctx, cur)
	if err != nil {
		return engine.Namespace{}, err
	}
	sort.Slice(colls, func(i, j int) bool { return colls[i].name < colls[j].name })

	out := make([]sampled, len(colls))
	sem := make(chan struct{}, sampleParallel)
	var wg sync.WaitGroup
	for i, ci := range colls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = c.describe(ctx, d, db, ci)
		}()
	}
	wg.Wait()
	c.addReferences(ctx, d, db, out)
	ns := engine.Namespace{Name: db}
	for _, s := range out {
		ns.Objects = append(ns.Objects, s.obj)
	}
	return ns, nil
}

// describe counts, lists the indexes of, and samples one collection. Each
// step that fails leaves its part empty; the catalog still builds.
func (c *conn) describe(ctx context.Context, d *mongo.Database, db string, ci collInfo) sampled {
	ctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	obj := &engine.Object{Namespace: db, Name: ci.name, Kind: engine.KindCollection, RowEstimate: -1, PrimaryKey: []string{"_id"}}
	if ci.view {
		obj.Kind, obj.PrimaryKey = engine.KindView, nil
	} else {
		// count with no query reads the collection's metadata.
		if reply, err := d.RunCommand(ctx, bson.D{{Key: "count", Value: ci.name}}).Raw(); err == nil {
			obj.RowEstimate = asInt(reply.Lookup("n"))
		}
		obj.Indexes = indexes(ctx, d, ci.name)
	}
	// The driver derives maxTimeMS from ctx's deadline; adding it here
	// would duplicate the field.
	var cmd bson.D
	if ci.view || (obj.RowEstimate >= 0 && obj.RowEstimate <= sampleSize) {
		cmd = bson.D{{Key: "find", Value: ci.name}, {Key: "limit", Value: sampleSize}, {Key: "batchSize", Value: sampleSize}}
	} else {
		cmd = bson.D{{Key: "aggregate", Value: ci.name},
			{Key: "pipeline", Value: bson.A{bson.D{{Key: "$sample", Value: bson.D{{Key: "size", Value: sampleSize}}}}}},
			{Key: "cursor", Value: bson.D{{Key: "batchSize", Value: sampleSize}}}}
	}
	var docs []bson.Raw
	if cur, err := d.RunCommandCursor(ctx, cmd); err == nil {
		for len(docs) < sampleSize && cur.Next(ctx) {
			docs = append(docs, append(bson.Raw(nil), cur.Current...))
		}
		if cur.Err() != nil {
			docs = nil
		}
		closeCursor(ctx, cur)
	}
	inf := infer(docs)
	obj.Fields, obj.Comment = inf.fields, inf.comment
	if ci.view {
		for i := range obj.Fields {
			obj.Fields[i].Nullable = true
		}
	}
	return sampled{obj: obj, inf: inf}
}

// indexes lists coll's indexes. Special kinds label their columns (ruling
// R4): bio:text, loc:2dsphere, k:hashed, and a TTL index adds " ttl" to its
// last column.
func indexes(ctx context.Context, d *mongo.Database, coll string) []engine.Index {
	cur, err := d.RunCommandCursor(ctx, bson.D{{Key: "listIndexes", Value: coll}})
	if err != nil {
		return nil
	}
	defer closeCursor(ctx, cur)
	var out []engine.Index
	for cur.Next(ctx) {
		key, ok := cur.Current.Lookup("key").DocumentOK()
		if !ok {
			continue
		}
		elems, _ := key.Elements()
		var cols []string
		for _, e := range elems {
			switch e.Key() {
			case "_fts":
				// A text index keys on _fts/_ftsx; its fields are the weights.
				weights, _ := cur.Current.Lookup("weights").DocumentOK()
				ws, _ := weights.Elements()
				for _, w := range ws {
					cols = append(cols, w.Key()+":text")
				}
				continue
			case "_ftsx":
				continue
			}
			col := e.Key()
			if s, ok := e.Value().StringValueOK(); ok {
				col += ":" + s
			}
			cols = append(cols, col)
		}
		if _, err := cur.Current.LookupErr("expireAfterSeconds"); err == nil && len(cols) > 0 {
			cols[len(cols)-1] += " ttl"
		}
		unique, _ := cur.Current.Lookup("unique").BooleanOK()
		name, _ := cur.Current.Lookup("name").StringValueOK()
		out = append(out, engine.Index{Name: name, Columns: cols, Unique: unique})
	}
	return out
}

// refSuffixes end the names of fields that may reference another
// collection's _id (spec §5.3 plus ruling R1).
var refSuffixes = []string{"_id", "Id", "_key", "_ref"}

// addReferences infers and verifies references between the collections of
// one database. Views are sources too (a view's user_id points at users),
// never targets.
func (c *conn) addReferences(ctx context.Context, d *mongo.Database, db string, colls []sampled) {
	byName := map[string]sampled{}
	for _, s := range colls {
		byName[strings.ToLower(s.obj.Name)] = s
	}
	for _, s := range colls {
		for _, f := range s.obj.Fields {
			last := f.Name[strings.LastIndexAny(f.Name, ".]")+1:]
			if last == "_id" {
				continue
			}
			base := ""
			for _, suf := range refSuffixes {
				if strings.HasSuffix(last, suf) && len(last) > len(suf) {
					base = strings.ToLower(strings.TrimSuffix(last, suf))
					break
				}
			}
			if base == "" {
				continue
			}
			target, ok := pickTarget(byName, base)
			if !ok || target.obj == s.obj {
				continue
			}
			if s.inf.types[f.Name] == "" || s.inf.types[f.Name] != target.inf.types["_id"] {
				continue
			}
			if verified(ctx, d, target.obj.Name, s.inf.samples[f.Name]) {
				s.obj.ForeignKeys = append(s.obj.ForeignKeys, engine.ForeignKey{Namespace: db, Object: s.obj.Name, Columns: []string{f.Name},
					RefNamespace: db, RefObject: target.obj.Name, RefColumns: []string{"_id"}})
			}
		}
	}
}

// pickTarget finds the collection a field base name points at: base, then
// base+"s", then the one collection ending in "_"+base, then the one ending
// in "_"+base+"s". Each form is tried in turn, so catalog_item wins over
// order_items for item_key. Views are never targets.
func pickTarget(byName map[string]sampled, base string) (sampled, bool) {
	for _, n := range []string{base, base + "s"} {
		if s, ok := byName[n]; ok && s.obj.Kind == engine.KindCollection {
			return s, true
		}
	}
	for _, suffix := range []string{"_" + base, "_" + base + "s"} {
		var found []sampled
		for n, s := range byName {
			if strings.HasSuffix(n, suffix) && s.obj.Kind == engine.KindCollection {
				found = append(found, s)
			}
		}
		switch len(found) {
		case 1:
			return found[0], true
		case 0:
			continue
		}
		return sampled{}, false
	}
	return sampled{}, false
}

// verified reports whether at least 4 of 5 sampled values (all of them, if
// fewer) exist as _id values in coll.
func verified(ctx context.Context, d *mongo.Database, coll string, values []bson.RawValue) bool {
	if len(values) == 0 {
		return false
	}
	in := bson.A{}
	for _, v := range values {
		in = append(in, v)
	}
	ctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	cmd := bson.D{{Key: "find", Value: coll}, {Key: "filter", Value: bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: in}}}}},
		{Key: "projection", Value: bson.D{{Key: "_id", Value: 1}}}, {Key: "limit", Value: len(values)}, {Key: "batchSize", Value: len(values)}}
	cur, err := d.RunCommandCursor(ctx, cmd)
	if err != nil {
		return false
	}
	defer closeCursor(ctx, cur)
	found := 0
	for cur.Next(ctx) {
		found++
	}
	if cur.Err() != nil {
		return false
	}
	need := len(values)
	if need == refSamples {
		need = refSamples - 1
	}
	return found >= need
}
