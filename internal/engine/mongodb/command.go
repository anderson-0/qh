package mongodb

import (
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

type opKind int

const (
	kDocs opKind = iota + 1
	kNames
	kCount
	kAggCount
	kDistinct
	kInsert
	kUpdate
	kDelete
	kFindAndModify
	kAdmin
)

// Op is a Call translated into one database command.
type Op struct {
	Access     engine.Access
	Kind       opKind
	DB         string
	Collection string
	Command    bson.D
	Explain    bool
	Key        string
	IDs        []any
	IndexNames []string
}

// method describes one supported method: its access class, its argument
// count, the cursor modifiers it takes, and whether it can be explained.
type method struct {
	access    engine.Access
	minArgs   int
	maxArgs   int
	modifiers string // space-separated names
	explain   bool
	dbLevel   bool
}

const findModifiers = "sort limit skip project projection hint collation count toArray pretty explain"

var methods = map[string]method{
	"find":                   {engine.Read, 0, 2, findModifiers, true, false},
	"findOne":                {engine.Read, 0, 2, "toArray pretty explain", true, false},
	"aggregate":              {engine.Read, 1, 2, "toArray pretty explain", true, false},
	"countDocuments":         {engine.Read, 0, 2, "explain", true, false},
	"estimatedDocumentCount": {engine.Read, 0, 1, "", false, false},
	"distinct":               {engine.Read, 1, 2, "toArray pretty explain", true, false},
	"getIndexes":             {engine.Read, 0, 0, "toArray pretty", false, false},
	"getCollectionNames":     {engine.Read, 0, 0, "toArray pretty", false, true},
	"insertOne":              {engine.Write, 1, 2, "", false, false},
	"insertMany":             {engine.Write, 1, 2, "", false, false},
	"updateOne":              {engine.Write, 2, 3, "", false, false},
	"updateMany":             {engine.Write, 2, 3, "", false, false},
	"replaceOne":             {engine.Write, 2, 3, "", false, false},
	"deleteOne":              {engine.Write, 1, 2, "", false, false},
	"deleteMany":             {engine.Write, 1, 2, "", false, false},
	"findOneAndUpdate":       {engine.Write, 2, 3, "", false, false},
	"findOneAndReplace":      {engine.Write, 2, 3, "", false, false},
	"findOneAndDelete":       {engine.Write, 1, 2, "", false, false},
	"createIndex":            {engine.DDL, 1, 2, "", false, false},
	"createIndexes":          {engine.DDL, 1, 2, "", false, false},
	"dropIndex":              {engine.DDL, 1, 1, "", false, false},
	"dropIndexes":            {engine.DDL, 0, 1, "", false, false},
	"drop":                   {engine.DDL, 0, 0, "", false, false},
	"renameCollection":       {engine.DDL, 1, 2, "", false, false},
	"createCollection":       {engine.DDL, 1, 2, "", false, true},
}

func invalid(msg string) error { return qerr.New(qerr.Invalid, msg) }

// classify checks c's method, arguments and modifiers and returns its access class.
func classify(c *Call) (engine.Access, error) {
	m, ok := methods[c.Method]
	if ok && m.dbLevel && c.Collection != "" {
		example := "db.getCollectionNames()"
		if c.Method == "createCollection" {
			example = `db.createCollection("name")`
		}
		return 0, invalid(fmt.Sprintf("%s is called on db, as in %s", c.Method, example))
	}
	if !ok || m.dbLevel != (c.Collection == "") {
		e := qerr.New(qerr.Invalid, fmt.Sprintf("unsupported method %q", c.Method))
		if s := closest(c.Method, methodNames()); s != "" {
			e = e.WithHint("did you mean " + s + "?")
		}
		return 0, e
	}
	if len(c.Args) < m.minArgs || len(c.Args) > m.maxArgs {
		if len(c.Args) > m.maxArgs {
			if m.maxArgs == 0 {
				return 0, invalid(c.Method + " takes no arguments")
			}
			return 0, invalid(fmt.Sprintf("%s takes at most %d arguments", c.Method, m.maxArgs))
		}
		return 0, invalid(argHelp(c.Method))
	}
	allowed := strings.Fields(m.modifiers)
	for _, mod := range c.Modifiers {
		if !contains(allowed, mod.Name) {
			if mod.Name == "explain" {
				return 0, invalid("explain is supported for reads only")
			}
			if isKnownModifier(mod.Name) {
				return 0, invalid(fmt.Sprintf("%s does not take .%s()", c.Method, mod.Name))
			}
			e := qerr.New(qerr.Invalid, fmt.Sprintf("unsupported cursor method %q", mod.Name))
			if s := closest(mod.Name, strings.Fields(findModifiers)); s != "" {
				e = e.WithHint("did you mean " + s + "?")
			}
			return 0, e
		}
	}
	if c.Method == "aggregate" {
		if _, ok := c.Args[0].(bson.A); !ok {
			return 0, invalid("aggregate takes an array of stages")
		}
		if writesOut(c.Args[0]) {
			return engine.Write, nil
		}
	}
	return m.access, nil
}

func isKnownModifier(name string) bool { return contains(strings.Fields(findModifiers), name) }

func argHelp(name string) string {
	switch name {
	case "insertOne":
		return "insertOne takes a document"
	case "insertMany":
		return "insertMany takes an array of documents"
	case "aggregate":
		return "aggregate takes an array of stages"
	case "distinct":
		return "distinct takes a field name and an optional filter"
	case "createCollection":
		return "createCollection takes a collection name"
	case "renameCollection":
		return "renameCollection takes the new collection name"
	case "dropIndex":
		return "dropIndex takes an index name or key document"
	case "createIndex":
		return "createIndex takes a key document"
	case "createIndexes":
		return "createIndexes takes an array of key documents"
	}
	return name + " takes a filter and " + map[bool]string{true: "an update", false: "a document"}[strings.HasPrefix(name, "update") || name == "findOneAndUpdate"]
}

// writesOut reports whether v contains a $out or $merge stage at any depth.
func writesOut(v any) bool {
	switch x := v.(type) {
	case bson.D:
		for _, e := range x {
			if e.Key == "$out" || e.Key == "$merge" || writesOut(e.Value) {
				return true
			}
		}
	case bson.A:
		for _, e := range x {
			if writesOut(e) {
				return true
			}
		}
	}
	return false
}

// translate builds the command for c on database db.
func translate(c *Call, db string, maxRows int) (*Op, error) {
	access, err := classify(c)
	if err != nil {
		return nil, err
	}
	fetch := int64(maxRows + 1)
	op := &Op{Access: access, DB: db, Collection: c.Collection}
	arg := func(i int) any {
		if i < len(c.Args) {
			return c.Args[i]
		}
		return nil
	}
	doc := func(i int, what string) (bson.D, error) {
		v := arg(i)
		if v == nil {
			return bson.D{}, nil
		}
		d, ok := v.(bson.D)
		if !ok {
			return nil, invalid(fmt.Sprintf("%s takes %s", c.Method, what))
		}
		return d, nil
	}
	for _, m := range c.Modifiers {
		if m.Name == "explain" {
			op.Explain = true
		}
	}
	switch c.Method {
	case "find", "findOne":
		filter, err := doc(0, "a filter document")
		if err != nil {
			return nil, err
		}
		cmd := bson.D{{Key: "find", Value: c.Collection}, {Key: "filter", Value: filter}}
		var projection, sortSpec, hint, collation any
		if p := arg(1); p != nil {
			if projection, err = doc(1, "a projection document as its second argument"); err != nil {
				return nil, err
			}
		}
		var skip, limit int64
		counting := false
		for _, m := range c.Modifiers {
			switch m.Name {
			case "sort":
				sortSpec, err = modDoc(m)
			case "project", "projection":
				projection, err = modDoc(m)
			case "hint":
				if len(m.Args) == 1 {
					hint = m.Args[0]
				} else {
					err = invalid("hint takes an index name or key document")
				}
			case "collation":
				collation, err = modDoc(m)
			case "skip":
				skip, err = modInt(m)
				if err == nil && skip < 0 {
					err = invalid("skip takes a non-negative number")
				}
			case "limit":
				limit, err = modInt(m)
				if limit < 0 {
					limit = -limit
				}
			case "count":
				counting = true
			}
			if err != nil {
				return nil, err
			}
		}
		if counting {
			op.Kind = kAggCount
			op.Command = countPipeline(c.Collection, filter)
			return op, nil
		}
		for _, kv := range []struct {
			k string
			v any
		}{{"projection", projection}, {"sort", sortSpec}} {
			if kv.v != nil {
				cmd = append(cmd, bson.E{Key: kv.k, Value: kv.v})
			}
		}
		if skip > 0 {
			cmd = append(cmd, bson.E{Key: "skip", Value: skip})
		}
		if hint != nil {
			cmd = append(cmd, bson.E{Key: "hint", Value: hint})
		}
		if collation != nil {
			cmd = append(cmd, bson.E{Key: "collation", Value: collation})
		}
		n := fetch
		if c.Method == "findOne" {
			n = 1
		} else if limit > 0 && limit < n {
			n = limit
		}
		op.Kind = kDocs
		op.Command = append(cmd, bson.E{Key: "limit", Value: n}, bson.E{Key: "batchSize", Value: n})
	case "aggregate":
		stages := c.Args[0].(bson.A)
		pipeline := append(bson.A{}, stages...)
		ends := false
		if k := len(stages); k > 0 {
			if last, ok := stages[k-1].(bson.D); ok && len(last) > 0 && (last[0].Key == "$out" || last[0].Key == "$merge") {
				ends = true
			}
		}
		cursor := bson.D{}
		if !ends {
			pipeline = append(pipeline, bson.D{{Key: "$limit", Value: fetch}})
			cursor = bson.D{{Key: "batchSize", Value: fetch}}
		}
		cmd := bson.D{{Key: "aggregate", Value: c.Collection}, {Key: "pipeline", Value: pipeline}, {Key: "cursor", Value: cursor}}
		if o := arg(1); o != nil {
			opts, err := doc(1, "an options document as its second argument")
			if err != nil {
				return nil, err
			}
			cmd = append(cmd, pick(opts, "allowDiskUse", "collation", "hint", "let", "comment")...)
		}
		op.Kind, op.Command = kDocs, cmd
	case "countDocuments":
		filter, err := doc(0, "a filter document")
		if err != nil {
			return nil, err
		}
		op.Kind, op.Command = kAggCount, countPipeline(c.Collection, filter)
		if o := arg(1); o != nil {
			opts, err := doc(1, "an options document as its second argument")
			if err != nil {
				return nil, err
			}
			pipeline := op.Command[1].Value.(bson.A)
			head := pipeline[:1:1]
			for _, k := range []string{"skip", "limit"} {
				if v, ok := lookup(opts, k); ok {
					head = append(head, bson.D{{Key: "$" + k, Value: v}})
				}
			}
			op.Command[1].Value = append(head, pipeline[1])
		}
	case "estimatedDocumentCount":
		op.Kind, op.Command = kCount, bson.D{{Key: "count", Value: c.Collection}}
	case "distinct":
		key, ok := c.Args[0].(string)
		if !ok || key == "" {
			return nil, invalid("distinct takes a field name and an optional filter")
		}
		filter, err := doc(1, "a filter document as its second argument")
		if err != nil {
			return nil, err
		}
		op.Kind, op.Key = kDistinct, key
		op.Command = bson.D{{Key: "distinct", Value: c.Collection}, {Key: "key", Value: key}, {Key: "query", Value: filter}}
	case "getIndexes":
		op.Kind, op.Command = kDocs, bson.D{{Key: "listIndexes", Value: c.Collection}}
	case "getCollectionNames":
		op.Kind = kNames
		op.Command = bson.D{{Key: "listCollections", Value: 1}, {Key: "nameOnly", Value: true}, {Key: "authorizedCollections", Value: true}}
	case "insertOne", "insertMany":
		var docs bson.A
		if c.Method == "insertOne" {
			d, ok := c.Args[0].(bson.D)
			if !ok {
				return nil, invalid("insertOne takes a document")
			}
			docs = bson.A{d}
		} else {
			a, ok := c.Args[0].(bson.A)
			if !ok || len(a) == 0 {
				return nil, invalid("insertMany takes an array of documents")
			}
			docs = a
		}
		out := make(bson.A, len(docs))
		for i, v := range docs {
			d, ok := v.(bson.D)
			if !ok {
				return nil, invalid(c.Method + " takes documents")
			}
			id, has := lookup(d, "_id")
			if !has {
				id = bson.NewObjectID()
				d = append(bson.D{{Key: "_id", Value: id}}, d...)
			}
			op.IDs = append(op.IDs, id)
			out[i] = d
		}
		cmd := bson.D{{Key: "insert", Value: c.Collection}, {Key: "documents", Value: out}}
		if o := arg(1); o != nil {
			opts, err := doc(1, "an options document as its second argument")
			if err != nil {
				return nil, err
			}
			cmd = append(cmd, pick(opts, "ordered", "bypassDocumentValidation", "comment")...)
		}
		op.Kind, op.Command = kInsert, cmd
	case "updateOne", "updateMany", "replaceOne":
		filter, err := doc(0, "a filter document")
		if err != nil {
			return nil, err
		}
		u := c.Args[1]
		if c.Method == "replaceOne" {
			d, ok := u.(bson.D)
			if !ok || hasOperator(d) {
				return nil, invalid("replaceOne takes a replacement document without $ operators")
			}
		} else if !isUpdate(u) {
			return nil, invalid(c.Method + " takes an update with $ operators or a pipeline")
		}
		stmt := bson.D{{Key: "q", Value: filter}, {Key: "u", Value: u}, {Key: "multi", Value: c.Method == "updateMany"}}
		if o := arg(2); o != nil {
			opts, err := doc(2, "an options document as its third argument")
			if err != nil {
				return nil, err
			}
			stmt = append(stmt, pick(opts, "upsert", "arrayFilters", "hint", "collation")...)
		}
		op.Kind = kUpdate
		op.Command = bson.D{{Key: "update", Value: c.Collection}, {Key: "updates", Value: bson.A{stmt}}}
	case "deleteOne", "deleteMany":
		filter, err := doc(0, "a filter document")
		if err != nil {
			return nil, err
		}
		limit := int32(0)
		if c.Method == "deleteOne" {
			limit = 1
		}
		stmt := bson.D{{Key: "q", Value: filter}, {Key: "limit", Value: limit}}
		if o := arg(1); o != nil {
			opts, err := doc(1, "an options document as its second argument")
			if err != nil {
				return nil, err
			}
			stmt = append(stmt, pick(opts, "hint", "collation")...)
		}
		op.Kind = kDelete
		op.Command = bson.D{{Key: "delete", Value: c.Collection}, {Key: "deletes", Value: bson.A{stmt}}}
	case "findOneAndUpdate", "findOneAndReplace", "findOneAndDelete":
		filter, err := doc(0, "a filter document")
		if err != nil {
			return nil, err
		}
		cmd := bson.D{{Key: "findAndModify", Value: c.Collection}, {Key: "query", Value: filter}}
		optsAt := 1
		switch c.Method {
		case "findOneAndDelete":
			cmd = append(cmd, bson.E{Key: "remove", Value: true})
		case "findOneAndReplace":
			d, ok := c.Args[1].(bson.D)
			if !ok || hasOperator(d) {
				return nil, invalid("findOneAndReplace takes a replacement document without $ operators")
			}
			cmd, optsAt = append(cmd, bson.E{Key: "update", Value: d}), 2
		default:
			if !isUpdate(c.Args[1]) {
				return nil, invalid("findOneAndUpdate takes an update with $ operators or a pipeline")
			}
			cmd, optsAt = append(cmd, bson.E{Key: "update", Value: c.Args[1]}), 2
		}
		if o := arg(optsAt); o != nil {
			opts, err := doc(optsAt, "an options document")
			if err != nil {
				return nil, err
			}
			if v, ok := lookup(opts, "returnDocument"); ok && v == "after" {
				cmd = append(cmd, bson.E{Key: "new", Value: true})
			} else if v, ok := lookup(opts, "returnNewDocument"); ok && v == true {
				cmd = append(cmd, bson.E{Key: "new", Value: true})
			}
			if v, ok := lookup(opts, "projection"); ok {
				cmd = append(cmd, bson.E{Key: "fields", Value: v})
			}
			cmd = append(cmd, pick(opts, "sort", "upsert", "arrayFilters", "hint", "collation")...)
		}
		op.Kind, op.Command = kFindAndModify, cmd
	case "createIndex", "createIndexes":
		var keys []bson.D
		if c.Method == "createIndex" {
			d, ok := c.Args[0].(bson.D)
			if !ok || len(d) == 0 {
				return nil, invalid("createIndex takes a key document")
			}
			keys = []bson.D{d}
		} else {
			a, ok := c.Args[0].(bson.A)
			if !ok || len(a) == 0 {
				return nil, invalid("createIndexes takes an array of key documents")
			}
			for _, v := range a {
				d, ok := v.(bson.D)
				if !ok || len(d) == 0 {
					return nil, invalid("createIndexes takes an array of key documents")
				}
				keys = append(keys, d)
			}
		}
		var opts bson.D
		if o := arg(1); o != nil {
			if opts, err = doc(1, "an options document as its second argument"); err != nil {
				return nil, err
			}
		}
		indexes := bson.A{}
		for _, k := range keys {
			name := indexName(k)
			if v, ok := lookup(opts, "name"); ok {
				if len(keys) > 1 {
					return nil, invalid("name applies to one index; use createIndex")
				}
				s, ok := v.(string)
				if !ok || s == "" {
					return nil, invalid("the index name must be a non-empty string")
				}
				name = s
			}
			spec := bson.D{{Key: "key", Value: k}, {Key: "name", Value: name}}
			spec = append(spec, omit(opts, "key", "name")...)
			indexes = append(indexes, spec)
			op.IndexNames = append(op.IndexNames, name)
		}
		op.Kind = kAdmin
		op.Command = bson.D{{Key: "createIndexes", Value: c.Collection}, {Key: "indexes", Value: indexes}}
	case "dropIndex":
		switch c.Args[0].(type) {
		case string, bson.D:
		default:
			return nil, invalid("dropIndex takes an index name or key document")
		}
		op.Kind, op.Command = kAdmin, bson.D{{Key: "dropIndexes", Value: c.Collection}, {Key: "index", Value: c.Args[0]}}
	case "dropIndexes":
		var idx any = "*"
		if v := arg(0); v != nil {
			idx = v
		}
		op.Kind, op.Command = kAdmin, bson.D{{Key: "dropIndexes", Value: c.Collection}, {Key: "index", Value: idx}}
	case "drop":
		op.Kind, op.Command = kAdmin, bson.D{{Key: "drop", Value: c.Collection}}
	case "renameCollection":
		to, ok := c.Args[0].(string)
		if !ok || to == "" {
			return nil, invalid("renameCollection takes the new collection name")
		}
		cmd := bson.D{{Key: "renameCollection", Value: db + "." + c.Collection}, {Key: "to", Value: db + "." + to}}
		if v := arg(1); v != nil {
			cmd = append(cmd, bson.E{Key: "dropTarget", Value: v == true})
		}
		op.Kind, op.DB, op.Command = kAdmin, "admin", cmd
	case "createCollection":
		name, ok := c.Args[0].(string)
		if !ok || name == "" {
			return nil, invalid("createCollection takes a collection name")
		}
		cmd := bson.D{{Key: "create", Value: name}}
		if o := arg(1); o != nil {
			opts, err := doc(1, "an options document as its second argument")
			if err != nil {
				return nil, err
			}
			cmd = append(cmd, omit(opts, "create")...)
		}
		op.Kind, op.Collection, op.Command = kAdmin, name, cmd
	}
	return op, nil
}

func (op *Op) explainCommand() bson.D {
	cmd := op.Command
	if len(cmd) > 0 && cmd[0].Key == "aggregate" {
		// explain takes an aggregate without a cursor batch size; copy so
		// op.Command, which is also run for real, is left alone.
		cmd = make(bson.D, len(op.Command))
		for i, e := range op.Command {
			if e.Key == "cursor" {
				e.Value = bson.D{}
			}
			cmd[i] = e
		}
	}
	return bson.D{{Key: "explain", Value: cmd}, {Key: "verbosity", Value: "queryPlanner"}}
}

func countPipeline(coll string, filter bson.D) bson.D {
	return bson.D{{Key: "aggregate", Value: coll}, {Key: "pipeline", Value: bson.A{
		bson.D{{Key: "$match", Value: filter}},
		bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: int32(1)}, {Key: "n", Value: bson.D{{Key: "$sum", Value: int32(1)}}}}}},
	}}, {Key: "cursor", Value: bson.D{}}}
}

func modDoc(m Modifier) (bson.D, error) {
	if len(m.Args) == 1 {
		if d, ok := m.Args[0].(bson.D); ok {
			return d, nil
		}
	}
	return nil, invalid(m.Name + " takes a document")
}

func modInt(m Modifier) (int64, error) {
	if len(m.Args) == 1 {
		switch v := m.Args[0].(type) {
		case int32:
			return int64(v), nil
		case int64:
			return v, nil
		case float64:
			if v == float64(int64(v)) {
				return int64(v), nil
			}
		}
	}
	return 0, invalid(m.Name + " takes a number")
}

func lookup(d bson.D, key string) (any, bool) {
	for _, e := range d {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// pick returns the entries of d whose keys are listed, in d's order.
func pick(d bson.D, keys ...string) bson.D {
	var out bson.D
	for _, e := range d {
		if contains(keys, e.Key) {
			out = append(out, e)
		}
	}
	return out
}

// omit returns the entries of d whose keys are not listed.
func omit(d bson.D, keys ...string) bson.D {
	var out bson.D
	for _, e := range d {
		if !contains(keys, e.Key) {
			out = append(out, e)
		}
	}
	return out
}

func hasOperator(d bson.D) bool {
	for _, e := range d {
		if strings.HasPrefix(e.Key, "$") {
			return true
		}
	}
	return false
}

// isUpdate reports whether v is an update document ($ operators only) or an
// aggregation pipeline.
func isUpdate(v any) bool {
	switch x := v.(type) {
	case bson.A:
		return len(x) > 0
	case bson.D:
		if len(x) == 0 {
			return false
		}
		for _, e := range x {
			if !strings.HasPrefix(e.Key, "$") {
				return false
			}
		}
		return true
	}
	return false
}

// indexName is the default name MongoDB gives an index: field_value pairs
// joined by underscores, as in email_1_n_-1.
func indexName(keys bson.D) string {
	parts := make([]string, 0, 2*len(keys))
	for _, e := range keys {
		parts = append(parts, e.Key, fmt.Sprint(e.Value))
	}
	return strings.Join(parts, "_")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func methodNames() []string {
	out := make([]string, 0, len(methods))
	for n := range methods {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// closest returns the candidate within edit distance 2 of s, or "".
func closest(s string, candidates []string) string {
	best, bestD := "", 3
	for _, c := range candidates {
		if d := editDistance(strings.ToLower(s), strings.ToLower(c)); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
