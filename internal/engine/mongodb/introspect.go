package mongodb

import (
	"context"
	"math"
	"sort"
	"strconv"
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
	var allowed []string
	for _, db := range dbs {
		if c.allowed(db) {
			allowed = append(allowed, db)
		}
	}
	nss, err := collectNamespaces(allowed, func(db string) (engine.Namespace, error) { return c.namespace(ctx, db) })
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, c.err(err)
	}
	cat := &engine.Catalog{Namespaces: nss}
	cat.Link()
	return cat, nil
}

// collectNamespaces builds one namespace per database. A database whose
// collections cannot be listed becomes an empty namespace, so the others
// still build; only when every database fails is the first error returned.
func collectNamespaces(dbs []string, build func(string) (engine.Namespace, error)) ([]engine.Namespace, error) {
	var out []engine.Namespace
	var first error
	failed := 0
	for _, db := range dbs {
		ns, err := build(db)
		if err != nil {
			if first == nil {
				first = err
			}
			failed++
			ns = engine.Namespace{Name: db}
		}
		out = append(out, ns)
	}
	if failed > 0 && failed == len(dbs) {
		return nil, first
	}
	return out, nil
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
	// authorizedCollections takes effect only with nameOnly; together they
	// let a user with collection-level privileges list what it may read.
	// type is still returned.
	cur, err := d.RunCommandCursor(ctx, bson.D{{Key: "listCollections", Value: 1}, {Key: "nameOnly", Value: true},
		{Key: "authorizedCollections", Value: true}})
	if err != nil {
		return engine.Namespace{}, err
	}
	var colls []collInfo
	for cur.Next(ctx) {
		name, _ := cur.Current.Lookup("name").StringValueOK()
		typ, _ := cur.Current.Lookup("type").StringValueOK()
		if name == "" || systemName(cur.Current) {
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

// describe counts, lists the indexes of, and samples one collection, with
// the index listing running alongside the count and the sample. Each step
// that fails leaves its part empty; the catalog still builds.
func (c *conn) describe(ctx context.Context, d *mongo.Database, db string, ci collInfo) sampled {
	ctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	obj := &engine.Object{Namespace: db, Name: ci.name, Kind: engine.KindCollection, RowEstimate: -1, PrimaryKey: []string{"_id"}}
	var idx []engine.Index
	listed := make(chan struct{})
	if ci.view {
		obj.Kind, obj.PrimaryKey = engine.KindView, nil
		close(listed)
	} else {
		go func() {
			defer close(listed)
			idx = indexes(ctx, d, ci.name)
		}()
		// count with no query reads the collection's metadata.
		if reply, err := d.RunCommand(ctx, bson.D{{Key: "count", Value: ci.name}}).Raw(); err == nil {
			obj.RowEstimate = asInt(reply.Lookup("n"))
		}
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
	<-listed
	obj.Indexes = idx
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

// refCandidate is a field whose name and type point at a target
// collection's _id; it becomes a reference when its samples are found there.
type refCandidate struct {
	src    sampled
	field  string
	target string
}

// addReferences infers and verifies references between the collections of
// one database. Views are sources too (a view's user_id points at users),
// never targets. Verification runs one _id lookup per target collection,
// sampleParallel at a time.
func (c *conn) addReferences(ctx context.Context, d *mongo.Database, db string, colls []sampled) {
	byName := map[string]sampled{}
	for _, s := range colls {
		byName[strings.ToLower(s.obj.Name)] = s
	}
	var cands []refCandidate
	byTarget := map[string][]int{}
	var targets []string
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
			if !ok || target.obj == s.obj || !sameIDType(s.inf.types[f.Name], target.inf.types["_id"]) ||
				len(s.inf.samples[f.Name]) == 0 {
				continue
			}
			name := target.obj.Name
			if _, seen := byTarget[name]; !seen {
				targets = append(targets, name)
			}
			byTarget[name] = append(byTarget[name], len(cands))
			cands = append(cands, refCandidate{src: s, field: f.Name, target: name})
		}
	}
	found := make([]map[string]bool, len(targets))
	sem := make(chan struct{}, sampleParallel)
	var wg sync.WaitGroup
	for i, name := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var values []bson.RawValue
			for _, ci := range byTarget[name] {
				values = append(values, cands[ci].src.inf.samples[cands[ci].field]...)
			}
			found[i] = foundIDs(ctx, d, name, values)
		}()
	}
	wg.Wait()
	ok := make([]bool, len(cands))
	for i, name := range targets {
		for _, ci := range byTarget[name] {
			n, samples := 0, cands[ci].src.inf.samples[cands[ci].field]
			for _, v := range samples {
				if found[i][refKey(v)] {
					n++
				}
			}
			ok[ci] = enoughFound(n, len(samples))
		}
	}
	for i, cand := range cands {
		if ok[i] {
			cand.src.obj.ForeignKeys = append(cand.src.obj.ForeignKeys, engine.ForeignKey{Namespace: db, Object: cand.src.obj.Name,
				Columns: []string{cand.field}, RefNamespace: db, RefObject: cand.target, RefColumns: []string{"_id"}})
		}
	}
}

// numericTypes compare as one class: an int field can reference a long _id.
var numericTypes = map[string]bool{"int": true, "long": true, "double": true, "decimal": true}

// sameIDType reports whether a field of type a can reference an _id of type b.
func sameIDType(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || numericTypes[a] && numericTypes[b]
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

// foundIDs looks up values as _id in coll and returns the refKeys of those
// that exist; nil when the lookup fails.
func foundIDs(ctx context.Context, d *mongo.Database, coll string, values []bson.RawValue) map[string]bool {
	in := bson.A{}
	seen := map[string]bool{}
	for _, v := range values {
		if k := refKey(v); !seen[k] {
			seen[k] = true
			in = append(in, v)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	cmd := bson.D{{Key: "find", Value: coll}, {Key: "filter", Value: bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: in}}}}},
		{Key: "projection", Value: bson.D{{Key: "_id", Value: 1}}}, {Key: "limit", Value: len(in)}, {Key: "batchSize", Value: len(in)}}
	cur, err := d.RunCommandCursor(ctx, cmd)
	if err != nil {
		return nil
	}
	defer closeCursor(ctx, cur)
	found := map[string]bool{}
	for cur.Next(ctx) {
		found[refKey(cur.Current.Lookup("_id"))] = true
	}
	if cur.Err() != nil {
		return nil
	}
	return found
}

// enoughFound reports whether found of total sampled values verify a
// reference: 4 of 5, or all of them when there are fewer than 5.
func enoughFound(found, total int) bool {
	return total > 0 && found >= min(total, refSamples-1)
}

// refKey identifies a value for matching sampled values against _id values
// the server returns. Numbers compare by value, as the server does, so an
// int 1 matches a long or double 1.
func refKey(v bson.RawValue) string {
	switch v.Type {
	case bson.TypeInt32:
		return "n:" + strconv.FormatInt(int64(v.Int32()), 10)
	case bson.TypeInt64:
		return "n:" + strconv.FormatInt(v.Int64(), 10)
	case bson.TypeDouble:
		return numKey(v.Double())
	case bson.TypeDecimal128:
		if f, err := strconv.ParseFloat(v.Decimal128().String(), 64); err == nil {
			return numKey(f)
		}
	}
	return strconv.Itoa(int(v.Type)) + ":" + string(v.Value)
}

func numKey(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return "n:" + strconv.FormatInt(int64(f), 10)
	}
	return "n:" + strconv.FormatFloat(f, 'g', -1, 64)
}
