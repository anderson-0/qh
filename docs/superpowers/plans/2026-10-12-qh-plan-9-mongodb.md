# qh MongoDB Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `mongodb` engine that agents use through qh's existing `schema` and `query` tools with mongosh syntax. Then show with the benchmark that it beats `mongodb-mcp-server` on tokens, accuracy and tool calls.

**Architecture:**
- **Classification:** each engine classifies its own queries. `Engine.Classify` and `Engine.ReferencedObjects` replace core's direct calls to `sqlx`.
- **Engine package:** a new package, `internal/engine/mongodb`, parses a fixed subset of mongosh into a `Call` and classifies it. It translates the call into one MongoDB database command (`find`, `aggregate`, `insert`, …), runs the command through the official Go driver v2 (`RunCommand` / `RunCommandCursor`), and flattens the documents into qh's engine-neutral `Result`.
- **Schema:** inferred by sampling documents.
- **Bench:** the harness gains two document datasets built from the existing Postgres containers. It also gets a configurable rival, so the same report compares qh with `mongodb-mcp-server`.

**Tech Stack:**
- Go (the version in `go.mod`);
- `go.mongodb.org/mongo-driver/v2` (latest v2.9.x);
- testcontainers-go `modules/mongodb` with `mongo:7` and `mongo:8`;
- the existing bench harness (Anthropic API, `npx`).

**Spec:** `docs/superpowers/specs/2026-10-12-qh-mongodb-engine-design.md`

## Global Constraints

These are copied from the spec, and every task must respect them.

**MCP surface**
- The `schema` and `query` tools, their descriptions and their schemas do not change. A workspace with only SQL connections gets byte-identical tool definitions and instructions.
- When at least one connection in the workspace has engine `mongodb`, the instructions gain exactly this sentence after the connections line, once: `mongodb sources take mongosh syntax: db.<collection>.find(…)/aggregate([…]).`

**What qh accepts and runs**
- No JavaScript is ever executed. Only the subset in spec §4 is accepted. Everything else fails with `invalid` before qh contacts the server.
- Read-only connections run only reads: `find`, `findOne`, `aggregate` without `$out`/`$merge` at any depth, `countDocuments`, `estimatedDocumentCount`, `distinct`, `getIndexes` and `getCollectionNames`. Writes give `read_only`. Explain is allowed for reads.
- Admin calls are classified as DDL. These are `createIndex(es)`, `dropIndex(es)`, `drop`, `renameCollection` and `createCollection`.
- Row caps work as follows:
  - `find`: `limit = min(user limit, max_rows + 1)`;
  - `aggregate`: a trailing `{$limit: max_rows + 1}` stage, except after `$out` or `$merge`;
  - `distinct`: cut in memory;
  - more than `max_rows` rows sets `Truncated`.
- Input is capped at 256 KiB and nesting at 100 levels.

**Results and schema**
- Columns: `_id` first, then the union of top-level keys in first-seen order. Nested values are compact relaxed Extended JSON, and missing keys print as NULL.
- Schema sampling:
  - up to 100 documents per collection;
  - 4 collections at a time, each with a 5 s timeout;
  - dotted paths to depth 4, `items[].sku` for arrays of documents, and at most 200 paths;
  - sampled string values (at most 8 distinct values, seen in at least 50 documents) end with `…`;
  - a reference is kept only after verifying that 4 of 5 sampled values exist.

**Security**
- In server mode, refuse the options `tlsCAFile`, `tlsCertificateKeyFile`, `tlsCertificateKeyFilePassword` and `tlsInsecure`, and the `authMechanism` values `MONGODB-AWS`, `GSSAPI` and `MONGODB-OIDC`. Errors never quote the DSN.
- `spec.Dial` dials every host. `spec.TLS` replaces the URL's TLS settings, and `spec.Password` replaces its password.
- A query naming a database outside `allowed_namespaces` fails with `forbidden` before qh contacts the server.
- No password or DSN ever appears in an error or log.

**Benchmark**
- The rival is `mongodb-mcp-server@3.0.5` via `npx`, run with `--readOnly` and `--telemetry disabled`, and with the connection string in `MDB_MCP_CONNECTION_STRING`.
- Datasets: `pagila-docs` and `lendco-docs`. They reuse the 60 existing questions and their SQL reference answers.

**Rulings made while planning** (small additions to the approved spec, recorded here):
- **R1. More reference candidates.** References (spec §5.3) also accept fields named `<x>_key`. A candidate may also target a collection whose name ends in `_<x>` or `_<x>s` when exactly one such collection exists.
  - Why: the Lendco names use `_key` (`lang_key`, `member_key`), and `item_key` and `unit_key` point at `catalog_item` and `stock_unit`.
  - Cost if wrong: a few extra verified candidates. They are still checked against the data.
- **R2. Database commands.** Every operation runs as a MongoDB database command (`find`, `aggregate`, `count`, `distinct`, `insert`, `update`, `delete`, `findAndModify`, `createIndexes`, `dropIndexes`, `create`, `drop`, `renameCollection`, `listCollections`, `listIndexes`, `explain`) through `RunCommand` or `RunCommandCursor`.
  - Why: one execution path; `maxTimeMS` is set explicitly on every command; and translation is testable without Docker.
  - Cost if wrong: none. These are the server's public commands.
- **R3. Question twins.** The bench stores each question's MongoDB twin as Extended JSON, `{"collection": …, "pipeline": […]}`, in `questions/<dataset>.json` under the key `mongo`. The bench runs twins through the driver directly.
  - Why: `bench` is a separate module and cannot import qh's `internal` parser.

- **R4. Index labels.** Labels for special index kinds (text, 2dsphere, hashed, TTL) go on the index column, as in `bio:text`, because `format` prints index columns and not index names. Cost if wrong: cosmetic.
- **R5. No dataset descriptions.** The document datasets carry no description, because `mongodb-mcp-server` has no place for one, so neither server gets one. Cost if wrong: qh loses a small advantage it would otherwise have.

## Review Focus

These five inputs are implied by the spec but untested by the task steps above, and are the most likely to bite users. Each has a test in the task named.

1. **A replica-set or `mongodb+srv` DSN in server mode.** Every host the driver contacts, including hosts it learns from SRV records or replica-set discovery, must be dialled through `spec.Dial`. Test: Task 4, `TestDialerSeesEveryHost`.
2. **Very large documents and arrays in results.** A find returning documents with a 20,000-element array or a 1 MB string must stay within the row cap, and each cell is cut by `format`'s existing cell truncation. Test: Task 5, `TestHugeDocumentsAreCapped`.
3. **Heterogeneous collections.** Documents that disagree on a field's type, nest deeper than 4 levels, or carry over 200 distinct paths. Inference must produce unions, stop at depth 4, cap the paths and record `…(+N fields)`. Test: Task 6, `TestInferenceCapsAndUnions`.
4. **A collection or view that disappears or fails during introspection.** It is dropped between `listCollections` and sampling, or it is a view whose pipeline errors. The catalog still builds and lists the object with no fields. Test: Task 6, `TestIntrospectSurvivesBrokenView`.
5. **`db.getSiblingDB("admin")` with no `allowed_namespaces`, and with `allowed_namespaces` set.** Without the setting it is allowed, because the server's own authorization applies. With the setting, `forbidden` is returned before contacting the server, and `getCollectionNames` lists only collections in the allowed databases. Test: Task 5, `TestAllowedNamespacesGuardQueries`.

## File Structure

| File | Responsibility |
|---|---|
| `internal/engine/engine.go` | `Access` alias; `Engine.Classify` and `Engine.ReferencedObjects`; `SQLEngine`; `ConnSpec.Namespaces` |
| `internal/engine/{postgres,mysql,sqlite}/*.go` | Delegate `Classify` and `ReferencedObjects` to `sqlx` |
| `internal/engine/enginetest/suite.go` | `RunCatalog`, the catalog checks every engine passes; `Run` calls it |
| `internal/core/service.go` | Uses `Engine.Classify` and `ReferencedObjects`; passes `AllowedNamespaces` into `ConnSpec` |
| `internal/engine/mongodb/lex.go` | Tokenizer for the mongosh subset |
| `internal/engine/mongodb/parse.go` | Parser: query text → `Call` (database, collection, method, BSON args, modifiers) |
| `internal/engine/mongodb/command.go` | `Call` → access class and database command(s) |
| `internal/engine/mongodb/mongodb.go` | Engine registration: `Inspect`, `BuildDSN`, `CheckServerDSN`, `Classify`, `Open`; `conn` with `Ping` and `Close` |
| `internal/engine/mongodb/errors.go` | Driver error → `qerr` |
| `internal/engine/mongodb/exec.go` | `Query` and `Explain`: run commands, enforce namespaces, flatten results |
| `internal/engine/mongodb/values.go` | BSON value → `engine.Value` text |
| `internal/engine/mongodb/introspect.go` | Catalog: listing, counts, indexes, sampling, inference, references |
| `internal/engine/mongodb/*_test.go`, `testdata/` | Tests, the fixture loader and golden files |
| `internal/format/schema.go` | Overview folds dotted fields into `parent{a,b,…}` |
| `internal/mcpserver/server.go` | The conditional MongoDB sentence |
| `cmd/qh/main.go` (or wherever engines are imported) | Blank import of the `mongodb` engine |
| `bench/harness/mongo.go` | Document datasets: the loader, the 1,000-collection generator and twin execution |
| `bench/harness/questions/{pagila,lendco}-docs.json` | Questions with `mongo` twins |
| `bench/harness/servers.go` | `StartMongoMCP` |
| `bench/harness/{runner,report,micro}.go`, `bench/cmd/bench/main.go` | A configurable rival, in place of hard-coded `dbhub` |

Execute tasks strictly in order. Each task ends green: `go vet ./... && go test ./... -count=1` (and `cd bench && go test ./... -short -count=1` for Tasks 8–9).

---
### Task 1: Engines classify their own queries

**Files:**
- Modify: `internal/engine/engine.go`
- Modify: `internal/engine/postgres/postgres.go`, `internal/engine/mysql/mysql.go`, `internal/engine/sqlite/sqlite.go`
- Modify: `internal/engine/enginetest/suite.go`
- Modify: `internal/core/service.go`
- Modify (test fakes): `internal/engine/engine_test.go`, `internal/core/connections_test.go`, `internal/core/open_test.go`
- Create: `internal/core/classify_test.go`

**Interfaces:**
- Produces:
  - `type engine.Access = sqlx.Access` and `engine.Read`, `engine.Write`, `engine.DDL`;
  - `Engine.Classify(q string) (engine.Access, error)`;
  - `Engine.ReferencedObjects(q string) []string`;
  - `engine.SQLEngine` (an `Engine` with `Dialect() sqlx.Dialect`);
  - `ConnSpec.Namespaces []string`;
  - `enginetest.CatalogHarness` and `enginetest.RunCatalog(t, h)`.
- Consumes: nothing new.

- [ ] **Step 1: Write the failing core test**

`internal/core/classify_test.go`:

```go
package core

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

// docEngine is a non-SQL engine: it classifies "write …" as a write and
// everything else as a read, and records what reaches its connection.
type docEngine struct{}

var (
	docQueries atomic.Int64
	docSpec    atomic.Pointer[engine.ConnSpec]
)

func (docEngine) Name() string                           { return "qhtest-doc" }
func (docEngine) Inspect(string) (engine.DSNInfo, error) { return engine.DSNInfo{Redacted: "doc://x"}, nil }
func (docEngine) Classify(q string) (engine.Access, error) {
	if strings.HasPrefix(q, "write") {
		return engine.Write, nil
	}
	return engine.Read, nil
}
func (docEngine) ReferencedObjects(string) []string { return nil }
func (docEngine) Open(_ context.Context, s engine.ConnSpec) (engine.Conn, error) {
	docSpec.Store(&s)
	return docConn{}, nil
}

type docConn struct{}

func (docConn) Query(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	docQueries.Add(1)
	return &engine.Result{Columns: []engine.Column{{Name: "x"}}, Rows: [][]engine.Value{{{Text: "1"}}}, RowsAffected: -1}, nil
}
func (docConn) Explain(context.Context, string, engine.QueryOpts) (*engine.Result, error) {
	return nil, nil
}
func (docConn) Introspect(context.Context) (*engine.Catalog, error) { return &engine.Catalog{}, nil }
func (docConn) Ping(context.Context) (string, error)                { return "doc 1", nil }
func (docConn) Close() error                                        { return nil }

func init() { engine.Register(docEngine{}) }

func TestEngineClassifiesItsQueries(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "doc", Engine: "qhtest-doc", DSN: "doc://x",
		Mode: engine.ModeRO, AllowedNamespaces: []string{"shop"}}, false); err != nil {
		t.Fatal(err)
	}
	before := docQueries.Load()
	_, err := h.svc.Query(ctx, ws, QueryInput{Source: "doc", Query: "write something"})
	if e := qerr.From(err); err == nil || e.Category != qerr.ReadOnly {
		t.Fatalf("write on a read-only doc connection: %v", err)
	}
	if docQueries.Load() != before {
		t.Fatal("the refused write reached the connection")
	}
	if _, err := h.svc.Query(ctx, ws, QueryInput{Source: "doc", Query: "db.x.find()"}); err != nil {
		t.Fatal(err)
	}
	if s := docSpec.Load(); s == nil || len(s.Namespaces) != 1 || s.Namespaces[0] != "shop" {
		t.Fatalf("ConnSpec.Namespaces = %+v", s)
	}
}
```

Check `ConnInput`'s field names (`grep -n "type ConnInput" -A30 internal/core/connections.go`) and adapt the literal if needed. `AllowedNamespaces` is a `[]string` there.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/core -run TestEngineClassifiesItsQueries -count=1`

Expected: a compile failure. `docEngine` does not implement `engine.Engine` (missing `Dialect`), and `ConnSpec` has no `Namespaces` field.

- [ ] **Step 3: Change the engine interface**

In `internal/engine/engine.go`, add after the `Mode` constants:

```go
// Access is what a query needs from the database.
type Access = sqlx.Access

// The access classes.
const (
	Read  = sqlx.Read
	Write = sqlx.Write
	DDL   = sqlx.DDL
)
```

Add to `ConnSpec`, after `Mode`:

```go
	// Namespaces are the connection's allowed namespaces; empty allows all.
	// Engines whose queries can name another namespace (MongoDB's
	// getSiblingDB) refuse the rest.
	Namespaces []string
```

Replace the `Engine` interface:

```go
// Engine is a database type.
type Engine interface {
	Name() string
	// Inspect parses dsn without connecting.
	Inspect(dsn string) (DSNInfo, error)
	Open(ctx context.Context, spec ConnSpec) (Conn, error)
	// Classify returns what q needs from the database, without connecting.
	// Its errors are *qerr.Error values (category invalid).
	Classify(q string) (Access, error)
	// ReferencedObjects names the tables or collections q reads, for
	// did-you-mean hints on unknown columns.
	ReferencedObjects(q string) []string
}

// SQLEngine is implemented by engines whose query language is SQL.
type SQLEngine interface {
	Engine
	Dialect() sqlx.Dialect
}
```

- [ ] **Step 4: Implement the two methods in the SQL engines**

In `internal/engine/postgres/postgres.go`, next to `Dialect`:

```go
func (Engine) Classify(q string) (engine.Access, error) { return sqlx.Classify(q, sqlx.Postgres) }
func (Engine) ReferencedObjects(q string) []string     { return sqlx.ReferencedTables(q, sqlx.Postgres) }
```

Make the same two changes in `internal/engine/mysql/mysql.go` (`sqlx.MySQL`; match the receiver style used there) and in `internal/engine/sqlite/sqlite.go` (`sqlx.SQLite`).

Add the two methods to each test fake that implements `engine.Engine`:
- `fakeEngine` in `internal/engine/engine_test.go`;
- `leakyEngine` in `internal/core/connections_test.go`;
- `leakEngine` in `internal/core/open_test.go`.

Each fake delegates to `sqlx` with `sqlx.Postgres`:

```go
func (leakyEngine) Classify(q string) (engine.Access, error) { return sqlx.Classify(q, sqlx.Postgres) }
func (leakyEngine) ReferencedObjects(q string) []string     { return sqlx.ReferencedTables(q, sqlx.Postgres) }
```

Keep their `Dialect` methods; they are harmless.

- [ ] **Step 5: Use them in core**

In `internal/core/service.go`, `runQuery`:

```go
	access, err := t.eng.Classify(in.Query)
	if err != nil {
		return nil, err
	}
	readOnly := t.spec.Mode != engine.ModeRW
	if !in.Explain && readOnly && access != engine.Read {
```

and later in the same function:

```go
	if access != engine.Read && !in.Explain {
		s.invalidate(ctx, t.rec.ID)
	}
```

In `withSuggestions`:

```go
		out.Hint = suggest.ColumnHint(cat, out.UnknownColumn, t.eng.ReferencedObjects(q))
```

In `connSpec`, set the new field:

```go
	spec := engine.ConnSpec{ID: id, Version: version, Engine: in.Engine, DSN: in.DSN, Mode: in.Mode, PoolSize: in.PoolSize,
		Namespaces: in.AllowedNamespaces,
		Dial: s.opts.Dial, TLS: in.TLS, Server: s.opts.Dial != nil, SSH: in.SSH, DSNRef: in.DSNRef, PasswordRef: in.PasswordRef,
		Resolve: s.refs.Resolve, CheckTarget: s.opts.CheckTarget}
```

Remove the `sqlx` import from `service.go` if nothing else in the file uses it.

- [ ] **Step 6: Split the catalog checks out of the engine suite**

In `internal/engine/enginetest/suite.go`, add:

```go
// CatalogHarness adapts an engine to the catalog checks, which document
// engines pass too.
type CatalogHarness struct {
	Engine engine.Engine
	// Spec opens a connection to a database loaded with the engine's fixture
	// (users, orders referencing users, order_items, the paid_orders view).
	Spec engine.ConnSpec
	// IDColumn is the key that orders.user_id references: "id" in SQL,
	// "_id" in MongoDB.
	IDColumn     string
	GoldenPrefix string
}

// RunCatalog checks Introspect against the fixture and the golden files.
func RunCatalog(t *testing.T, h CatalogHarness) {
	ctx := context.Background()
	c := open(t, h.Engine, h.Spec)
	cat, err := c.Introspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	orders, users, paid := one(t, cat, "orders"), one(t, cat, "users"), one(t, cat, "paid_orders")
	if !hasFK(orders, "user_id", "users", h.IDColumn) {
		t.Errorf("orders FKs: %+v", orders.ForeignKeys)
	}
	if len(users.ReferencedBy) == 0 || users.ReferencedBy[0].Object != "orders" {
		t.Errorf("users.ReferencedBy: %+v", users.ReferencedBy)
	}
	if paid.Kind != engine.KindView {
		t.Errorf("paid_orders kind: %v", paid.Kind)
	}
	if !hasUniqueIndex(users, "email") {
		t.Errorf("users indexes: %+v", users.Indexes)
	}
	overview, _ := format.Schema(cat, format.SchemaOpts{})
	golden(t, h.GoldenPrefix+"_overview.golden", overview)
	ordersOut, _ := format.Schema(cat, format.SchemaOpts{Object: "orders"})
	golden(t, h.GoldenPrefix+"_orders.golden", ordersOut)
	usersOut, _ := format.Schema(cat, format.SchemaOpts{Object: "users"})
	golden(t, h.GoldenPrefix+"_users.golden", usersOut)
}
```

Replace the body of `Run`'s `introspect` subtest with:

```go
	t.Run("introspect", func(t *testing.T) {
		RunCatalog(t, CatalogHarness{Engine: h.Engine, Spec: roSpec, IDColumn: "id", GoldenPrefix: h.GoldenPrefix})
		cat, err := ro.Introspect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if items := one(t, cat, "order_items"); !reflect.DeepEqual(items.PrimaryKey, []string{"order_id", "sku"}) {
			t.Errorf("order_items PK: %v", items.PrimaryKey)
		}
	})
```

Check that `open` (in the same file) registers a cleanup that closes the connection. If it doesn't, add `t.Cleanup(func() { c.Close() })` in `RunCatalog`.

- [ ] **Step 7: Run everything**

Run: `go vet ./... && go test ./... -count=1`

Expected: PASS. The SQL engine suites produce the same golden files, so `git status` shows no `testdata` changes.

- [ ] **Step 8: Commit**

```bash
git add internal
git commit -m "refactor(engine): engines classify their own queries

Core asks the engine for a query's access class and referenced objects
instead of calling sqlx, so a non-SQL engine can plug in. ConnSpec carries
the allowed namespaces, and the catalog checks of the engine suite are
callable on their own.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 2: Parse the mongosh subset

**Files:**
- Create: `internal/engine/mongodb/parse.go`
- Create: `internal/engine/mongodb/parse_test.go`
- Modify: `go.mod` and `go.sum` (add `go.mongodb.org/mongo-driver/v2`)

**Interfaces:**
- Produces, in package `mongodb`:

```go
// Call is one parsed mongosh call.
type Call struct {
	DB         string     // from getSiblingDB; "" means the connection's default database
	Collection string     // "" for database-level methods
	Method     string     // as written: "find", "getCollectionNames", ...
	Args       []any      // BSON-ready values (see value())
	Modifiers  []Modifier // chained cursor methods, in order
}

// Modifier is one chained cursor method, such as .sort({a: 1}).
type Modifier struct {
	Name string
	Args []any
}

func Parse(q string) (*Call, error) // errors are *qerr.Error, category invalid
```

  BSON-ready values are: `bson.D` (objects, keys in order), `bson.A`, `string`, `int32`, `int64`, `float64`, `bool`, `nil`, `bson.ObjectID`, `bson.DateTime`, `bson.Regex`, `bson.Decimal128`, `bson.Binary` (UUID, subtype 4), `bson.Timestamp`, `bson.MinKey` and `bson.MaxKey`.
- Consumes: nothing from earlier tasks.

The parser handles syntax only. Which methods exist, their arity and their access class are Task 3's job (`command.go`).

- [ ] **Step 1: Add the driver**

```bash
go get go.mongodb.org/mongo-driver/v2@latest
```

Check its API with `go doc go.mongodb.org/mongo-driver/v2/bson`. The code below uses the v2 names `bson.ObjectID`, `bson.ObjectIDFromHex`, `bson.NewDateTimeFromTime`, `bson.Regex`, `bson.ParseDecimal128`, `bson.Binary`, `bson.Timestamp`, `bson.MinKey`, `bson.MaxKey` and `bson.MarshalExtJSON`. If a name differs in the version you get, use the real one and note it in your report.

- [ ] **Step 2: Write the failing tests**

`internal/engine/mongodb/parse_test.go`:

```go
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
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/engine/mongodb -count=1`

Expected: FAIL, because `Parse` is undefined.

- [ ] **Step 4: Implement `parse.go`**

```go
// Package mongodb is the MongoDB engine. Queries are a fixed subset of mongosh
// syntax, parsed here into a Call; no JavaScript is ever executed.
package mongodb

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/qerr"
)

const (
	maxQueryBytes = 256 << 10
	maxDepth      = 100
)

// Call is one parsed mongosh call.
type Call struct {
	DB         string     // from getSiblingDB; "" means the connection's default database
	Collection string     // "" for database-level methods
	Method     string     // as written: "find", "getCollectionNames", ...
	Args       []any      // BSON-ready values
	Modifiers  []Modifier // chained cursor methods, in order
}

// Modifier is one chained cursor method, such as .sort({a: 1}).
type Modifier struct {
	Name string
	Args []any
}

// dbMethods are the methods called on db itself.
var dbMethods = map[string]bool{"getCollectionNames": true, "createCollection": true}

type parser struct {
	src   string
	pos   int
	depth int
}

// Parse parses one mongosh call. Errors are *qerr.Error values with category
// invalid; the hint shows the query line with a caret under the position.
func Parse(q string) (*Call, error) {
	if len(q) > maxQueryBytes {
		return nil, qerr.New(qerr.Invalid, "query is longer than 256 KiB")
	}
	p := &parser{src: q}
	c, err := p.call()
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.peek() == ';' {
		p.pos++
		p.skip()
	}
	if p.pos < len(p.src) {
		return nil, p.errAt(p.pos, "only one statement per query")
	}
	return c, nil
}

func (p *parser) call() (*Call, error) {
	p.skip()
	if !p.word("db") {
		return nil, p.errAt(p.pos, `expected "db" (queries look like db.<collection>.find({...}))`)
	}
	c := &Call{}
	var names []string
	for {
		p.skip()
		switch p.peek() {
		case '[':
			p.pos++
			p.skip()
			s, err := p.str()
			if err != nil {
				return nil, err
			}
			if err := p.expect(']'); err != nil {
				return nil, err
			}
			names = append(names, s)
			continue
		case '.':
			p.pos++
		default:
			if len(names) == 0 {
				return nil, p.unexpected()
			}
			return nil, p.errAt(p.pos, "expected a method call such as .find(...)")
		}
		p.skip()
		at := p.pos
		id, err := p.ident()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.peek() != '(' {
			names = append(names, id)
			continue
		}
		args, err := p.args()
		if err != nil {
			return nil, err
		}
		switch {
		case len(names) == 0 && id == "getSiblingDB":
			if c.DB != "" || len(args) != 1 {
				return nil, p.errAt(at, "getSiblingDB takes one database name")
			}
			s, ok := args[0].(string)
			if !ok || s == "" {
				return nil, p.errAt(at, "getSiblingDB takes one database name")
			}
			c.DB = s
			continue
		case len(names) == 0 && id == "getCollection":
			s, ok := oneString(args)
			if !ok {
				return nil, p.errAt(at, "getCollection takes one collection name")
			}
			names = append(names, s)
			continue
		case len(names) == 0 && !dbMethods[id]:
			return nil, p.errAt(at, fmt.Sprintf("db.%s is not supported; call methods on a collection, as in db.<collection>.%s(...)", id, id))
		}
		c.Collection, c.Method, c.Args = strings.Join(names, "."), id, args
		break
	}
	for {
		p.skip()
		if p.peek() != '.' {
			return c, nil
		}
		p.pos++
		p.skip()
		id, err := p.ident()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.peek() != '(' {
			return nil, p.errAt(p.pos, fmt.Sprintf("expected ( after .%s", id))
		}
		args, err := p.args()
		if err != nil {
			return nil, err
		}
		c.Modifiers = append(c.Modifiers, Modifier{Name: id, Args: args})
	}
}

func oneString(args []any) (string, bool) {
	if len(args) != 1 {
		return "", false
	}
	s, ok := args[0].(string)
	return s, ok && s != ""
}

// args parses "(v, v, ...)" with an optional trailing comma.
func (p *parser) args() ([]any, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	out := []any{}
	for {
		p.skip()
		if p.peek() == ')' {
			p.pos++
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skip()
		switch p.peek() {
		case ',':
			p.pos++
		case ')':
		default:
			return nil, p.unexpected()
		}
	}
}

func (p *parser) value() (any, error) {
	p.skip()
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxDepth {
		return nil, p.errAt(p.pos, "nested more than 100 levels")
	}
	switch ch := p.peek(); {
	case ch == '{':
		return p.object()
	case ch == '[':
		return p.array()
	case ch == '"' || ch == '\'':
		return p.str()
	case ch == '/':
		return p.regex()
	case ch == '-' || ch == '.' || ch >= '0' && ch <= '9':
		return p.number()
	case isIdentStart(ch):
		return p.word2value()
	}
	return nil, p.unexpected()
}

func (p *parser) object() (bson.D, error) {
	p.pos++ // {
	d := bson.D{}
	for {
		p.skip()
		switch ch := p.peek(); {
		case ch == '}':
			p.pos++
			return d, nil
		case ch == '"' || ch == '\'':
		case isIdentStart(ch) || ch >= '0' && ch <= '9':
		default:
			return nil, p.unexpected()
		}
		var key string
		var err error
		if ch := p.peek(); ch == '"' || ch == '\'' {
			key, err = p.str()
		} else {
			key, err = p.keyWord()
		}
		if err != nil {
			return nil, err
		}
		if err := p.expect(':'); err != nil {
			return nil, err
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		if strings.ContainsRune(key, 0) {
			return nil, p.errAt(p.pos, "object keys cannot contain a NUL character")
		}
		d = append(d, bson.E{Key: key, Value: v})
		p.skip()
		switch p.peek() {
		case ',':
			p.pos++
		case '}':
		default:
			return nil, p.unexpected()
		}
	}
}

func (p *parser) array() (bson.A, error) {
	p.pos++ // [
	a := bson.A{}
	for {
		p.skip()
		if p.peek() == ']' {
			p.pos++
			return a, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		p.skip()
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
		default:
			return nil, p.unexpected()
		}
	}
}

func (p *parser) str() (string, error) {
	start := p.pos
	quote := p.peek()
	if quote != '"' && quote != '\'' {
		return "", p.unexpected()
	}
	p.pos++
	var b strings.Builder
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		switch {
		case ch == quote:
			p.pos++
			return b.String(), nil
		case ch == '\n':
			return "", p.errAt(start, "unterminated string")
		case ch == '\\':
			if p.pos+1 >= len(p.src) {
				return "", p.errAt(start, "unterminated string")
			}
			esc := p.src[p.pos+1]
			p.pos += 2
			switch esc {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case '0':
				b.WriteByte(0)
			case 'u':
				if p.pos+4 > len(p.src) {
					return "", p.errAt(p.pos-2, `invalid \u escape`)
				}
				n, err := strconv.ParseUint(p.src[p.pos:p.pos+4], 16, 32)
				if err != nil {
					return "", p.errAt(p.pos-2, `invalid \u escape`)
				}
				b.WriteRune(rune(n))
				p.pos += 4
			default: // \\ \' \" \/ and any other character stand for themselves
				b.WriteByte(esc)
			}
		default:
			b.WriteByte(ch)
			p.pos++
		}
	}
	return "", p.errAt(start, "unterminated string")
}

func (p *parser) number() (any, error) {
	start := p.pos
	if p.peek() == '-' {
		p.pos++
	}
	isFloat := false
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		switch {
		case ch >= '0' && ch <= '9':
		case ch == '.' || ch == 'e' || ch == 'E':
			isFloat = true
		case (ch == '+' || ch == '-') && (p.src[p.pos-1] == 'e' || p.src[p.pos-1] == 'E'):
		default:
			goto done
		}
		p.pos++
	}
done:
	text := p.src[start:p.pos]
	if !isFloat {
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			if n >= math.MinInt32 && n <= math.MaxInt32 {
				return int32(n), nil
			}
			return n, nil
		}
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || text == "-" {
		return nil, p.errAt(start, fmt.Sprintf("invalid number %q", text))
	}
	return f, nil
}

func (p *parser) regex() (bson.Regex, error) {
	start := p.pos
	p.pos++ // /
	inClass := false
	var b strings.Builder
	for {
		if p.pos >= len(p.src) || p.src[p.pos] == '\n' {
			return bson.Regex{}, p.errAt(start, "unterminated regex")
		}
		ch := p.src[p.pos]
		if ch == '\\' && p.pos+1 < len(p.src) {
			b.WriteString(p.src[p.pos : p.pos+2])
			p.pos += 2
			continue
		}
		p.pos++
		if ch == '/' && !inClass {
			break
		}
		if ch == '[' {
			inClass = true
		} else if ch == ']' {
			inClass = false
		}
		b.WriteByte(ch)
	}
	if strings.ContainsRune(b.String(), 0) {
		return bson.Regex{}, p.errAt(start, "a regex cannot contain a NUL character")
	}
	flagsAt := p.pos
	for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
		p.pos++
	}
	flags := p.src[flagsAt:p.pos]
	for _, f := range flags {
		if !strings.ContainsRune("imsxu", f) {
			return bson.Regex{}, p.errAt(flagsAt, fmt.Sprintf("unsupported regex flag %q (MongoDB accepts i, m, s, x, u)", string(f)))
		}
	}
	return bson.Regex{Pattern: b.String(), Options: sortFlags(flags)}, nil
}

// sortFlags orders regex flags alphabetically, as BSON requires.
func sortFlags(f string) string {
	out := []byte(f)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return string(out)
}

// word2value parses a bare word in value position: a literal or a helper call.
func (p *parser) word2value() (any, error) {
	at := p.pos
	w, err := p.ident()
	if err != nil {
		return nil, err
	}
	switch w {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null", "undefined":
		return nil, nil
	case "new":
		p.skip()
		at = p.pos
		if w, err = p.ident(); err != nil {
			return nil, err
		}
		if w != "Date" && w != "ObjectId" && w != "NumberDecimal" && w != "NumberLong" && w != "NumberInt" && w != "UUID" && w != "Timestamp" {
			return nil, p.errAt(at, fmt.Sprintf("unsupported expression \"new %s\"", w))
		}
	}
	p.skip()
	if p.peek() != '(' {
		return nil, p.errAt(at, fmt.Sprintf("unsupported expression %q (only literals and ObjectId(), ISODate(), NumberLong() and similar helpers are allowed)", w))
	}
	args, err := p.args()
	if err != nil {
		return nil, err
	}
	return p.helper(at, w, args)
}

func (p *parser) helper(at int, name string, args []any) (any, error) {
	str := func() (string, bool) {
		if len(args) != 1 {
			return "", false
		}
		s, ok := args[0].(string)
		return s, ok
	}
	switch name {
	case "ObjectId":
		s, ok := str()
		if !ok {
			return nil, p.errAt(at, "ObjectId() needs a 24-character hex string")
		}
		id, err := bson.ObjectIDFromHex(s)
		if err != nil {
			return nil, p.errAt(at, fmt.Sprintf("invalid ObjectId %q", s))
		}
		return id, nil
	case "ISODate", "Date":
		if len(args) == 0 {
			return nil, p.errAt(at, "new Date() needs an argument (qh does not evaluate the current time)")
		}
		if len(args) == 1 {
			switch v := args[0].(type) {
			case string:
				t, err := parseDate(v)
				if err != nil {
					return nil, p.errAt(at, fmt.Sprintf("invalid date %q", v))
				}
				return bson.NewDateTimeFromTime(t), nil
			case int32:
				return bson.DateTime(int64(v)), nil
			case int64:
				return bson.DateTime(v), nil
			case float64:
				return bson.DateTime(int64(v)), nil
			}
		}
		return nil, p.errAt(at, name+"() takes one date string or a number of milliseconds")
	case "NumberInt", "NumberLong":
		var n int64
		var err error
		switch v := one(args).(type) {
		case int32:
			n = int64(v)
		case int64:
			n = v
		case string:
			n, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		default:
			err = fmt.Errorf("bad")
		}
		if err != nil || len(args) != 1 {
			return nil, p.errAt(at, name+"() takes one integer")
		}
		if name == "NumberInt" {
			if n < math.MinInt32 || n > math.MaxInt32 {
				return nil, p.errAt(at, "NumberInt() is out of range")
			}
			return int32(n), nil
		}
		return n, nil
	case "NumberDecimal":
		var text string
		switch v := one(args).(type) {
		case string:
			text = v
		case int32, int64, float64:
			text = fmt.Sprint(v)
		}
		d, err := bson.ParseDecimal128(text)
		if err != nil || len(args) != 1 {
			return nil, p.errAt(at, "NumberDecimal() takes one number as a string")
		}
		return d, nil
	case "UUID":
		s, ok := str()
		raw, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
		if !ok || err != nil || len(raw) != 16 {
			return nil, p.errAt(at, "UUID() takes one UUID string")
		}
		return bson.Binary{Subtype: 4, Data: raw}, nil
	case "Timestamp":
		if len(args) == 2 {
			t, ok1 := asUint32(args[0])
			i, ok2 := asUint32(args[1])
			if ok1 && ok2 {
				return bson.Timestamp{T: t, I: i}, nil
			}
		}
		return nil, p.errAt(at, "Timestamp() takes two integers (seconds, increment)")
	case "MinKey", "MaxKey":
		if len(args) != 0 {
			return nil, p.errAt(at, name+"() takes no arguments")
		}
		if name == "MinKey" {
			return bson.MinKey{}, nil
		}
		return bson.MaxKey{}, nil
	}
	return nil, p.errAt(at, fmt.Sprintf("unsupported expression %q (only literals and ObjectId(), ISODate(), NumberLong() and similar helpers are allowed)", name))
}

func one(args []any) any {
	if len(args) != 1 {
		return nil
	}
	return args[0]
}

func asUint32(v any) (uint32, bool) {
	var n int64
	switch x := v.(type) {
	case int32:
		n = int64(x)
	case int64:
		n = x
	default:
		return 0, false
	}
	if n < 0 || n > math.MaxUint32 {
		return 0, false
	}
	return uint32(n), true
}

var dateLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"}

// parseDate accepts the forms mongosh's ISODate accepts; times without a
// zone are UTC.
func parseDate(s string) (time.Time, error) {
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("bad date")
}

func (p *parser) ident() (string, error) {
	start := p.pos
	if p.pos >= len(p.src) || !isIdentStart(p.src[p.pos]) {
		return "", p.unexpected()
	}
	for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
		p.pos++
	}
	return p.src[start:p.pos], nil
}

// keyWord is an unquoted object key: an identifier, or digits.
func (p *parser) keyWord() (string, error) {
	start := p.pos
	for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
		p.pos++
	}
	if p.pos == start {
		return "", p.unexpected()
	}
	return p.src[start:p.pos], nil
}

// word consumes w if the input continues with it as a whole word.
func (p *parser) word(w string) bool {
	if strings.HasPrefix(p.src[p.pos:], w) && (p.pos+len(w) == len(p.src) || !isIdentPart(p.src[p.pos+len(w)])) {
		p.pos += len(w)
		return true
	}
	return false
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentPart(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

func (p *parser) peek() byte {
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) expect(c byte) error {
	p.skip()
	if p.peek() != c {
		return p.unexpected()
	}
	p.pos++
	return nil
}

// skip skips whitespace and comments.
func (p *parser) skip() {
	for p.pos < len(p.src) {
		switch {
		case strings.ContainsRune(" \t\r\n", rune(p.src[p.pos])):
			p.pos++
		case strings.HasPrefix(p.src[p.pos:], "//"):
			if i := strings.IndexByte(p.src[p.pos:], '\n'); i >= 0 {
				p.pos += i + 1
			} else {
				p.pos = len(p.src)
			}
		case strings.HasPrefix(p.src[p.pos:], "/*"):
			if i := strings.Index(p.src[p.pos+2:], "*/"); i >= 0 {
				p.pos += i + 4
			} else {
				p.pos = len(p.src)
			}
		default:
			return
		}
	}
}

func (p *parser) unexpected() error {
	if p.pos >= len(p.src) {
		return p.errAt(p.pos, "unexpected end of query")
	}
	r, _ := utf8.DecodeRuneInString(p.src[p.pos:])
	return p.errAt(p.pos, fmt.Sprintf("unexpected %q", string(r)))
}

// errAt reports msg at byte offset pos as "msg at line:col", with the query
// line and a caret in the hint.
func (p *parser) errAt(pos int, msg string) error {
	pos = min(pos, len(p.src))
	lineStart := strings.LastIndexByte(p.src[:pos], '\n') + 1
	lineEnd := strings.IndexByte(p.src[pos:], '\n')
	if lineEnd < 0 {
		lineEnd = len(p.src)
	} else {
		lineEnd += pos
	}
	line := strings.Count(p.src[:pos], "\n") + 1
	col := utf8.RuneCountInString(p.src[lineStart:pos]) + 1
	text := p.src[lineStart:lineEnd]
	if len(text) > 200 { // keep the hint short around the position
		from := max(0, pos-lineStart-100)
		text = text[from:min(len(text), from+200)]
		col -= utf8.RuneCountInString(p.src[lineStart : lineStart+from])
	}
	e := qerr.New(qerr.Invalid, fmt.Sprintf("%s at %d:%d", msg, line, col))
	return e.WithHint(text + "\n" + strings.Repeat(" ", col-1) + "^")
}
```

Notes for the implementer:
- `unexpected end of query at 1:22` in the test: the position is the end of input (byte 21, column 22).
- Strings for error messages use Go's `%q`, so a backtick prints as "`".
- The `object` function checks the key's first character before reading it. Keep that check, so `{1: 2}` and `{"a": 1}` work and `{+: 1}` fails with `unexpected "+"`.
- If a test expectation and the code disagree on a column number, check the column by hand and fix whichever is wrong. Note it in your report.

- [ ] **Step 5: Run the tests and a short fuzz**

Run: `go test ./internal/engine/mongodb -count=1 && go test ./internal/engine/mongodb -run '^$' -fuzz FuzzParse -fuzztime 30s`

Expected: PASS, and the fuzzer finds no failures. If it finds a crash, fix the parser and add the input to `TestParseErrors` or `TestParse`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/engine/mongodb
git commit -m "feat(mongodb): parse a safe subset of mongosh syntax

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 3: Classify calls and translate them into database commands

**Files:**
- Create: `internal/engine/mongodb/command.go`
- Create: `internal/engine/mongodb/command_test.go`

**Interfaces:**
- Consumes: `Parse`, `Call` and `Modifier` (Task 2); `engine.Access`, `engine.Read`, `engine.Write` and `engine.DDL` (Task 1).
- Produces, in package `mongodb`:

```go
type opKind int // how exec runs an Op and shapes its result

const (
	kDocs        opKind = iota + 1 // cursor of documents (find, findOne, aggregate, getIndexes)
	kNames                         // cursor of listCollections results: one "name" column
	kCount                         // count command: reply field n becomes column "count"
	kAggCount                      // aggregate ending in {$group: {_id: 1, n: {$sum: 1}}}: column "count", 0 when empty
	kDistinct                      // distinct: reply field values, one column named after the key
	kInsert                        // insert: RowsAffected = n, rows = the inserted _id values
	kUpdate                        // update: RowsAffected = nModified
	kDelete                        // delete: RowsAffected = n
	kFindAndModify                 // findAndModify: reply field value is the one document
	kAdmin                         // createIndexes, dropIndexes, create, drop, renameCollection
)

// Op is a Call translated into one database command.
type Op struct {
	Access     engine.Access
	Kind       opKind
	DB         string   // the database the command runs on ("admin" for renameCollection)
	Collection string   // "" for database-level calls
	Command    bson.D   // without maxTimeMS; exec adds it
	Explain    bool     // run as {explain: Command, verbosity: "queryPlanner"}
	Key        string   // kDistinct: the field name
	IDs        []any    // kInsert: the _id of each inserted document
	IndexNames []string // kAdmin createIndex(es): the index names
}

// classify checks c's method, arguments and modifiers and returns its access class.
func classify(c *Call) (engine.Access, error)

// translate builds the command for c on database db. maxRows is the row
// cap; read commands fetch maxRows+1 so truncation can be detected.
func translate(c *Call, db string, maxRows int) (*Op, error)

// explainCommand wraps op's command for the explain path.
func (op *Op) explainCommand() bson.D
```

- [ ] **Step 1: Write the failing tests**

`internal/engine/mongodb/command_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/engine/mongodb -run 'Translate|Explain|Insert|Classify' -count=1`

Expected: FAIL, because `translate` and the rest are undefined.

- [ ] **Step 3: Implement `command.go`**

```go
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
	access     engine.Access
	minArgs    int
	maxArgs    int
	modifiers  string // space-separated names
	explain    bool
	dbLevel    bool
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
	if !ok || m.dbLevel != (c.Collection == "") {
		e := qerr.New(qerr.Invalid, fmt.Sprintf("unsupported method %q", c.Method))
		if s := closest(c.Method, methodNames()); s != "" {
			e = e.WithHint("did you mean " + s + "?")
		}
		return 0, e
	}
	if len(c.Args) < m.minArgs || len(c.Args) > m.maxArgs {
		if len(c.Args) > m.maxArgs {
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
			if v, ok := lookup(opts, "name"); ok && len(keys) == 1 {
				if s, ok := v.(string); ok {
					name = s
				}
			}
			spec := bson.D{{Key: "key", Value: k}, {Key: "name", Value: name}}
			for _, e := range opts {
				if e.Key != "name" {
					spec = append(spec, e)
				}
			}
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
			cmd = append(cmd, opts...)
		}
		op.Kind, op.Collection, op.Command = kAdmin, name, cmd
	}
	return op, nil
}

func (op *Op) explainCommand() bson.D {
	return bson.D{{Key: "explain", Value: op.Command}, {Key: "verbosity", Value: "queryPlanner"}}
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
```

Notes for the implementer:
- `insertOne` options (the second argument) are accepted and passed on through `pick`.
- `relaxed` Extended JSON prints `int64` values that fit in a double as plain numbers, so `"limit":101` matches the `int64` limit.
- If a test expectation differs from what the server needs, follow the server and fix the test. For example, the server may require `"limit": 0` as an int32 in deletes. Note each case in your report.
- `bson.NewObjectID` is the v2 name. Check it with `go doc`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/engine/mongodb -count=1`

Expected: PASS, including Task 2's tests.

- [ ] **Step 5: Commit**

```bash
git add internal/engine/mongodb
git commit -m "feat(mongodb): classify calls and translate them into database commands

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 4: The engine: connection strings, server-mode checks and connecting

**Files:**
- Create: `internal/engine/mongodb/mongodb.go`
- Create: `internal/engine/mongodb/errors.go`
- Create: `internal/engine/mongodb/mongodb_test.go`, for unit tests that need no Docker
- Create: `internal/engine/mongodb/server_test.go`, for the container helpers, the fixture and the connection tests
- Modify: `internal/cli/engines.go`, to blank-import the engine
- Modify: `go.mod` and `go.sum`, to add `github.com/testcontainers/testcontainers-go/modules/mongodb` at the version the other testcontainers modules use

**Interfaces:**
- Consumes:
  - `classify`, `translate`, `Parse` (Tasks 2–3);
  - `engine.Register`, `engine.BuildURL`, `engine.DSNFields`, `engine.TLS.Config`, `engine.ConnSpec` (`Dial`, `TLS`, `Password`, `Server`, `Namespaces`, `PoolSize`, `Mode`).
- Produces:
  - `mongodb.Engine` (registered as `"mongodb"`), with `Inspect`, `BuildDSN`, `CheckServerDSN`, `Classify`, `ReferencedObjects` and `Open`;
  - `type conn struct { client *mongo.Client; defaultDB string; pinned bool; namespaces []string; readOnly bool; … }`, with `Ping` and `Close`. `Query`, `Explain` and `Introspect` are stubbed here and implemented in Tasks 5–6;
  - `mapErr(err error) error`, which turns driver errors into `qerr`;
  - test helpers, used by Tasks 5–6:
    - `mongoURI(t, image string) string`: the base URI of a shared container with auth;
    - `loadFixture(t, uri string) (db string)` and `loadFixtureNamed(t, uri, db string) string`;
    - `openSpec(t, spec engine.ConnSpec) engine.Conn`;
    - `fixtureSpec(t, image string, mode engine.Mode) engine.ConnSpec`.

- [ ] **Step 1: Write the failing unit tests (no Docker)**

`internal/engine/mongodb/mongodb_test.go`:

```go
package mongodb

import (
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func TestInspect(t *testing.T) {
	info, err := Engine{}.Inspect("mongodb://bob:s3cret@h1:27017,h2:27018/shop?replicaSet=rs0&authSource=admin")
	if err != nil {
		t.Fatal(err)
	}
	if info.Host != "h1,h2" || info.Port != 27017 || info.Database != "shop" || info.User != "bob" || info.Password != "s3cret" {
		t.Fatalf("info = %+v", info)
	}
	if info.Redacted != "mongodb://bob:***@h1:27017,h2:27018/shop?replicaSet=rs0&authSource=admin" {
		t.Fatalf("redacted = %q", info.Redacted)
	}
	info, err = Engine{}.Inspect("mongodb+srv://u:p%40ss@cluster0.example.net/app")
	if err != nil || info.Host != "cluster0.example.net" || info.Port != 0 || info.Password != "p@ss" || info.Database != "app" {
		t.Fatalf("srv: %+v %v", info, err)
	}
	info, err = Engine{}.Inspect("mongodb://localhost")
	if err != nil || info.Port != 27017 || info.Database != "" || info.Redacted != "mongodb://localhost" {
		t.Fatalf("plain: %+v %v", info, err)
	}
	for _, bad := range []string{"postgres://x/y", "mongodb://", "mongodb://h:notaport/x", "mongodb://h/x?%zz"} {
		if _, err := (Engine{}).Inspect(bad); err == nil || qerr.From(err).Category != qerr.Invalid || strings.Contains(err.Error(), bad) {
			t.Errorf("Inspect(%q) = %v", bad, err)
		}
	}
}

func TestCheckServerDSN(t *testing.T) {
	ok := []string{"mongodb://u:p@h/db?tls=true&authSource=admin", "mongodb+srv://u:p@c.example.net/db?retryWrites=true"}
	for _, d := range ok {
		if err := (Engine{}).CheckServerDSN(d); err != nil {
			t.Errorf("CheckServerDSN(%q) = %v", d, err)
		}
	}
	refused := []string{
		"mongodb://h/db?tlsCAFile=/etc/ssl/ca.pem",
		"mongodb://h/db?TLSCertificateKeyFile=/k.pem",
		"mongodb://h/db?tlsCertificateKeyFilePassword=x",
		"mongodb://h/db?tlsInsecure=true",
		"mongodb://h/db?authMechanism=MONGODB-AWS",
		"mongodb://u@h/db?authMechanism=GSSAPI",
		"mongodb://h/db?authMechanism=mongodb-oidc",
	}
	for _, d := range refused {
		err := (Engine{}).CheckServerDSN(d)
		if err == nil || qerr.From(err).Category != qerr.Invalid || strings.Contains(err.Error(), "/etc/ssl") || strings.Contains(err.Error(), d) {
			t.Errorf("CheckServerDSN(%q) = %v", d, err)
		}
	}
}

func TestBuildDSN(t *testing.T) {
	got, err := Engine{}.BuildDSN(engine.DSNFields{Host: "db.example.com", Port: 27017, User: "app", Password: "p w", Database: "shop"})
	if err != nil || !strings.HasPrefix(got, "mongodb://app:") || !strings.HasSuffix(got, "@db.example.com:27017/shop") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestEngineClassify(t *testing.T) {
	for q, want := range map[string]engine.Access{
		`db.a.find()`:                     engine.Read,
		`db.a.deleteMany({})`:             engine.Write,
		`db.a.createIndex({x: 1})`:        engine.DDL,
		`db.a.aggregate([{$merge: "b"}])`: engine.Write,
	} {
		if got, err := (Engine{}).Classify(q); err != nil || got != want {
			t.Errorf("Classify(%q) = %v %v", q, got, err)
		}
	}
	if _, err := (Engine{}).Classify(`db.a.findd()`); qerr.From(err).Category != qerr.Invalid {
		t.Errorf("bad method: %v", err)
	}
	if got := (Engine{}).ReferencedObjects(`db.orders.find({a: 1})`); len(got) != 1 || got[0] != "orders" {
		t.Errorf("ReferencedObjects = %v", got)
	}
}
```

- [ ] **Step 2: Write the failing container tests**

`internal/engine/mongodb/server_test.go`:

```go
package mongodb

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongodb "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

const testPassword = "mongo-secret-pw"

var (
	serversMu sync.Mutex
	servers   = map[string]string{} // image → base URI; one container per image per test binary
	dbSeq     atomic.Int64
)

// mongoURI returns mongodb://root:<pw>@host:port/?authSource=admin for a
// shared container running image. Containers stop when the process exits
// (testcontainers' reaper).
func mongoURI(t *testing.T, image string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	serversMu.Lock()
	defer serversMu.Unlock()
	if u, ok := servers[image]; ok {
		return u
	}
	ctr, err := tcmongodb.Run(context.Background(), image, tcmongodb.WithUsername("root"), tcmongodb.WithPassword(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	u, err := ctr.ConnectionString(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	pu.Path = "/"
	q := pu.Query()
	q.Set("authSource", "admin")
	pu.RawQuery = q.Encode()
	servers[image] = pu.String()
	return servers[image]
}

// withDB returns uri with db as its path.
func withDB(uri, db string) string {
	u, _ := url.Parse(uri)
	u.Path = "/" + db
	return u.String()
}

// loadFixture creates a fresh database with the shared fixture, mirroring
// testutil.SQLiteFixtureStatements: users (unique email), orders (250, with
// user_id), order_items (20, with order_id) and the paid_orders view.
func loadFixture(t *testing.T, uri string) string {
	t.Helper()
	return loadFixtureNamed(t, uri, fmt.Sprintf("fx_%d_%d", time.Now().UnixNano()%1e6, dbSeq.Add(1)))
}

// loadFixtureNamed loads the fixture into database db, dropping it first.
// Golden-file tests use a fixed name so the output is stable.
func loadFixtureNamed(t *testing.T, uri, db string) string {
	t.Helper()
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	d := client.Database(db)
	if err := d.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2025, 3, 14, 10, 22, 31, 0, time.UTC)
	users := []any{
		bson.D{{Key: "_id", Value: int32(1)}, {Key: "email", Value: "alice@example.com"}, {Key: "name", Value: "Alice"},
			{Key: "bio", Value: "line1\nline2\tx"}, {Key: "avatar", Value: bson.Binary{Data: []byte{0xde, 0xad, 0xbe, 0xef}}},
			{Key: "created_at", Value: bson.NewDateTimeFromTime(created)}},
		bson.D{{Key: "_id", Value: int32(2)}, {Key: "email", Value: "zoe@example.com"}, {Key: "name", Value: "Zoë 🚀"}},
		bson.D{{Key: "_id", Value: int32(3)}, {Key: "email", Value: "nobody@example.com"}, {Key: "name", Value: nil}},
	}
	if _, err := d.Collection("users").InsertMany(ctx, users); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Collection("users").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "email", Value: 1}}, Options: options.Index().SetUnique(true)}); err != nil {
		t.Fatal(err)
	}
	var orders []any
	for i := 1; i <= 250; i++ {
		status := "pending"
		if i%2 == 0 {
			status = "paid"
		}
		orders = append(orders, bson.D{{Key: "_id", Value: int32(i)}, {Key: "user_id", Value: int32(i%3 + 1)},
			{Key: "status", Value: status}, {Key: "total", Value: float64(i) * 1.5}})
	}
	if _, err := d.Collection("orders").InsertMany(ctx, orders); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Collection("orders").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}}}); err != nil {
		t.Fatal(err)
	}
	var items []any
	for i := 1; i <= 10; i++ {
		for k, sku := range []string{"sku-1", "sku-2"} {
			items = append(items, bson.D{{Key: "_id", Value: fmt.Sprintf("%d-%s", i, sku)}, {Key: "order_id", Value: int32(i)},
				{Key: "sku", Value: sku}, {Key: "qty", Value: int32(k + 1)}})
		}
	}
	if _, err := d.Collection("order_items").InsertMany(ctx, items); err != nil {
		t.Fatal(err)
	}
	view := bson.A{bson.D{{Key: "$match", Value: bson.D{{Key: "status", Value: "paid"}}}},
		bson.D{{Key: "$project", Value: bson.D{{Key: "user_id", Value: 1}, {Key: "total", Value: 1}}}}}
	if err := d.CreateView(ctx, "paid_orders", "orders", view); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := mongo.Connect(options.Client().ApplyURI(uri))
		if err == nil {
			_ = c.Database(db).Drop(context.Background())
			_ = c.Disconnect(context.Background())
		}
	})
	return db
}

// fixtureSpec returns a spec for a fresh fixture database on image.
func fixtureSpec(t *testing.T, image string, mode engine.Mode) engine.ConnSpec {
	t.Helper()
	uri := mongoURI(t, image)
	db := loadFixture(t, uri)
	return engine.ConnSpec{ID: "fx", Version: 1, Engine: "mongodb", DSN: withDB(uri, db), Mode: mode, PoolSize: 2}
}

func openSpec(t *testing.T, spec engine.ConnSpec) engine.Conn {
	t.Helper()
	c, err := Engine{}.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestOpenAndPing(t *testing.T) {
	for _, image := range []string{"mongo:7", "mongo:8"} {
		t.Run(image, func(t *testing.T) {
			c := openSpec(t, fixtureSpec(t, image, engine.ModeRO))
			v, err := c.Ping(context.Background())
			if err != nil || !strings.HasPrefix(v, "MongoDB "+strings.TrimPrefix(image, "mongo:")) {
				t.Fatalf("Ping = %q, %v", v, err)
			}
		})
	}
}

func TestPasswordReplacesAndIsNeverShown(t *testing.T) {
	uri := mongoURI(t, "mongo:8")
	noPW := strings.Replace(uri, ":"+testPassword+"@", "@", 1)
	c := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: noPW, Password: testPassword, Mode: engine.ModeRO})
	if _, err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{Engine: "mongodb", DSN: noPW + "&serverSelectionTimeoutMS=3000", Password: "wrong-pw-xyz", Mode: engine.ModeRO})
	if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed || strings.Contains(err.Error(), "wrong-pw-xyz") {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestDialerSeesEveryHost(t *testing.T) {
	uri := mongoURI(t, "mongo:8")
	u, _ := url.Parse(uri)
	var mu sync.Mutex
	seen := map[string]bool{}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		seen[addr] = true
		mu.Unlock()
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	two := strings.Replace(uri, u.Host, u.Host+",127.0.0.1:1", 1) + "&serverSelectionTimeoutMS=2000"
	c, err := Engine{}.Open(context.Background(), engine.ConnSpec{Engine: "mongodb", DSN: two, Dial: dial, Mode: engine.ModeRO})
	if err == nil {
		c.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen[u.Host] || !seen["127.0.0.1:1"] {
		t.Fatalf("dialled %v, want both %s and 127.0.0.1:1", seen, u.Host)
	}
}

func TestTLSSettingsApply(t *testing.T) {
	uri := mongoURI(t, "mongo:8")
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{Engine: "mongodb", DSN: uri + "&serverSelectionTimeoutMS=2000",
		TLS: &engine.TLS{Mode: engine.TLSRequire}, Mode: engine.ModeRO})
	if e := qerr.From(err); err == nil || e.Category != qerr.ConnectionFailed {
		t.Fatalf("TLS against a plain server: %v", err)
	}
}

func TestServerModeRefusesFileOptions(t *testing.T) {
	_, err := Engine{}.Open(context.Background(), engine.ConnSpec{Engine: "mongodb", DSN: "mongodb://h/db?tlsCAFile=/etc/passwd", Server: true})
	if e := qerr.From(err); err == nil || e.Category != qerr.Invalid {
		t.Fatalf("server mode with tlsCAFile: %v", err)
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/engine/mongodb -count=1`

Expected: a compile failure. `Engine` and `Open` are undefined.

- [ ] **Step 4: Implement `mongodb.go`**

```go
package mongodb

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func init() { engine.Register(Engine{}) }

// Engine is the MongoDB engine.
type Engine struct{}

func (Engine) Name() string { return "mongodb" }

// BuildDSN implements engine.DSNBuilder.
func (Engine) BuildDSN(f engine.DSNFields) (string, error) { return engine.BuildURL("mongodb", f) }

func (Engine) Classify(q string) (engine.Access, error) {
	c, err := Parse(q)
	if err != nil {
		return 0, err
	}
	return classify(c)
}

func (Engine) ReferencedObjects(q string) []string {
	c, err := Parse(q)
	if err != nil || c.Collection == "" {
		return nil
	}
	return []string{c.Collection}
}

// uri is a parsed mongodb:// or mongodb+srv:// connection string. net/url
// cannot parse a host list, so the parts are split by hand.
type uri struct {
	srv            bool
	user, password string
	hasPassword    bool
	hosts          []string // host or host:port
	db             string
	query          url.Values
	rawQuery       string
}

func badURI() error {
	return qerr.New(qerr.Invalid, "invalid mongodb connection string").
		WithHint("Use mongodb://user:password@host:27017/database or mongodb+srv://user:password@cluster.example.net/database")
}

func parseURI(dsn string) (*uri, error) {
	u := &uri{}
	rest, ok := strings.CutPrefix(dsn, "mongodb://")
	if !ok {
		if rest, ok = strings.CutPrefix(dsn, "mongodb+srv://"); !ok {
			return nil, badURI()
		}
		u.srv = true
	}
	hostEnd := strings.IndexAny(rest, "/?")
	authority, tail := rest, ""
	if hostEnd >= 0 {
		authority, tail = rest[:hostEnd], rest[hostEnd:]
	}
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		userinfo := authority[:at]
		authority = authority[at+1:]
		name, pw, has := strings.Cut(userinfo, ":")
		var err error
		if u.user, err = url.PathUnescape(name); err != nil {
			return nil, badURI()
		}
		if has {
			if u.password, err = url.PathUnescape(pw); err != nil {
				return nil, badURI()
			}
			u.hasPassword = true
		}
	}
	if authority == "" {
		return nil, badURI()
	}
	for _, h := range strings.Split(authority, ",") {
		if h == "" {
			return nil, badURI()
		}
		if _, port, err := net.SplitHostPort(h); err == nil {
			if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 || u.srv {
				return nil, badURI()
			}
		} else if strings.Contains(strings.TrimSuffix(strings.TrimPrefix(h, "["), "]"), ":") && !strings.HasPrefix(h, "[") {
			return nil, badURI()
		}
		u.hosts = append(u.hosts, h)
	}
	if u.srv && len(u.hosts) != 1 {
		return nil, badURI()
	}
	path, query, _ := strings.Cut(tail, "?")
	db, err := url.PathUnescape(strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, badURI()
	}
	u.db = db
	if u.query, err = url.ParseQuery(query); err != nil {
		return nil, badURI()
	}
	u.rawQuery = query
	return u, nil
}

// param returns the value of query parameter name, matched case-insensitively
// as the driver does.
func (u *uri) param(name string) (string, bool) {
	for k, v := range u.query {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[len(v)-1], true
		}
	}
	return "", false
}

func (Engine) Inspect(dsn string) (engine.DSNInfo, error) {
	u, err := parseURI(dsn)
	if err != nil {
		return engine.DSNInfo{}, err
	}
	info := engine.DSNInfo{Database: u.db, User: u.user, Password: u.password}
	var names []string
	for i, h := range u.hosts {
		host, port, err := net.SplitHostPort(h)
		if err != nil {
			host, port = strings.Trim(h, "[]"), ""
		}
		names = append(names, host)
		if i == 0 && !u.srv {
			info.Port = 27017
			if port != "" {
				info.Port, _ = strconv.Atoi(port)
			}
		}
	}
	info.Host = strings.Join(names, ",")
	info.Redacted = redactURI(dsn)
	return info, nil
}

// redactURI replaces the password in dsn's authority with ***, leaving
// every other byte as it was.
func redactURI(dsn string) string {
	i := strings.Index(dsn, "://")
	if i < 0 {
		return dsn
	}
	start := i + 3
	end := len(dsn)
	if j := strings.IndexAny(dsn[start:], "/?"); j >= 0 {
		end = start + j
	}
	at := strings.LastIndexByte(dsn[start:end], '@')
	if at < 0 {
		return dsn
	}
	colon := strings.IndexByte(dsn[start:start+at], ':')
	if colon < 0 {
		return dsn
	}
	return dsn[:start+colon+1] + "***" + dsn[start+at:]
}

// refusedServerParams make the driver read files on the qh host or use its
// ambient credentials; server mode refuses them.
var refusedServerParams = []string{"tlsCAFile", "tlsCertificateKeyFile", "tlsCertificateKeyFilePassword", "tlsInsecure"}

var refusedMechanisms = []string{"MONGODB-AWS", "GSSAPI", "MONGODB-OIDC"}

// CheckServerDSN implements engine.ServerDSNChecker.
func (Engine) CheckServerDSN(dsn string) error {
	u, err := parseURI(dsn)
	if err != nil {
		return err
	}
	for _, p := range refusedServerParams {
		if _, ok := u.param(p); ok {
			return qerr.New(qerr.Invalid, "this server does not accept "+p+" in a mongodb connection string").
				WithHint("Give TLS certificates through the connection's tls settings instead")
		}
	}
	if m, ok := u.param("authMechanism"); ok {
		for _, r := range refusedMechanisms {
			if strings.EqualFold(m, r) {
				return qerr.New(qerr.Invalid, "this server does not accept authMechanism "+r).
					WithHint("Use a user name and password (SCRAM), or X.509 with the connection's tls settings")
			}
		}
	}
	return nil
}

// dialer adapts spec.Dial to the driver's ContextDialer.
type dialer func(ctx context.Context, network, addr string) (net.Conn, error)

func (d dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d(ctx, network, addr)
}

// tlsParams are the connection-string settings spec.TLS replaces.
var tlsParams = []string{"tls", "ssl", "tlsCAFile", "tlsCertificateKeyFile", "tlsCertificateKeyFilePassword",
	"tlsInsecure", "tlsAllowInvalidCertificates", "tlsAllowInvalidHostnames", "tlsDisableOCSPEndpointCheck"}

// withoutParams returns dsn without the named query parameters
// (case-insensitively), plus extra ("k=v") when not empty.
func withoutParams(dsn string, names []string, extra string) string {
	base, query, _ := strings.Cut(dsn, "?")
	var keep []string
	for _, kv := range strings.Split(query, "&") {
		if kv == "" {
			continue
		}
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, n := range names {
			if strings.EqualFold(k, n) {
				drop = true
			}
		}
		if !drop {
			keep = append(keep, kv)
		}
	}
	if extra != "" {
		keep = append(keep, extra)
	}
	if len(keep) == 0 {
		return base
	}
	if !strings.Contains(base[strings.Index(base, "://")+3:], "/") {
		base += "/"
	}
	return base + "?" + strings.Join(keep, "&")
}

func clientOptions(spec engine.ConnSpec) (*options.ClientOptions, *uri, error) {
	if spec.Server {
		if err := (Engine{}).CheckServerDSN(spec.DSN); err != nil {
			return nil, nil, err
		}
	}
	u, err := parseURI(spec.DSN)
	if err != nil {
		return nil, nil, err
	}
	dsn := spec.DSN
	var tlsCfg *tls.Config
	if spec.TLS != nil {
		// The driver sets ServerName per host when it is empty.
		if tlsCfg, err = spec.TLS.Config(""); err != nil {
			return nil, nil, err
		}
		extra := ""
		if tlsCfg == nil {
			extra = "tls=false"
		}
		dsn = withoutParams(dsn, tlsParams, extra)
	}
	opts := options.Client().ApplyURI(dsn)
	if err := opts.Validate(); err != nil {
		return nil, nil, qerr.New(qerr.Invalid, "invalid mongodb connection string: "+scrubText(err.Error(), spec.DSN, u.password))
	}
	if tlsCfg != nil {
		opts.SetTLSConfig(tlsCfg)
	}
	if spec.Password != "" {
		if opts.Auth == nil || opts.Auth.Username == "" {
			return nil, nil, qerr.New(qerr.Invalid, "a password needs a user name in the mongodb connection string")
		}
		opts.Auth.Password, opts.Auth.PasswordSet = spec.Password, true
	}
	if spec.Dial != nil {
		opts.SetDialer(dialer(spec.Dial))
	}
	opts.SetMaxPoolSize(uint64(max(spec.PoolSize, 1)))
	opts.SetAppName("qh")
	if opts.ServerSelectionTimeout == nil {
		opts.SetServerSelectionTimeout(10 * time.Second)
	}
	if opts.ConnectTimeout == nil {
		opts.SetConnectTimeout(10 * time.Second)
	}
	return opts, u, nil
}

func (Engine) Open(ctx context.Context, spec engine.ConnSpec) (engine.Conn, error) {
	opts, u, err := clientOptions(spec)
	if err != nil {
		return nil, err
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, mapErr(err)
	}
	c := &conn{client: client, defaultDB: u.db, pinned: u.db != "", namespaces: spec.Namespaces,
		readOnly: spec.Mode != engine.ModeRW, password: u.password, extra: spec.Password}
	if c.defaultDB == "" {
		c.defaultDB = "test"
	}
	if _, err := c.Ping(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return c, nil
}

type conn struct {
	client     *mongo.Client
	defaultDB  string // the DSN's database, else "test" as in mongosh
	pinned     bool   // the DSN names a database: it is the only namespace
	namespaces []string
	readOnly   bool
	// password and extra are scrubbed from every error, in case the driver
	// quotes them.
	password, extra string
}

func (c *conn) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.client.Disconnect(ctx)
}

func (c *conn) Ping(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var info struct {
		Version string `bson:"version"`
	}
	if err := c.client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&info); err != nil {
		return "", c.err(err)
	}
	return "MongoDB " + info.Version, nil
}

// err maps a driver error and removes the passwords from its text.
func (c *conn) err(err error) error {
	e := qerr.From(mapErr(err))
	out := *e
	out.Message = scrubText(out.Message, c.password, c.extra)
	return &out
}

func scrubText(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return s
}

// Query, Explain and Introspect are implemented in exec.go and introspect.go.
func (c *conn) Query(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	return nil, qerr.New(qerr.Invalid, fmt.Sprintf("not implemented: %q", q))
}

func (c *conn) Explain(ctx context.Context, q string, o engine.QueryOpts) (*engine.Result, error) {
	return nil, qerr.New(qerr.Invalid, "not implemented")
}

func (c *conn) Introspect(ctx context.Context) (*engine.Catalog, error) {
	return nil, qerr.New(qerr.Invalid, "not implemented")
}
```

- [ ] **Step 5: Implement `errors.go`**

```go
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/anderson-0/qh/internal/qerr"
)

// Server error codes qh treats specially.
const (
	codeUnauthorized      = 13
	codeAuthFailed        = 18
	codeNamespaceNotFound = 26
	codeMaxTimeExpired    = 50
)

// mapErr classifies a driver error.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var qe *qerr.Error
	if errors.As(err, &qe) {
		return qe
	}
	if errors.Is(err, context.DeadlineExceeded) || mongo.IsTimeout(err) {
		return qerr.New(qerr.Timeout, "the query timed out").WithHint("Narrow the filter, add an index, or raise the connection's timeout")
	}
	var ce mongo.CommandError
	if errors.As(err, &ce) {
		switch ce.Code {
		case codeMaxTimeExpired:
			return qerr.New(qerr.Timeout, "the query timed out").WithHint("Narrow the filter, add an index, or raise the connection's timeout")
		case codeAuthFailed:
			return qerr.New(qerr.ConnectionFailed, "authentication failed").WithHint("Check the user name, password and authSource")
		}
		return qerr.New(qerr.QueryError, fmt.Sprintf("%s (%s, code %d)", ce.Message, ce.Name, ce.Code))
	}
	var we mongo.WriteException
	if errors.As(err, &we) && len(we.WriteErrors) > 0 {
		w := we.WriteErrors[0]
		return qerr.New(qerr.QueryError, fmt.Sprintf("%s (code %d)", w.Message, w.Code))
	}
	msg := err.Error()
	if mongo.IsNetworkError(err) || strings.Contains(msg, "server selection") || strings.Contains(msg, "connection(") ||
		strings.Contains(msg, "auth error") || strings.Contains(msg, "unable to authenticate") {
		return qerr.Wrap(qerr.ConnectionFailed, "cannot reach MongoDB: "+firstLine(msg), err)
	}
	return qerr.New(qerr.QueryError, firstLine(msg))
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}
```

Check the v2 error types with `go doc go.mongodb.org/mongo-driver/v2/mongo CommandError`, and likewise for `WriteException`, `IsTimeout` and `IsNetworkError`. If `mongo.CommandError` is matched as a pointer in v2, adapt the `errors.As` targets. Check also what a wrong password returns from `Connect` + `buildInfo` in practice. The test requires `connection_failed`.

- [ ] **Step 6: Register the engine in the CLI**

`internal/cli/engines.go`:

```go
package cli

// Engines register themselves in init.
import (
	_ "github.com/anderson-0/qh/internal/engine/mongodb"
	_ "github.com/anderson-0/qh/internal/engine/mysql"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
)
```

Grep for other places that list engines and add `mongodb` where they are meant to cover every engine:
- `grep -rn '"postgres", "mysql"' internal cmd`;
- `grep -rn 'engine/postgres"' internal --include=*_test.go`, for invariant tests that blank-import all engines.

Leave tests that are specifically about SQL alone.

- [ ] **Step 7: Run the tests**

```bash
go get github.com/testcontainers/testcontainers-go/modules/mongodb@$(go list -m -f '{{.Version}}' github.com/testcontainers/testcontainers-go)
go vet ./... && go test ./internal/engine/mongodb ./internal/cli -count=1
```

Expected: PASS. The container tests start `mongo:7` and `mongo:8` once each.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal
git commit -m "feat(mongodb): connect with qh's dialer, TLS, password and server-mode checks

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 5: Run queries and flatten results

**Files:**
- Create: `internal/engine/mongodb/exec.go`, which replaces the `Query` and `Explain` stubs in `mongodb.go` (delete the stubs there)
- Create: `internal/engine/mongodb/values.go`
- Create: `internal/engine/mongodb/exec_test.go`
- Create: `internal/engine/mongodb/values_test.go`

**Interfaces:**
- Consumes:
  - `Parse` and `translate`, and `Op` with its kinds (Tasks 2–3);
  - `conn` and `mapErr` (Task 4);
  - the test helpers `fixtureSpec`, `openSpec`, `mongoURI`, `loadFixture` and `withDB` (Task 4).
- Produces:
  - `(*conn).Query` and `(*conn).Explain`;
  - `valueOf(bson.RawValue) engine.Value`;
  - `flatten([]bson.Raw) ([]engine.Column, [][]engine.Value)`;
  - `relaxedJSON(bson.RawValue) string`;
  - `(*conn).allowed(db string) bool`.

  Task 6 uses `valueOf`, `relaxedJSON` and `allowed`.

- [ ] **Step 1: Write the failing value tests (no Docker)**

`internal/engine/mongodb/values_test.go`:

```go
package mongodb

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/engine"
)

func rawDoc(t *testing.T, d bson.D) bson.Raw {
	t.Helper()
	b, err := bson.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestValueOf(t *testing.T) {
	oid, _ := bson.ObjectIDFromHex("507f1f77bcf86cd799439011")
	dec, _ := bson.ParseDecimal128("1.10")
	at := time.Date(2024, 1, 2, 3, 4, 5, 6e6, time.UTC)
	doc := rawDoc(t, bson.D{
		{Key: "s", Value: "x"}, {Key: "i", Value: int32(7)}, {Key: "l", Value: int64(9007199254740993)},
		{Key: "f", Value: 1.5}, {Key: "whole", Value: 375.0}, {Key: "dec", Value: dec}, {Key: "b", Value: true},
		{Key: "at", Value: bson.NewDateTimeFromTime(at)}, {Key: "oid", Value: oid}, {Key: "nul", Value: nil},
		{Key: "obj", Value: bson.D{{Key: "city", Value: "Lyon"}, {Key: "n", Value: int32(1)}}},
		{Key: "arr", Value: bson.A{"a", int32(2), bson.D{{Key: "x", Value: true}}}},
		{Key: "bin", Value: bson.Binary{Data: []byte{1, 2, 3}}},
		{Key: "uuid", Value: bson.Binary{Subtype: 4, Data: []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}}},
		{Key: "re", Value: bson.Regex{Pattern: "^a", Options: "i"}},
		{Key: "ts", Value: bson.Timestamp{T: 1, I: 2}},
	})
	want := map[string]engine.Value{
		"s": {Text: "x"}, "i": {Text: "7"}, "l": {Text: "9007199254740993"}, "f": {Text: "1.5"}, "whole": {Text: "375"},
		"dec": {Text: "1.10"}, "b": {Text: "true"}, "at": {Text: "2024-01-02T03:04:05.006Z"},
		"oid": {Text: "507f1f77bcf86cd799439011"}, "nul": {Null: true},
		"obj": {Text: `{"city":"Lyon","n":1}`}, "arr": {Text: `["a",2,{"x":true}]`},
		"bin": {Binary: true, Size: 3}, "uuid": {Text: "01234567-89ab-cdef-0123-456789abcdef"},
		"re": {Text: "/^a/i"}, "ts": {Text: "Timestamp(1, 2)"},
	}
	elems, _ := doc.Elements()
	for _, e := range elems {
		if got := valueOf(e.Value()); got != want[e.Key()] {
			t.Errorf("%s: got %+v, want %+v", e.Key(), got, want[e.Key()])
		}
	}
}

func TestFlattenColumnOrder(t *testing.T) {
	docs := []bson.Raw{
		rawDoc(t, bson.D{{Key: "b", Value: 1}, {Key: "_id", Value: 1}}),
		rawDoc(t, bson.D{{Key: "_id", Value: 2}, {Key: "c", Value: 3}, {Key: "b", Value: 2}}),
	}
	cols, rows := flatten(docs)
	if len(cols) != 3 || cols[0].Name != "_id" || cols[1].Name != "b" || cols[2].Name != "c" {
		t.Fatalf("cols = %+v", cols)
	}
	if !rows[0][2].Null || rows[1][2].Text != "3" {
		t.Fatalf("rows = %+v", rows)
	}
}
```

- [ ] **Step 2: Write the failing query tests (Docker)**

`internal/engine/mongodb/exec_test.go`:

```go
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
	for _, image := range []string{"mongo:7", "mongo:8"} {
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
	ro := openSpec(t, fixtureSpec(t, "mongo:8", engine.ModeRO))
	o := roOpts
	o.MaxRows = 3
	expectOut(t, "find", runQ(t, ro, `db.orders.find({}, {_id: 1}).sort({_id: 1})`, o), "_id\n1\n2\n3\n-- 3 rows shown, more exist (truncated)")
	expectOut(t, "limit", runQ(t, ro, `db.orders.find({}, {_id: 1}).sort({_id: 1}).limit(3)`, o), "_id\n1\n2\n3\n-- 3 rows")
	expectOut(t, "aggregate", runQ(t, ro, `db.orders.aggregate([{$sort: {_id: 1}}, {$project: {_id: 1}}])`, o), "_id\n1\n2\n3\n-- 3 rows shown, more exist (truncated)")
	o.MaxRows = 1
	expectOut(t, "distinct", strings.Split(runQ(t, ro, `db.orders.distinct("status")`, o), "\n")[2], "-- 1 row shown, more exist (truncated)")
}

func TestReadOnlyRefusesWrites(t *testing.T) {
	spec := fixtureSpec(t, "mongo:8", engine.ModeRO)
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
	rw := openSpec(t, fixtureSpec(t, "mongo:8", engine.ModeRW))
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
	ro := openSpec(t, fixtureSpec(t, "mongo:8", engine.ModeRO))
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
	ro := openSpec(t, fixtureSpec(t, "mongo:8", engine.ModeRO))
	for _, q := range []string{`db.ordrs.find({status: "paid"})`, `db.ordrs.countDocuments({})`, `db.ordrs.aggregate([{$match: {}}])`, `db.ordrs.getIndexes()`} {
		_, err := ro.Query(context.Background(), q, roOpts)
		if e := qerr.From(err); err == nil || e.Category != qerr.QueryError || e.UnknownTable != "ordrs" || !strings.Contains(e.Message, `collection "ordrs" does not exist`) {
			t.Errorf("%s: %#v", q, e)
		}
	}
}

func TestExplain(t *testing.T) {
	ro := openSpec(t, fixtureSpec(t, "mongo:8", engine.ModeRO))
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
	spec := fixtureSpec(t, "mongo:8", engine.ModeRO)
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
	spec := fixtureSpec(t, "mongo:8", engine.ModeRO)
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
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/engine/mongodb -count=1`

Expected: FAIL. `valueOf` and `flatten` are undefined, and `Query` returns "not implemented".

- [ ] **Step 4: Implement `values.go`**

```go
package mongodb

import (
	"encoding/hex"
	"fmt"
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
		return engine.Value{Text: strconv.FormatFloat(v.Double(), 'f', -1, 64)}
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
		if (sub == 4 || sub == 3) && len(data) == 16 {
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
```

`relaxedJSON` marshals a `bson.RawValue` inside a `bson.D`. Check that v2 marshals a `RawValue` value transparently; `TestValueOf` pins the output. If it doesn't, unmarshal into `any` first (`v.Unmarshal(&x)`) and marshal `x`. `'f', -1` prints `375` for `375.0` and `1.5` for `1.5`, matching the test.

- [ ] **Step 5: Implement `exec.go`**

Delete the `Query` and `Explain` stubs from `mongodb.go` first.

```go
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
	if op.Explain {
		return c.run(ctx, op, o, true)
	}
	return c.run(ctx, op, o, false)
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
	// The server's maxTimeMS fires first; the context is a backstop.
	ctx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	d := c.client.Database(op.DB)
	var res *engine.Result
	var err error
	if explain {
		res, err = c.explain(ctx, d, op)
	} else {
		res, err = c.exec(ctx, d, op, timeout, maxRows)
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

// timed are the commands that accept maxTimeMS.
var timed = map[string]bool{"find": true, "aggregate": true, "count": true, "distinct": true, "findAndModify": true}

func withMaxTime(cmd bson.D, timeout time.Duration) bson.D {
	if !timed[cmd[0].Key] {
		return cmd
	}
	return append(append(bson.D{}, cmd...), bson.E{Key: "maxTimeMS", Value: timeout.Milliseconds()})
}

func (c *conn) exec(ctx context.Context, d *mongo.Database, op *Op, timeout time.Duration, maxRows int) (*engine.Result, error) {
	cmd := withMaxTime(op.Command, timeout)
	switch op.Kind {
	case kDocs, kNames, kAggCount:
		cur, err := d.RunCommandCursor(ctx, cmd)
		if err != nil {
			return nil, err
		}
		defer cur.Close(context.WithoutCancel(ctx))
		var docs []bson.Raw
		for len(docs) <= maxRows && cur.Next(ctx) {
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
		vals, _ := reply.Lookup("values").Array().Values()
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

// names renders listCollections results as one "name" column, without
// system collections.
func names(docs []bson.Raw, maxRows int) *engine.Result {
	r := &engine.Result{Columns: []engine.Column{{Name: "name"}}, RowsAffected: -1}
	for _, d := range docs {
		n, _ := d.Lookup("name").StringValueOK()
		if strings.HasPrefix(n, "system.") {
			continue
		}
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
```

Notes for the implementer:
- `bson.RawValue{Type:, Value:}` constructs a raw value from document bytes. Check the field names in v2 with `go doc`.
- `mongo.CommandError` may need a pointer target in `errors.As`. Use the same form you settled on in `errors.go`.
- `TestUnknownCollection` covers `getIndexes`. `listIndexes` on a missing collection returns code 26, which `opErr` maps.

- [ ] **Step 6: Run the tests**

Run: `go vet ./internal/engine/mongodb && go test ./internal/engine/mongodb -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/engine/mongodb
git commit -m "feat(mongodb): run queries, cap rows and flatten documents into TSV

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 6: Infer the schema

**Files:**
- Create: `internal/engine/mongodb/infer.go`: pure inference over sampled documents
- Create: `internal/engine/mongodb/introspect.go`: `(*conn).Introspect`, which replaces the stub in `mongodb.go` (delete the stub)
- Create: `internal/engine/mongodb/infer_test.go` (no Docker) and `internal/engine/mongodb/introspect_test.go` (Docker)
- Create: `internal/engine/mongodb/testdata/mongodb_{overview,orders,users}.golden` (generated, then checked)
- Modify: `internal/format/schema.go` (overview folding; `→coll` instead of `→coll._id`)
- Modify: `internal/format/schema_test.go`

**Interfaces:**
- Consumes:
  - `valueOf`, `relaxedJSON` and `(*conn).allowed` (Task 5);
  - `conn.pinned` and `conn.defaultDB` (Task 4);
  - `enginetest.RunCatalog` and `CatalogHarness` (Task 1);
  - `loadFixtureNamed`, `mongoURI`, `withDB` and `openSpec` (Task 4).
- Produces:
  - `(*conn).Introspect`;
  - `infer(docs []bson.Raw) *inferred`, which returns fields, an overflow comment, and per-path samples for reference checks.

**Ruling R4:** the spec says special index kinds "add the label to the index name". But `format` never prints index names, only columns. So the label goes on the column, as `bio:text`, `loc:2dsphere` or `k:hashed`, and a TTL index adds `ttl` to its last column (`created_at ttl`). Cost if wrong: cosmetic.

- [ ] **Step 1: Write the failing inference tests (no Docker)**

`internal/engine/mongodb/infer_test.go`:

```go
package mongodb

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/engine"
)

func fieldMap(fs []engine.Field) map[string]engine.Field {
	m := map[string]engine.Field{}
	for _, f := range fs {
		m[f.Name] = f
	}
	return m
}

func TestInferenceCapsAndUnions(t *testing.T) {
	var docs []bson.Raw
	for i := 0; i < 60; i++ {
		var v any = int32(i)
		if i%3 == 0 {
			v = fmt.Sprint(i)
		}
		d := bson.D{{Key: "_id", Value: int32(i)}, {Key: "v", Value: v}, {Key: "status", Value: []string{"a", "b", "c"}[i%3]},
			{Key: "deep", Value: bson.D{{Key: "b", Value: bson.D{{Key: "c", Value: bson.D{{Key: "d", Value: bson.D{{Key: "e", Value: int32(1)}}}}}}}}},
			{Key: "items", Value: bson.A{bson.D{{Key: "sku", Value: "x"}, {Key: "qty", Value: int32(1)}}}},
			{Key: "tags", Value: bson.A{"t1", "t2"}}}
		if i%2 == 0 {
			d = append(d, bson.E{Key: "sometimes", Value: "s"})
		}
		if i == 0 {
			d = append(d, bson.E{Key: "nul", Value: nil})
		}
		docs = append(docs, rawDoc(t, d))
	}
	got := infer(docs)
	f := fieldMap(got.fields)
	checks := map[string]string{
		"_id": "int", "v": "int|string", "status": "string", "deep": "object", "deep.b": "object", "deep.b.c": "object",
		"deep.b.c.d": "object", "items": "array<object>", "items[].sku": "string", "items[].qty": "int", "tags": "array<string>",
		"sometimes": "string",
	}
	for path, typ := range checks {
		if f[path].Type != typ {
			t.Errorf("%s: type %q, want %q", path, f[path].Type, typ)
		}
		if !f[path].Inferred {
			t.Errorf("%s: not marked inferred", path)
		}
	}
	if _, ok := f["deep.b.c.d.e"]; ok {
		t.Error("descended deeper than 4 levels")
	}
	if f["v"].Nullable || !f["sometimes"].Nullable || !f["nul"].Nullable {
		t.Errorf("nullability: v %v sometimes %v nul %v", f["v"].Nullable, f["sometimes"].Nullable, f["nul"].Nullable)
	}
	if !reflect.DeepEqual(f["status"].Enum, []string{"a", "b", "c", "…"}) {
		t.Errorf("status enum: %v", f["status"].Enum)
	}
	if f["v"].Enum != nil {
		t.Errorf("mixed-type field got values: %v", f["v"].Enum)
	}
	if got.fields[0].Name != "_id" || got.fields[1].Name != "v" {
		t.Errorf("order: %v, %v", got.fields[0].Name, got.fields[1].Name)
	}

	wide := bson.D{{Key: "_id", Value: 1}}
	for i := 0; i < 250; i++ {
		wide = append(wide, bson.E{Key: fmt.Sprintf("k%03d", i), Value: int32(i)})
	}
	w := infer([]bson.Raw{rawDoc(t, wide)})
	if len(w.fields) != 200 || w.comment != "…(+51 fields)" {
		t.Errorf("wide: %d fields, comment %q", len(w.fields), w.comment)
	}
}

func TestInferenceFewDocsHaveNoValues(t *testing.T) {
	var docs []bson.Raw
	for i := 0; i < 10; i++ {
		docs = append(docs, rawDoc(t, bson.D{{Key: "_id", Value: int32(i)}, {Key: "status", Value: "a"}}))
	}
	if e := fieldMap(infer(docs).fields)["status"].Enum; e != nil {
		t.Errorf("enum from 10 documents: %v", e)
	}
}

func TestInferenceSamples(t *testing.T) {
	var docs []bson.Raw
	for i := 0; i < 8; i++ {
		docs = append(docs, rawDoc(t, bson.D{{Key: "_id", Value: int32(i)}, {Key: "user_id", Value: int32(i % 3)}}))
	}
	s := infer(docs).samples["user_id"]
	if len(s) != 3 {
		t.Fatalf("distinct samples: %d", len(s))
	}
	if !strings.Contains(fmt.Sprint(infer(docs).types["user_id"]), "int") {
		t.Errorf("types: %v", infer(docs).types["user_id"])
	}
}
```

Add a test to `internal/format/schema_test.go`:

```go
func TestOverviewFoldsNestedFields(t *testing.T) {
	c := &engine.Catalog{Namespaces: []engine.Namespace{{Name: "shop", Objects: []*engine.Object{
		{Namespace: "shop", Name: "customers", Kind: engine.KindCollection, RowEstimate: 599, PrimaryKey: []string{"_id"},
			Fields: []engine.Field{{Name: "_id"}, {Name: "name"}, {Name: "address"}, {Name: "address.city"},
				{Name: "address.country"}, {Name: "address.zip"}, {Name: "items"}, {Name: "items[].sku"}, {Name: "store_id"}},
			ForeignKeys: []engine.ForeignKey{{Namespace: "shop", Object: "customers", Columns: []string{"store_id"},
				RefNamespace: "shop", RefObject: "stores", RefColumns: []string{"_id"}}}},
	}}}}
	out, _ := Schema(c, SchemaOpts{})
	want := "shop: 1 collection\n  customers ~599: _id PK, name, address{city,country,…}, items{sku}, store_id→stores\n"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}
```

- [ ] **Step 2: Write the failing introspection tests (Docker)**

`internal/engine/mongodb/introspect_test.go`:

```go
package mongodb

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/engine/enginetest"
)

func TestCatalog(t *testing.T) {
	for _, image := range []string{"mongo:7", "mongo:8"} {
		t.Run(image, func(t *testing.T) {
			uri := mongoURI(t, image)
			db := loadFixtureNamed(t, uri, "shop")
			enginetest.RunCatalog(t, enginetest.CatalogHarness{Engine: Engine{},
				Spec:     engine.ConnSpec{Engine: "mongodb", DSN: withDB(uri, db), Mode: engine.ModeRO},
				IDColumn: "_id", GoldenPrefix: "mongodb"})
		})
	}
}

// insert adds docs to db.coll through the driver.
func insert(t *testing.T, uri, db, coll string, docs ...any) {
	t.Helper()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	if _, err := client.Database(db).Collection(coll).InsertMany(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
}

func objectNamed(t *testing.T, cat *engine.Catalog, name string) *engine.Object {
	t.Helper()
	objs := cat.Lookup(name)
	if len(objs) != 1 {
		t.Fatalf("%s: %d objects", name, len(objs))
	}
	return objs[0]
}

func TestReferences(t *testing.T) {
	uri := mongoURI(t, "mongo:8")
	db := loadFixture(t, uri)
	insert(t, uri, db, "ghost_refs", bson.D{{Key: "user_id", Value: int32(100)}}, bson.D{{Key: "user_id", Value: int32(101)}},
		bson.D{{Key: "user_id", Value: int32(102)}}, bson.D{{Key: "user_id", Value: int32(103)}}, bson.D{{Key: "user_id", Value: int32(1)}})
	insert(t, uri, db, "catalog_item", bson.D{{Key: "_id", Value: int32(1)}}, bson.D{{Key: "_id", Value: int32(2)}})
	insert(t, uri, db, "loan", bson.D{{Key: "item_key", Value: int32(1)}, {Key: "lines", Value: bson.A{bson.D{{Key: "order_id", Value: int32(2)}}}}},
		bson.D{{Key: "item_key", Value: int32(2)}})
	c := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: withDB(uri, db), Mode: engine.ModeRO})
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fks := objectNamed(t, cat, "ghost_refs").ForeignKeys; len(fks) != 0 {
		t.Errorf("unverified reference kept: %+v", fks)
	}
	loan := objectNamed(t, cat, "loan")
	has := func(col, ref string) bool {
		for _, fk := range loan.ForeignKeys {
			if fk.Columns[0] == col && fk.RefObject == ref && fk.RefColumns[0] == "_id" {
				return true
			}
		}
		return false
	}
	if !has("item_key", "catalog_item") || !has("lines[].order_id", "orders") {
		t.Errorf("loan FKs: %+v", loan.ForeignKeys)
	}
}

func TestIntrospectSurvivesBrokenView(t *testing.T) {
	uri := mongoURI(t, "mongo:8")
	db := loadFixture(t, uri)
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	broken := bson.A{bson.D{{Key: "$project", Value: bson.D{{Key: "x", Value: bson.D{{Key: "$divide", Value: bson.A{1, 0}}}}}}}}
	if err := client.Database(db).CreateView(context.Background(), "broken_view", "orders", broken); err != nil {
		t.Fatal(err)
	}
	c := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: withDB(uri, db), Mode: engine.ModeRO})
	cat, err := c.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v := objectNamed(t, cat, "broken_view")
	if v.Kind != engine.KindView || len(v.Fields) != 0 {
		t.Errorf("broken view: %+v", v)
	}
	if len(objectNamed(t, cat, "orders").Fields) == 0 {
		t.Error("orders lost its fields")
	}
}

func TestIntrospectDatabases(t *testing.T) {
	uri := mongoURI(t, "mongo:8")
	a, b := loadFixture(t, uri), loadFixture(t, uri)
	all := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: uri, Mode: engine.ModeRO})
	cat, err := all.Introspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, ns := range cat.Namespaces {
		names[ns.Name] = true
	}
	if !names[a] || !names[b] || names["admin"] || names["local"] || names["config"] {
		t.Errorf("namespaces: %v", names)
	}
	only := openSpec(t, engine.ConnSpec{Engine: "mongodb", DSN: uri, Mode: engine.ModeRO, Namespaces: []string{a}})
	cat, err = only.Introspect(context.Background())
	if err != nil || len(cat.Namespaces) != 1 || cat.Namespaces[0].Name != a {
		t.Fatalf("allowed: %+v %v", cat, err)
	}
	if !strings.HasPrefix(a, "fx_") {
		t.Fatal(a)
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test ./internal/engine/mongodb ./internal/format -count=1`

Expected: FAIL. `infer` is undefined, `Introspect` is not implemented, and the overview does not fold.

- [ ] **Step 4: Implement `infer.go`**

```go
package mongodb

import (
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/engine"
)

const (
	sampleSize    = 100
	maxPathDepth  = 4
	maxPaths      = 200
	maxEnumValues = 8
	minEnumDocs   = 50
	refSamples    = 5
)

// inferred is what sampling found about one collection.
type inferred struct {
	fields  []engine.Field
	comment string                     // "…(+N fields)" when paths were cut
	samples map[string][]bson.RawValue // up to refSamples distinct scalar values per path
	types   map[string]string          // the dominant type per path
}

type pathStat struct {
	order   int
	types   map[string]int
	present int // documents where the path holds a non-null value
	strs    map[string]bool
	strDocs int
	nonStr  bool
	samples []bson.RawValue
	seen    map[string]bool // sample dedup by raw bytes
}

// infer summarizes sampled documents: dotted paths to depth 4, arrays of
// documents as path[].field, type unions by frequency, nullability, sampled
// string values, and a few distinct values per path for reference checks.
func infer(docs []bson.Raw) *inferred {
	stats := map[string]*pathStat{}
	var order []string
	stat := func(p string) *pathStat {
		s := stats[p]
		if s == nil {
			s = &pathStat{order: len(order), types: map[string]int{}, strs: map[string]bool{}, seen: map[string]bool{}}
			stats[p] = s
			order = append(order, p)
		}
		return s
	}
	for _, d := range docs {
		present := map[string]bool{}
		walk(d, "", 1, stat, present)
		for p := range present {
			stats[p].present++
		}
	}
	out := &inferred{samples: map[string][]bson.RawValue{}, types: map[string]string{}}
	for i, p := range order {
		if i == maxPaths {
			out.comment = fmt.Sprintf("…(+%d fields)", len(order)-maxPaths)
			break
		}
		s := stats[p]
		f := engine.Field{Name: p, Type: union(s.types), Nullable: s.present < len(docs), Inferred: true}
		if !s.nonStr && len(s.types) == 1 && s.types["string"] > 0 && s.strDocs >= minEnumDocs && len(s.strs) <= maxEnumValues {
			for v := range s.strs {
				f.Enum = append(f.Enum, v)
			}
			sort.Strings(f.Enum)
			f.Enum = append(f.Enum, "…")
		}
		out.fields = append(out.fields, f)
		out.samples[p] = s.samples
		out.types[p] = dominant(s.types)
	}
	return out
}

func walk(d bson.Raw, prefix string, depth int, stat func(string) *pathStat, present map[string]bool) {
	elems, _ := d.Elements()
	for _, e := range elems {
		p := prefix + e.Key()
		v := e.Value()
		s := stat(p)
		if v.Type == bson.TypeNull || v.Type == bson.TypeUndefined {
			continue
		}
		present[p] = true
		typ := typeName(v)
		s.types[typ]++
		switch v.Type {
		case bson.TypeString:
			if !s.strs[v.StringValue()] && len(s.strs) <= maxEnumValues {
				s.strs[v.StringValue()] = true
			}
			s.strDocs++
		default:
			s.nonStr = true
		}
		switch v.Type {
		case bson.TypeEmbeddedDocument:
			if depth < maxPathDepth {
				walk(v.Document(), p+".", depth+1, stat, present)
			}
		case bson.TypeArray:
			if depth < maxPathDepth {
				vals, _ := v.Array().Values()
				for _, x := range vals {
					if x.Type == bson.TypeEmbeddedDocument {
						walk(x.Document(), p+"[].", depth+1, stat, present)
					}
				}
			}
		default:
			if len(s.samples) < refSamples && !s.seen[string(v.Value)] {
				s.seen[string(v.Value)] = true
				s.samples = append(s.samples, v)
			}
		}
	}
}

func typeName(v bson.RawValue) string {
	switch v.Type {
	case bson.TypeString, bson.TypeSymbol:
		return "string"
	case bson.TypeInt32:
		return "int"
	case bson.TypeInt64:
		return "long"
	case bson.TypeDouble:
		return "double"
	case bson.TypeDecimal128:
		return "decimal"
	case bson.TypeBoolean:
		return "bool"
	case bson.TypeDateTime:
		return "date"
	case bson.TypeObjectID:
		return "objectId"
	case bson.TypeEmbeddedDocument:
		return "object"
	case bson.TypeBinary:
		if sub, data := v.Binary(); (sub == 4 || sub == 3) && len(data) == 16 {
			return "uuid"
		}
		return "binData"
	case bson.TypeRegex:
		return "regex"
	case bson.TypeTimestamp:
		return "timestamp"
	case bson.TypeArray:
		vals, _ := v.Array().Values()
		inner := map[string]int{}
		for _, x := range vals {
			if x.Type != bson.TypeNull {
				inner[typeName(x)]++
			}
		}
		if len(inner) == 0 {
			return "array"
		}
		return "array<" + union(inner) + ">"
	}
	return strings.ToLower(v.Type.String())
}

// union joins type names by descending frequency, then by name.
func union(types map[string]int) string {
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if types[names[i]] != types[names[j]] {
			return types[names[i]] > types[names[j]]
		}
		return names[i] < names[j]
	})
	return strings.Join(names, "|")
}

func dominant(types map[string]int) string {
	u := union(types)
	first, _, _ := strings.Cut(u, "|")
	return first
}
```

- [ ] **Step 5: Implement `introspect.go`**

Delete the `Introspect` stub from `mongodb.go`.

```go
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
	if err := cur.Err(); err != nil {
		return engine.Namespace{}, err
	}
	_ = cur.Close(ctx)
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
		if reply, err := d.RunCommand(ctx, bson.D{{Key: "count", Value: ci.name}}).Raw(); err == nil {
			obj.RowEstimate = asInt(reply.Lookup("n"))
		}
		obj.Indexes = c.indexes(ctx, d, ci.name)
	}
	var cmd bson.D
	if ci.view || (obj.RowEstimate >= 0 && obj.RowEstimate <= sampleSize) {
		cmd = bson.D{{Key: "find", Value: ci.name}, {Key: "limit", Value: sampleSize}, {Key: "batchSize", Value: sampleSize}}
	} else {
		cmd = bson.D{{Key: "aggregate", Value: ci.name}, {Key: "pipeline", Value: bson.A{bson.D{{Key: "$sample", Value: bson.D{{Key: "size", Value: sampleSize}}}}}},
			{Key: "cursor", Value: bson.D{{Key: "batchSize", Value: sampleSize}}}}
	}
	cmd = append(cmd, bson.E{Key: "maxTimeMS", Value: sampleTimeout.Milliseconds()})
	var docs []bson.Raw
	if cur, err := d.RunCommandCursor(ctx, cmd); err == nil {
		for len(docs) < sampleSize && cur.Next(ctx) {
			docs = append(docs, append(bson.Raw(nil), cur.Current...))
		}
		if cur.Err() != nil {
			docs = nil
		}
		_ = cur.Close(context.WithoutCancel(ctx))
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

func (c *conn) indexes(ctx context.Context, d *mongo.Database, coll string) []engine.Index {
	cur, err := d.RunCommandCursor(ctx, bson.D{{Key: "listIndexes", Value: coll}})
	if err != nil {
		return nil
	}
	defer cur.Close(context.WithoutCancel(ctx))
	var out []engine.Index
	for cur.Next(ctx) {
		key, ok := cur.Current.Lookup("key").DocumentOK()
		if !ok {
			continue
		}
		elems, _ := key.Elements()
		var cols []string
		for _, e := range elems {
			col := e.Key()
			if s, ok := e.Value().StringValueOK(); ok {
				col += ":" + s // text, 2dsphere, hashed (ruling R4)
			}
			cols = append(cols, col)
		}
		if _, ttl := cur.Current.Lookup("expireAfterSeconds").Int32OK(); ttl && len(cols) > 0 {
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
// one database.
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
			if !ok || target.obj == s.obj || target.obj.Kind != engine.KindCollection {
				continue
			}
			if s.inf.types[f.Name] != target.inf.types["_id"] {
				continue
			}
			if c.verified(ctx, d, target.obj.Name, s.inf.samples[f.Name]) {
				s.obj.ForeignKeys = append(s.obj.ForeignKeys, engine.ForeignKey{Namespace: db, Object: s.obj.Name, Columns: []string{f.Name},
					RefNamespace: db, RefObject: target.obj.Name, RefColumns: []string{"_id"}})
			}
		}
	}
}

// pickTarget finds the collection a field base name points at: base, base+"s",
// or the one collection ending in "_"+base or "_"+base+"s".
func pickTarget(byName map[string]sampled, base string) (sampled, bool) {
	for _, n := range []string{base, base + "s"} {
		if s, ok := byName[n]; ok {
			return s, true
		}
	}
	var found []sampled
	for n, s := range byName {
		if strings.HasSuffix(n, "_"+base) || strings.HasSuffix(n, "_"+base+"s") {
			found = append(found, s)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return sampled{}, false
}

// verified reports whether at least 4 of 5 sampled values (all of them, if
// fewer) exist as _id values in coll.
func (c *conn) verified(ctx context.Context, d *mongo.Database, coll string, values []bson.RawValue) bool {
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
		{Key: "projection", Value: bson.D{{Key: "_id", Value: 1}}}, {Key: "limit", Value: len(values)}, {Key: "batchSize", Value: len(values)},
		{Key: "maxTimeMS", Value: sampleTimeout.Milliseconds()}}
	cur, err := d.RunCommandCursor(ctx, cmd)
	if err != nil {
		return false
	}
	defer cur.Close(context.WithoutCancel(ctx))
	found := 0
	for cur.Next(ctx) {
		found++
	}
	need := len(values)
	if need == refSamples {
		need = refSamples - 1
	}
	return found >= need
}
```

Check the v2 signatures of `ListDatabaseNames` and `options.ListDatabases().SetAuthorizedDatabases` with `go doc`. References are inferred from views too (a view's `user_id` points at `users`); that is intended.

- [ ] **Step 6: Fold nested fields in the overview**

In `internal/format/schema.go`, `columnList`:
- For a field whose name contains `.` or `[]` and whose top-level parent is itself a field, do not list it. Instead, record its next path segment under the parent.
- A parent with recorded children prints as `parent{a,b,…}`: up to two child names in first-seen order, then `…` when there are more.
- An array parent prints its children the same way, without `[]`.
- A single-column FK whose `RefColumns[0]` is `_id` prints `→coll`, with no `._id`.

Add this helper and use it:

```go
// childOf returns the top-level parent and the next segment of a nested
// field name such as "address.city" or "items[].sku".
func childOf(name string) (parent, child string, ok bool) {
	i := strings.IndexAny(name, ".[")
	if i <= 0 {
		return "", "", false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(name[i:], "[]"), ".")
	if j := strings.IndexAny(rest, ".["); j >= 0 {
		rest = rest[:j]
	}
	return name[:i], rest, rest != ""
}
```

```go
func columnList(o *engine.Object) string {
	pk := map[string]bool{}
	for _, c := range o.PrimaryKey {
		pk[c] = true
	}
	fks := map[string]engine.ForeignKey{}
	for _, fk := range o.ForeignKeys {
		if len(fk.Columns) == 1 && len(fk.RefColumns) == 1 {
			fks[fk.Columns[0]] = fk
		}
	}
	names := map[string]bool{}
	for _, f := range o.Fields {
		names[f.Name] = true
	}
	children := map[string][]string{}
	for _, f := range o.Fields {
		if p, c, ok := childOf(f.Name); ok && names[p] && !contains(children[p], c) {
			children[p] = append(children[p], c)
		}
	}
	var cols []string
	for _, f := range o.Fields {
		if p, _, ok := childOf(f.Name); ok && names[p] {
			continue
		}
		s := f.Name
		if kids := children[f.Name]; len(kids) > 0 {
			shown := kids
			if len(shown) > 2 {
				shown = append(append([]string{}, kids[:2]...), "…")
			}
			s += "{" + strings.Join(shown, ",") + "}"
		}
		if pk[f.Name] {
			s += " PK"
		}
		if fk, ok := fks[f.Name]; ok {
			s += "→" + target(o.Namespace, fk.RefNamespace, fk.RefObject)
			if fk.RefColumns[0] != f.Name && fk.RefColumns[0] != "_id" {
				s += "." + fk.RefColumns[0]
			}
		}
		if len(f.Enum) > 0 && len(f.Enum) <= 10 {
			s += " [" + strings.Join(f.Enum, "|") + "]"
		}
		cols = append(cols, s)
	}
	return strings.Join(cols, ", ")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
```

`contains` may already exist in the `format` package; if it does, reuse it. SQL columns never have a field named exactly the prefix of another column with a dot, so SQL output is unchanged. The existing format and engine golden tests prove that.

In `writeObject` (the detail view), a single-column FK to `_id` prints `→ coll._id` as today. Detail output is explicit on purpose; only the overview is shortened.

- [ ] **Step 7: Generate and check the golden files**

```bash
go test ./internal/engine/mongodb -run TestCatalog -count=1 -args -update
cat internal/engine/mongodb/testdata/mongodb_overview.golden internal/engine/mongodb/testdata/mongodb_orders.golden internal/engine/mongodb/testdata/mongodb_users.golden
```

Check them by hand. Estimates and key order must match exactly, and field order is first-seen. `mongodb_overview.golden` must read:

```
shop: 3 collections, 1 view
  order_items ~20: _id PK, order_id→orders, sku, qty
  orders ~250: _id PK, user_id→users, status [paid|pending|…], total
  paid_orders (view): _id, user_id→users, total
  users ~3: _id PK, email, name, bio, avatar, created_at
```

`sku` shows no values, because order_items has only 20 documents, under the 50-document minimum. `status` comes from a 100-document sample of 250 and shows `[paid|pending|…]`.

`mongodb_users.golden` should list:
- `_id int PK`, `email string NN`, `name string`, `bio string`, `avatar binData` and `created_at date`;
- a `← orders.user_id, paid_orders.user_id` line;
- `idx unique(email)`.

Then run the test again without `-update`. It must pass on both `mongo:7` and `mongo:8` with the same golden files.

- [ ] **Step 8: Run everything**

Run: `go vet ./... && go test ./... -count=1`

Expected: PASS. The SQL engine golden files are unchanged; check with `git status internal/engine/*/testdata`.

- [ ] **Step 9: Commit**

```bash
git add internal
git commit -m "feat(mongodb): infer collection schemas, indexes and verified references

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 7: Instructions, end-to-end checks and docs

**Files:**
- Create: `internal/mcpserver/testdata/sql_surface.golden`, generated **before** changing `server.go`
- Modify: `internal/mcpserver/server.go`
- Modify: `internal/mcpserver/server_test.go`
- Create: `internal/cli/mongodb_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes:
  - the registered `mongodb` engine (Tasks 4–6);
  - `core.ConnInfo.Engine`;
  - `connectSvc`, `call` and `run`, the existing test helpers in `mcpserver` and `cli`.
- Produces: the constant `mongoHint` and its use in `withConnections`.

- [ ] **Step 1: Freeze the SQL surface before changing anything**

Add to `internal/mcpserver/server_test.go`:

```go
var updateSurface = flag.Bool("update-surface", false, "rewrite testdata/sql_surface.golden")

// TestSQLSurfaceUnchanged pins the tool definitions and instructions a
// workspace with only SQL connections sees: every byte is paid on every
// model turn, and the benchmark margin over dbhub depends on them.
func TestSQLSurfaceUnchanged(t *testing.T) {
	s := connect(t)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(struct {
		Instructions string
		Tools        any
	}{s.InitializeResult().Instructions, tools.Tools}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "sql_surface.golden")
	if *updateSurface {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(b) {
		t.Fatalf("SQL-only tool surface changed:\n%s", b)
	}
}
```

Add the `flag` and `os` imports. Then generate the golden file from the **unchanged** server and commit it on its own:

```bash
go test ./internal/mcpserver -run TestSQLSurfaceUnchanged -count=1 -args -update-surface
go test ./internal/mcpserver -run TestSQLSurfaceUnchanged -count=1
git add internal/mcpserver
git commit -m "test(mcpserver): pin the tool surface SQL-only workspaces see

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Check that the golden file's instructions read `Query directly if you know the names; else call schema first. Errors on unknown names suggest close matches. Connection: shop (sqlite, ro). source may be omitted.` If the mode or wording differs, the golden file records the truth. Do not edit it by hand.

- [ ] **Step 2: Write the failing instructions test**

Add to `internal/mcpserver/server_test.go`, and add `_ "github.com/anderson-0/qh/internal/engine/mongodb"` to the imports:

```go
func TestInstructionsMentionMongoshOnlyWithMongo(t *testing.T) {
	s, svc := connectSvc(t)
	if strings.Contains(s.InitializeResult().Instructions, "mongosh") {
		t.Fatal("SQL-only workspace got the mongosh sentence")
	}
	for _, name := range []string{"events", "logs"} {
		if _, _, err := svc.CreateConnection(context.Background(), store.DefaultWorkspace, core.ConnInput{
			Name: name, Engine: "mongodb", DSN: "mongodb://127.0.0.1:1/" + name,
		}, false); err != nil {
			t.Fatal(err)
		}
	}
	server := New(svc, func(context.Context) string { return store.DefaultWorkspace }, "test")
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverT, nil); err != nil {
		t.Fatal(err)
	}
	s2, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	in := s2.InitializeResult().Instructions
	if strings.Count(in, mongoHint) != 1 || !strings.HasSuffix(in, mongoHint) {
		t.Fatalf("instructions: %q", in)
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/mcpserver -count=1`

Expected: FAIL, because `mongoHint` is undefined.

- [ ] **Step 4: Implement**

In `internal/mcpserver/server.go`, next to `instructions`:

```go
// mongoHint follows the connections line when a workspace has a MongoDB
// connection; SQL-only workspaces never pay for it.
const mongoHint = "mongodb sources take mongosh syntax: db.<collection>.find(…)/aggregate([…])."
```

In `withConnections`, replace the line that appends the connections line:

```go
			if list, lerr := svc.ListConnections(ctx, ws(ctx)); lerr == nil {
				*in += " " + connectionsLine(list)
				for _, c := range list {
					if c.Engine == "mongodb" {
						*in += " " + mongoHint
						break
					}
				}
			}
```

- [ ] **Step 5: Write the end-to-end CLI test**

`internal/cli/mongodb_test.go`. Check `qh schema --help` and `qh query --help` for the real argument shapes, and adapt the arguments below if they differ.

```go
package cli

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcmongodb "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMongoDBEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcmongodb.Run(ctx, "mongo:8")
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	if err != nil {
		t.Fatal(err)
	}
	base, _ := ctr.ConnectionString(ctx)
	u, _ := url.Parse(base)
	u.Path = "/shop"
	dsn := u.String()
	client, err := mongo.Connect(options.Client().ApplyURI(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	var orders []any
	for i := 1; i <= 30; i++ {
		orders = append(orders, bson.D{{Key: "_id", Value: int32(i)}, {Key: "status", Value: []string{"paid", "pending"}[i%2]}, {Key: "total", Value: float64(i)}})
	}
	if _, err := client.Database("shop").Collection("orders").InsertMany(ctx, orders); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	if _, errOut, code := run(t, home, dsn+"\n", "conn", "add", "shop", "--engine", "mongodb", "--dsn", "-"); code != 0 {
		t.Fatal(errOut)
	}
	out, errOut, code := run(t, home, "", "query", "shop", `db.orders.countDocuments({status: "paid"})`)
	if code != 0 || !strings.Contains(out, "count\n15\n") {
		t.Fatalf("query: %d %q %q", code, out, errOut)
	}
	out, errOut, code = run(t, home, "", "schema", "shop")
	if code != 0 || !strings.Contains(out, "orders ~30: _id PK, status, total") {
		t.Fatalf("schema: %d %q %q", code, out, errOut)
	}
	_, errOut, code = run(t, home, "", "query", "shop", `db.ordrs.find({})`)
	if code == 0 || !strings.Contains(errOut, `collection "ordrs" does not exist`) || !strings.Contains(errOut, "orders") {
		t.Fatalf("unknown collection: %d %q", code, errOut)
	}
	_, errOut, code = run(t, home, "", "query", "shop", `db.orders.deleteMany({})`)
	if code == 0 || !strings.Contains(errOut, "read_only") {
		t.Fatalf("write on read-only: %d %q", code, errOut)
	}
}
```

The `orders ~30: _id PK, status, total` expectation assumes the 30 documents fall under the 50-document threshold, so `status` shows no values. Run `go get github.com/testcontainers/testcontainers-go/modules/mongodb` if the main module does not already have it from Task 4.

- [ ] **Step 6: Document MongoDB in the README**

In `README.md`:
- Add `MongoDB (7 and 8)` to the list of supported engines.
- Add a section after the last engine-specific section:

````markdown
## MongoDB

```bash
qh conn add events --engine mongodb --dsn -    # paste mongodb://user:pw@host:27017/events
qh query events 'db.orders.find({status: "paid"}).sort({total: -1}).limit(5)'
```

Agents query MongoDB through the same `schema` and `query` tools, in mongosh syntax. qh parses a fixed subset and never runs JavaScript:

- **Reads:** `find`, `findOne`, `aggregate`, `countDocuments`, `estimatedDocumentCount`, `distinct`, `getIndexes` and `db.getCollectionNames()`. `find` takes `.sort`, `.limit`, `.skip`, `.project`, `.hint`, `.collation`, `.count()` and `.explain()`.
- **Writes and admin calls:** these need a read-write connection.
  - Document writes: `insertOne/Many`, `updateOne/Many`, `replaceOne`, `deleteOne/Many` and `findOneAnd*`.
  - Aggregates with `$out`/`$merge`.
  - Admin calls: `createIndex(es)`, `dropIndex(es)`, `drop`, `renameCollection` and `db.createCollection`.
- **Literals:** relaxed JSON plus `ObjectId()`, `ISODate()`, `new Date("…")`, `NumberLong()`, `NumberInt()`, `NumberDecimal()`, `UUID()`, `Timestamp()`, `MinKey()`, `MaxKey()` and `/regex/flags`.
- **Results:** TSV, with `_id` first. Nested documents and arrays are compact JSON, and a missing field is `NULL`.
- **Schema:** sampled from up to 100 documents per collection.
  - Fields show as dotted paths with their types.
  - String fields with few values list the values seen, ending in `…`.
  - Fields such as `customer_id` that match another collection's `_id` values show as references (`→ customers`).
- **Databases:** the database in the connection string is the default. Without one, every non-system database is listed, and `db.getSiblingDB("x")` reaches another. `allowed_namespaces` limits which databases queries may name.
- **Server mode:** connection strings may not use `tlsCAFile`, `tlsCertificateKeyFile` or `tlsInsecure`. Use qh's TLS settings instead. Connection strings may not use `authMechanism` values that take the qh host's own credentials (`MONGODB-AWS`, `GSSAPI` or `MONGODB-OIDC`).

Use a MongoDB user with the `read` role for read-only connections; qh checks every call, and the role is a second line of defence.
````

Match the README's existing tone and heading levels.

- [ ] **Step 7: Run everything**

Run: `go vet ./... && go test ./... -count=1`

Expected: PASS. `TestSQLSurfaceUnchanged` still passes after Step 4.

- [ ] **Step 8: Commit**

```bash
git add internal README.md go.mod go.sum
git commit -m "feat(mcpserver): tell agents to use mongosh syntax for MongoDB sources

Adds the sentence only to workspaces with a MongoDB connection, an
end-to-end CLI test and the README section.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 8: Bench: document datasets and their question twins

All paths in this task are under `bench/`, a separate Go module. Run its commands from `bench/`.

**Files:**
- Create: `bench/harness/mongo.go` (the loader, the dataset starters and twin execution)
- Create: `bench/harness/mongo_test.go`
- Create: `bench/harness/questions/pagila-docs.json`, `bench/harness/questions/lendco-docs.json`
- Modify: `bench/harness/questions.go` (`Question.Mongo`)
- Modify: `bench/harness/datasets.go` (`StartDataset` cases)
- Modify: `bench/harness/questions_test.go` (`TestQuestionFiles` covers the new files)
- Modify: `bench/go.mod` and `bench/go.sum` (`go.mongodb.org/mongo-driver/v2`, `github.com/testcontainers/testcontainers-go/modules/mongodb` at the module's testcontainers version)

**Interfaces:**
- Consumes: `StartPagila`, `StartLendco`, `Dataset`, `Truth`, `Equal`, `LoadQuestions` (existing).
- Produces:
  - `StartPagilaDocs(ctx) (*Dataset, func(), error)` and `StartLendcoDocs(ctx) (*Dataset, func(), error)`. Each returns `Engine: "mongodb"`, a `DSN` of the form `mongodb://host:port/<db>`, and `DB` set to the Postgres twin's `*sql.DB`, which is used for reference answers.
  - `Question.Mongo json.RawMessage`, the twin `{"collection": "...", "pipeline": [...]}` in relaxed Extended JSON.
  - `RunTwin(ctx, dsn string, twin json.RawMessage) ([][]any, error)`.

**Document model.** Both datasets hold the same data under different names. Integer keys from Postgres become `_id`.

| pagila-docs collection | Fields |
|---|---|
| `films` | `_id`, `title`, `description`, `release_year`, `language` (name), `rental_duration`, `rental_rate`, `length`, `replacement_cost`, `rating`, `special_features[]`, `actors[]` `{actor_id, first_name, last_name}`, `categories[]` (names) |
| `actors` | `_id`, `first_name`, `last_name` |
| `customers` | `_id`, `store_id`, `first_name`, `last_name`, `email`, `active`, `create_date` (date), `address{address, district, postal_code, phone, city, country}` |
| `staff` | `_id`, `first_name`, `last_name`, `email`, `store_id`, `active`, `username`, `address{…}` |
| `stores` | `_id`, `manager_staff_id`, `address{…}` |
| `inventory` | `_id`, `film_id`, `store_id` |
| `rentals` | `_id`, `rental_date`, `inventory_id`, `customer_id`, `return_date` (date or null), `staff_id` |
| `payments` | `_id`, `customer_id`, `staff_id`, `rental_id`, `amount`, `payment_date` |

| lendco-docs collection | Fields |
|---|---|
| `catalog_item` | `_id`, `item_title`, `synopsis`, `release_yr`, `lang_label`, `loan_days`, `loan_fee`, `runtime_min`, `replacement_fee`, `audience`, `extras[]`, `cast[]` `{performer_key, given_nm, family_nm}`, `genres[]` (labels) |
| `performer` | `_id`, `given_nm`, `family_nm` |
| `member` | `_id`, `branch_key`, `given_nm`, `family_nm`, `contact_email`, `is_active`, `joined_on` (date), `residence{street_line, street_line2, district_nm, postcode, phone_no, locality_nm, nation_nm}` |
| `employee` | `_id`, `given_nm`, `family_nm`, `contact_email`, `branch_key`, `is_active`, `login_nm`, `residence{…}` |
| `branch` | `_id`, `supervisor_key`, `residence{…}` |
| `stock_unit` | `_id`, `item_key`, `branch_key` |
| `loan` | `_id`, `loaned_at`, `unit_key`, `member_key`, `returned_at` (date or null), `employee_key` |
| `charge` | `_id`, `member_key`, `employee_key`, `loan_key`, `amt`, `charged_at` |

Indexes, mirroring Lendco's SQL indexes:
- pagila-docs: `rentals.customer_id`, `payments.customer_id`, `inventory.film_id`;
- lendco-docs: `loan.member_key`, `charge.member_key`, `stock_unit.item_key`.

- [ ] **Step 1: Write the failing tests**

`bench/harness/mongo_test.go`:

```go
package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestDocsQuestionsMirrorSQL(t *testing.T) {
	for docs, sql := range map[string]string{"pagila-docs": "pagila", "lendco-docs": "lendco"} {
		dq, err := LoadQuestions(docs)
		if err != nil {
			t.Fatal(err)
		}
		sq, _ := LoadQuestions(sql)
		if len(dq) != len(sq) {
			t.Fatalf("%s: %d questions, %s has %d", docs, len(dq), sql, len(sq))
		}
		for i := range dq {
			d, s := dq[i], sq[i]
			if d.ID != docs+strings.TrimPrefix(s.ID, sql) || d.Kind != s.Kind || d.Question != s.Question || d.SQL != s.SQL || d.Ordered != s.Ordered {
				t.Errorf("%s does not mirror %s", d.ID, s.ID)
			}
			var twin struct {
				Collection string `json:"collection"`
				Pipeline   []any  `json:"pipeline"`
			}
			if err := json.Unmarshal(d.Mongo, &twin); err != nil || twin.Collection == "" || len(twin.Pipeline) == 0 {
				t.Errorf("%s: bad twin %s: %v", d.ID, d.Mongo, err)
			}
			var parsed bson.D
			if err := bson.UnmarshalExtJSON(d.Mongo, false, &parsed); err != nil {
				t.Errorf("%s: twin is not Extended JSON: %v", d.ID, err)
			}
		}
	}
}

// TestDocsTwinsMatchTruth proves every question is answerable from the
// documents: each twin pipeline returns the reference SQL's answer.
func TestDocsTwinsMatchTruth(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker and the Pagila download")
	}
	ctx := context.Background()
	for _, name := range []string{"pagila-docs", "lendco-docs"} {
		t.Run(name, func(t *testing.T) {
			ds, stop, err := StartDataset(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if ds.Engine != "mongodb" || !strings.HasPrefix(ds.DSN, "mongodb://") {
				t.Fatalf("dataset %+v", ds)
			}
			qs, _ := LoadQuestions(name)
			for _, q := range qs {
				want, err := Truth(ctx, ds.DB, q.SQL)
				if err != nil {
					t.Fatalf("%s truth: %v", q.ID, err)
				}
				got, err := RunTwin(ctx, ds.DSN, q.Mongo)
				if err != nil {
					t.Errorf("%s twin: %v", q.ID, err)
					continue
				}
				if !Equal(want, got, q.Ordered) {
					t.Errorf("%s: twin %v, truth %v", q.ID, got, want)
				}
			}
		})
	}
}
```

In `questions_test.go`, add `"pagila-docs"` and `"lendco-docs"` to the dataset list in `TestQuestionFiles`.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd bench && go test ./harness -run 'Docs|QuestionFiles' -count=1`

Expected: FAIL, because there are no questions for `pagila-docs`.

- [ ] **Step 3: Add the twin field**

In `bench/harness/questions.go`:

```go
// Question is one benchmark question with its reference query.
type Question struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // lookup, join, enum, aggregate, large, misspell
	Question string `json:"question"`
	SQL      string `json:"sql"`
	Ordered  bool   `json:"ordered,omitempty"`
	// Mongo is the twin of SQL on a document dataset:
	// {"collection": "...", "pipeline": [...]} in relaxed Extended JSON. Its
	// result must equal SQL's on the Postgres dataset the documents came from.
	Mongo json.RawMessage `json:"mongo,omitempty"`
}
```

- [ ] **Step 4: Implement `mongo.go`**

```go
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongodb "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// docSource builds one collection: sql returns one text column holding each
// document as relaxed Extended JSON.
type docSource struct {
	coll    string
	sql     string
	indexes []string
}

// date renders a timestamp column as an Extended JSON date (NULL stays NULL).
func date(col string) string {
	return fmt.Sprintf(`CASE WHEN %[1]s IS NULL THEN NULL ELSE json_build_object('$date', to_char(%[1]s::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')) END`, col)
}

const pagilaAddress = `json_build_object('address', ad.address, 'district', ad.district, 'postal_code', ad.postal_code,
	'phone', ad.phone, 'city', ci.city, 'country', co.country)`

const pagilaAddressJoin = `JOIN address ad ON ad.address_id = x.address_id JOIN city ci ON ci.city_id = ad.city_id
	JOIN country co ON co.country_id = ci.country_id`

var pagilaDocs = []docSource{
	{"films", `SELECT json_build_object('_id', f.film_id, 'title', f.title, 'description', f.description,
		'release_year', f.release_year::int, 'language', trim(l.name), 'rental_duration', f.rental_duration,
		'rental_rate', f.rental_rate, 'length', f.length, 'replacement_cost', f.replacement_cost, 'rating', f.rating::text,
		'special_features', to_json(f.special_features),
		'actors', (SELECT coalesce(json_agg(json_build_object('actor_id', a.actor_id, 'first_name', a.first_name, 'last_name', a.last_name) ORDER BY a.actor_id), '[]')
			FROM film_actor fa JOIN actor a ON a.actor_id = fa.actor_id WHERE fa.film_id = f.film_id),
		'categories', (SELECT coalesce(json_agg(c.name ORDER BY c.name), '[]')
			FROM film_category fc JOIN category c ON c.category_id = fc.category_id WHERE fc.film_id = f.film_id))::text
		FROM film f JOIN language l ON l.language_id = f.language_id`, nil},
	{"actors", `SELECT json_build_object('_id', actor_id, 'first_name', first_name, 'last_name', last_name)::text FROM actor`, nil},
	{"customers", `SELECT json_build_object('_id', x.customer_id, 'store_id', x.store_id, 'first_name', x.first_name,
		'last_name', x.last_name, 'email', x.email, 'active', x.activebool, 'create_date', ` + date("x.create_date") + `,
		'address', ` + pagilaAddress + `)::text FROM customer x ` + pagilaAddressJoin, nil},
	{"staff", `SELECT json_build_object('_id', x.staff_id, 'first_name', x.first_name, 'last_name', x.last_name,
		'email', x.email, 'store_id', x.store_id, 'active', x.active, 'username', x.username,
		'address', ` + pagilaAddress + `)::text FROM staff x ` + pagilaAddressJoin, nil},
	{"stores", `SELECT json_build_object('_id', x.store_id, 'manager_staff_id', x.manager_staff_id,
		'address', ` + pagilaAddress + `)::text FROM store x ` + pagilaAddressJoin, nil},
	{"inventory", `SELECT json_build_object('_id', inventory_id, 'film_id', film_id, 'store_id', store_id)::text FROM inventory`, []string{"film_id"}},
	{"rentals", `SELECT json_build_object('_id', rental_id, 'rental_date', ` + date("rental_date") + `, 'inventory_id', inventory_id,
		'customer_id', customer_id, 'return_date', ` + date("return_date") + `, 'staff_id', staff_id)::text FROM rental`, []string{"customer_id"}},
	{"payments", `SELECT json_build_object('_id', payment_id, 'customer_id', customer_id, 'staff_id', staff_id,
		'rental_id', rental_id, 'amount', amount, 'payment_date', ` + date("payment_date") + `)::text FROM payment`, []string{"customer_id"}},
}

const lendcoResidence = `json_build_object('street_line', r.street_line, 'street_line2', r.street_line2, 'district_nm', r.district_nm,
	'postcode', r.postcode, 'phone_no', r.phone_no, 'locality_nm', lo.locality_nm, 'nation_nm', na.nation_nm)`

const lendcoResidenceJoin = `JOIN residence r ON r.residence_key = x.residence_key JOIN locality lo ON lo.locality_key = r.locality_key
	JOIN nation na ON na.nation_key = lo.nation_key`

var lendcoDocs = []docSource{
	{"catalog_item", `SELECT json_build_object('_id', c.item_key, 'item_title', c.item_title, 'synopsis', c.synopsis,
		'release_yr', c.release_yr, 'lang_label', l.lang_label, 'loan_days', c.loan_days, 'loan_fee', c.loan_fee,
		'runtime_min', c.runtime_min, 'replacement_fee', c.replacement_fee, 'audience', c.audience::text, 'extras', to_json(c.extras),
		'cast', (SELECT coalesce(json_agg(json_build_object('performer_key', p.performer_key, 'given_nm', p.given_nm, 'family_nm', p.family_nm) ORDER BY p.performer_key), '[]')
			FROM item_cast ic JOIN performer p ON p.performer_key = ic.performer_key WHERE ic.item_key = c.item_key),
		'genres', (SELECT coalesce(json_agg(g.genre_label ORDER BY g.genre_label), '[]')
			FROM item_genre ig JOIN genre g ON g.genre_key = ig.genre_key WHERE ig.item_key = c.item_key))::text
		FROM catalog_item c JOIN lang l ON l.lang_key = c.lang_key`, nil},
	{"performer", `SELECT json_build_object('_id', performer_key, 'given_nm', given_nm, 'family_nm', family_nm)::text FROM performer`, nil},
	{"member", `SELECT json_build_object('_id', x.member_key, 'branch_key', x.branch_key, 'given_nm', x.given_nm,
		'family_nm', x.family_nm, 'contact_email', x.contact_email, 'is_active', x.is_active, 'joined_on', ` + date("x.joined_on") + `,
		'residence', ` + lendcoResidence + `)::text FROM member x ` + lendcoResidenceJoin, nil},
	{"employee", `SELECT json_build_object('_id', x.employee_key, 'given_nm', x.given_nm, 'family_nm', x.family_nm,
		'contact_email', x.contact_email, 'branch_key', x.branch_key, 'is_active', x.is_active, 'login_nm', x.login_nm,
		'residence', ` + lendcoResidence + `)::text FROM employee x ` + lendcoResidenceJoin, nil},
	{"branch", `SELECT json_build_object('_id', x.branch_key, 'supervisor_key', x.supervisor_key,
		'residence', ` + lendcoResidence + `)::text FROM branch x ` + lendcoResidenceJoin, nil},
	{"stock_unit", `SELECT json_build_object('_id', unit_key, 'item_key', item_key, 'branch_key', branch_key)::text FROM stock_unit`, []string{"item_key"}},
	{"loan", `SELECT json_build_object('_id', loan_key, 'loaned_at', ` + date("loaned_at") + `, 'unit_key', unit_key,
		'member_key', member_key, 'returned_at', ` + date("returned_at") + `, 'employee_key', employee_key)::text FROM loan`, []string{"member_key"}},
	{"charge", `SELECT json_build_object('_id', charge_key, 'member_key', member_key, 'employee_key', employee_key,
		'loan_key', loan_key, 'amt', amt, 'charged_at', ` + date("charged_at") + `)::text FROM charge`, []string{"member_key"}},
}

// The document datasets carry no description: mongodb-mcp-server has no
// place for one, so neither server gets it.

// StartPagilaDocs loads Pagila as MongoDB documents.
func StartPagilaDocs(ctx context.Context) (*Dataset, func(), error) {
	return startDocs(ctx, "pagila-docs", "pagila", StartPagila, pagilaDocs)
}

// StartLendcoDocs loads Lendco as MongoDB documents.
func StartLendcoDocs(ctx context.Context) (*Dataset, func(), error) {
	return startDocs(ctx, "lendco-docs", "lendco", StartLendco, lendcoDocs)
}

// startDocs starts the Postgres dataset (kept for reference answers) and a
// MongoDB 8 container, and copies the documents built by sources into db.
func startDocs(ctx context.Context, name, db string,
	start func(context.Context) (*Dataset, func(), error), sources []docSource) (*Dataset, func(), error) {
	pg, stopPG, err := start(ctx)
	if err != nil {
		return nil, nil, err
	}
	ctr, err := tcmongodb.Run(ctx, "mongo:8")
	stop := func() { _ = testcontainers.TerminateContainer(ctr); stopPG() }
	if err != nil {
		stop()
		return nil, nil, err
	}
	base, err := ctr.ConnectionString(ctx)
	if err != nil {
		stop()
		return nil, nil, err
	}
	u, err := url.Parse(base)
	if err != nil {
		stop()
		return nil, nil, err
	}
	u.Path = "/" + db
	dsn := u.String()
	client, err := mongo.Connect(options.Client().ApplyURI(dsn))
	if err != nil {
		stop()
		return nil, nil, err
	}
	defer client.Disconnect(context.Background())
	for _, s := range sources {
		if err := copyDocs(ctx, pg, client.Database(db).Collection(s.coll), s); err != nil {
			stop()
			return nil, nil, fmt.Errorf("%s.%s: %w", name, s.coll, err)
		}
	}
	return &Dataset{Name: name, Engine: "mongodb", DSN: dsn, DB: pg.DB}, stop, nil
}

func copyDocs(ctx context.Context, pg *Dataset, coll *mongo.Collection, s docSource) error {
	rows, err := pg.DB.QueryContext(ctx, s.sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	var batch []any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := coll.InsertMany(ctx, batch)
		batch = batch[:0]
		return err
	}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return err
		}
		var d bson.D
		if err := bson.UnmarshalExtJSON([]byte(text), false, &d); err != nil {
			return fmt.Errorf("document %.80s: %w", text, err)
		}
		batch = append(batch, d)
		if len(batch) == 1000 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	for _, f := range s.indexes {
		if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: f, Value: 1}}}); err != nil {
			return err
		}
	}
	return nil
}

// RunTwin runs a question's MongoDB twin and returns its rows: each
// document's values in field order (the twin's final $project fixes the
// order and drops _id).
func RunTwin(ctx context.Context, dsn string, twin json.RawMessage) ([][]any, error) {
	var t struct {
		Collection string `bson:"collection"`
		Pipeline   bson.A `bson:"pipeline"`
	}
	if err := bson.UnmarshalExtJSON(twin, false, &t); err != nil {
		return nil, err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	client, err := mongo.Connect(options.Client().ApplyURI(dsn))
	if err != nil {
		return nil, err
	}
	defer client.Disconnect(context.Background())
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cur, err := client.Database(u.Path[1:]).Collection(t.Collection).Aggregate(ctx, t.Pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out [][]any
	for cur.Next(ctx) {
		elems, err := cur.Current.Elements()
		if err != nil {
			return nil, err
		}
		row := make([]any, 0, len(elems))
		for _, e := range elems {
			row = append(row, plain(e.Value()))
		}
		out = append(out, row)
	}
	return out, cur.Err()
}

// plain converts a BSON value to what Equal compares: numbers as int64 or
// float64, decimals and strings as strings, dates as time.Time.
func plain(v bson.RawValue) any {
	switch v.Type {
	case bson.TypeInt32:
		return int64(v.Int32())
	case bson.TypeInt64:
		return v.Int64()
	case bson.TypeDouble:
		return v.Double()
	case bson.TypeDecimal128:
		return v.Decimal128().String()
	case bson.TypeString:
		return v.StringValue()
	case bson.TypeBoolean:
		return v.Boolean()
	case bson.TypeDateTime:
		return time.UnixMilli(v.DateTime()).UTC()
	case bson.TypeNull, bson.TypeUndefined:
		return nil
	}
	return v.String()
}
```

In `datasets.go`, `StartDataset`, add the two cases and update the error message:

```go
	case "pagila-docs":
		return StartPagilaDocs(ctx)
	case "lendco-docs":
		return StartLendcoDocs(ctx)
	}
	return nil, nil, fmt.Errorf("unknown dataset %q (want pagila, sakila, chinook, lendco, pagila-docs or lendco-docs)", name)
```

- [ ] **Step 5: Write the question files**

Create `questions/pagila-docs.json` and `questions/lendco-docs.json`. Each entry copies its SQL twin (`pagila.json` or `lendco.json`): the same `kind`, `question`, `sql` and `ordered`. Its id becomes `pagila-docs-NN` or `lendco-docs-NN`. Add a `mongo` twin over the document model above.

Rules for twins:
- One `aggregate` pipeline on one collection; `$lookup` reaches the others.
- End with a `$project` that drops `_id` (`"_id": 0`) and lists the answer's columns, in the order the question asks for them.
- Reproduce the SQL's rounding with `$round`, and its tie handling. For questions like "the X with the most Y", where the SQL keeps every row tied at the maximum, keep every tied row.

Example twins (pagila-docs):

```json
{"id":"pagila-docs-01","kind":"lookup","question":"…","sql":"SELECT count(*) FROM film",
 "mongo":{"collection":"films","pipeline":[{"$count":"n"},{"$project":{"_id":0,"n":1}}]}}
{"id":"pagila-docs-02", …,
 "mongo":{"collection":"customers","pipeline":[{"$match":{"first_name":"MARY","last_name":"SMITH"}},{"$project":{"_id":0,"email":1}}]}}
{"id":"pagila-docs-06", …,
 "mongo":{"collection":"films","pipeline":[{"$match":{"actors":{"$elemMatch":{"first_name":"PENELOPE","last_name":"GUINESS"}}}},{"$project":{"_id":0,"title":1}}]}}
{"id":"pagila-docs-21", …,
 "mongo":{"collection":"rentals","pipeline":[{"$match":{"return_date":null}},{"$count":"n"},{"$project":{"_id":0,"n":1}}]}}
{"id":"pagila-docs-24", …,
 "mongo":{"collection":"rentals","pipeline":[
   {"$lookup":{"from":"inventory","localField":"inventory_id","foreignField":"_id","as":"inv"}},{"$unwind":"$inv"},
   {"$lookup":{"from":"films","localField":"inv.film_id","foreignField":"_id","as":"film"}},{"$unwind":"$film"},
   {"$match":{"film.categories":"Horror"}},{"$group":{"_id":"$customer_id"}},{"$count":"n"},{"$project":{"_id":0,"n":1}}]}}
```

Example twins (lendco-docs):

```json
{"id":"lendco-docs-05", …,
 "mongo":{"collection":"catalog_item","pipeline":[{"$match":{"item_title":"ALIEN CENTER"}},{"$project":{"_id":0,"lang_label":1}}]}}
{"id":"lendco-docs-26", …,
 "mongo":{"collection":"charge","pipeline":[{"$group":{"_id":null,"total":{"$sum":"$amt"},"members":{"$addToSet":"$member_key"}}},
   {"$project":{"_id":0,"v":{"$round":[{"$divide":["$total",{"$size":"$members"}]},2]}}}]}}
```

Fill in `question`, `sql` and the rest of each entry from the SQL twin, and write a twin for every one of the 60 questions. `TestDocsTwinsMatchTruth` is the oracle: a twin is right when it returns the reference SQL's answer. When one disagrees, compare the SQL with your pipeline step by step. Do not change the SQL or the question.

Rounding needs care: Postgres `round(numeric, 2)` rounds half away from zero, while `$round` on doubles rounds half to even. If a value lands exactly on a half, compute the twin on `$toDecimal` values and round that.

- [ ] **Step 6: Run the tests**

```bash
cd bench
go get go.mongodb.org/mongo-driver/v2@latest github.com/testcontainers/testcontainers-go/modules/mongodb@$(go list -m -f '{{.Version}}' github.com/testcontainers/testcontainers-go)
go vet ./... && go test ./harness -run 'Docs|QuestionFiles' -count=1
go test ./... -short -count=1
```

Expected: PASS. `TestDocsTwinsMatchTruth` takes a few minutes: it downloads Pagila once and loads two Postgres and two MongoDB containers.

- [ ] **Step 7: Commit**

```bash
git add bench
git commit -m "bench: Pagila and Lendco as MongoDB documents, with twin answers

Each question gets an aggregate twin that must return the reference SQL's
answer, so the document datasets provably answer all 60 questions.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 9: Bench: mongodb-mcp-server as the rival

All paths in this task are under `bench/`.

**Files:**
- Modify: `bench/harness/servers.go` (`StartMongoMCP`; rename `dbhubEnv` to `nodeEnv`)
- Modify: `bench/harness/runner.go` (`Config.Rival` and `Config.RivalVersion`; tool-token and micro datasets per rival)
- Modify: `bench/harness/report.go` (`Report.Rival` and `Report.RivalVersion`; no hard-coded `dbhub`)
- Modify: `bench/harness/micro.go` (`StartGeneratedMongo`; `SchemaCall` and `QueryCall` for `mongodb-mcp`)
- Modify: `bench/cmd/bench/main.go` (`-rival` and `-rival-version`, plus defaults that follow the rival)
- Modify: the tests that break: `bench/harness/{report,runner,servers,micro}_test.go`
- Modify: `bench/README.md`

**Interfaces:**
- Consumes: `StartPagilaDocs`, `StartLendcoDocs`, `Question.Mongo` (Task 8); `connect`, `FindTool`, `ColdSchema`, `QueryLatency` (existing).
- Produces:
  - `const DefaultMongoMCPVersion = "3.0.5"`;
  - `StartMongoMCP(ctx, version string, conns []Conn) (Server, error)`;
  - `StartGeneratedMongo(ctx, n int) (*Dataset, func(), error)`;
  - `Config.Rival` and `Config.RivalVersion`, which replace `Config.DBHubVersion`;
  - `Report.Rival` and `Report.RivalVersion`, which replace `Report.DBHubVersion`;
  - `pairedRuns(results []Result, rival string)`.

  An empty `Rival` means `"dbhub"`, so old reports and the existing tests keep their meaning.

- [ ] **Step 1: Write the failing tests**

Add to `bench/harness/report_test.go`:

```go
func TestReportNamesTheRival(t *testing.T) {
	rep := &Report{Date: "2026-10-12", Model: "m", Runs: 1, Rival: "mongodb-mcp", RivalVersion: "3.0.5",
		ToolTokens: map[string][]int64{"qh": {400}, "mongodb-mcp": {4000}}, ColdSchema: map[string]time.Duration{"qh": 2 * time.Second},
		LatencyP50: map[string]time.Duration{}, LatencyP95: map[string]time.Duration{},
		Results: []Result{
			{Dataset: "pagila-docs", Server: "qh", Question: "q1", Kind: "lookup", Run: 1, Correct: true, InputTokens: 600, OutputTokens: 100, ToolCalls: 1},
			{Dataset: "pagila-docs", Server: "mongodb-mcp", Question: "q1", Kind: "lookup", Run: 1, Correct: true, InputTokens: 1800, OutputTokens: 200, ToolCalls: 3},
		}}
	md := rep.Markdown()
	if strings.Contains(md, "dbhub") || !strings.Contains(md, "# qh vs mongodb-mcp benchmark") || !strings.Contains(md, "mongodb-mcp 3.0.5") {
		t.Fatalf("report:\n%s", md)
	}
	var tokens, cold Criterion
	for _, c := range Criteria(rep) {
		switch c.Name {
		case "tokens":
			tokens = c
		case "cold schema":
			cold = c
		}
	}
	if !tokens.Known || !tokens.Pass || tokens.Target != "≥ 40% fewer than mongodb-mcp" {
		t.Errorf("tokens criterion: %+v", tokens)
	}
	if cold.Label != "Cold schema build, 1,000 collections" || cold.Target != "< 5 s" || !cold.Pass {
		t.Errorf("cold criterion: %+v", cold)
	}
}
```

Add to `bench/harness/servers_test.go`:

```go
func TestStartMongoMCP(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker, npx and the npm registry")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not installed")
	}
	ctx := context.Background()
	gen, stop, err := StartGeneratedMongo(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	srv, err := StartMongoMCP(ctx, DefaultMongoMCPVersion, []Conn{ConnFor(gen)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	for _, prefix := range []string{"list-collections", "find", "aggregate"} {
		if FindTool(srv.Tools(), prefix) == "" {
			t.Errorf("no %s tool in %v", prefix, ToolDefs(srv.Tools()))
		}
	}
	for _, tl := range srv.Tools() {
		if strings.Contains(tl.Name, "insert") || strings.Contains(tl.Name, "delete") || strings.Contains(tl.Name, "update") {
			t.Errorf("write tool %s registered despite --readOnly", tl.Name)
		}
	}
	name, args := SchemaCall("mongodb-mcp", gen)
	out, isErr := srv.Call(ctx, FindTool(srv.Tools(), name), args)
	if isErr || !strings.Contains(out, "t0001") {
		t.Fatalf("list-collections: %v %s", isErr, out)
	}
	if _, err := StartMongoMCP(ctx, DefaultMongoMCPVersion, []Conn{ConnFor(gen), ConnFor(gen)}); err == nil {
		t.Error("two connections accepted")
	}
}
```

`SchemaCall` changes signature in this task, to `SchemaCall(server string, ds *Dataset)`. Update its existing callers and tests.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd bench && go test ./harness -run 'Rival|MongoMCP' -count=1`

Expected: a compile failure. `Report.Rival` and `StartMongoMCP` are undefined.

- [ ] **Step 3: Start the rival**

In `servers.go`, rename `dbhubEnv` to `nodeEnv`, keep its doc comment, and add a sentence: "It drops MDB_MCP_* variables too, so a stray setting cannot change mongodb-mcp-server either." The filter already drops them. Update the existing call site. Then add:

```go
// DefaultMongoMCPVersion is the mongodb-mcp-server npm version benchmarked.
const DefaultMongoMCPVersion = "3.0.5"

// StartMongoMCP serves MongoDB's official MCP server over stdio through npx:
// read-only, telemetry off, with the connection string in its environment.
// It serves one connection per process.
func StartMongoMCP(ctx context.Context, version string, conns []Conn) (Server, error) {
	if len(conns) != 1 || conns[0].Engine != "mongodb" {
		return nil, errors.New("start mongodb-mcp-server: it takes exactly one MongoDB connection per process")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		return nil, errors.New("start mongodb-mcp-server: npx is not installed")
	}
	cmd := exec.Command("npx", "-y", "mongodb-mcp-server@"+version, "--readOnly", "--telemetry", "disabled")
	cmd.Env = append(nodeEnv(os.Environ()), "MDB_MCP_CONNECTION_STRING="+conns[0].DSN)
	return connect(ctx, "mongodb-mcp", cmd, func() {})
}
```

- [ ] **Step 4: Make the runner rival-aware**

In `runner.go`:

```go
type Config struct {
	Model, Effort                    string
	Rival, RivalVersion              string // "dbhub" (default) or "mongodb-mcp"
	Datasets, Servers                []string
	// … the remaining fields unchanged
}

// rival returns cfg.Rival, defaulting to dbhub.
func (cfg Config) rival() string {
	if cfg.Rival == "" {
		return "dbhub"
	}
	return cfg.Rival
}
```

The changes:
- `DefaultDeps`: `StartServer` dispatches `"qh"` → `StartQH`; `"dbhub"` → `StartDBHub(ctx, cfg.RivalVersion, conns)`; `"mongodb-mcp"` → `StartMongoMCP(ctx, cfg.RivalVersion, conns)`.
- `RunBenchmark`: set `Rival: cfg.rival(), RivalVersion: cfg.RivalVersion` on the report. The aborted-runs note uses `cfg.rival()` in place of the literal `dbhub`: `"%d qh and %d %s runs were aborted …"`.
- `failureNotes(results)` becomes `failureNotes(results, rival)`, using the rival name in place of `"dbhub"`.
- `toolTokens`:
  - The registered dataset is `"chinook"` for dbhub and `"pagila-docs"` for mongodb-mcp.
  - For mongodb-mcp, measure only k = 1; it serves one connection per process. Add the report note "mongodb-mcp-server serves one connection per process; its tool tokens are measured with one".
- `micro`:
  - The cold-schema database is `StartGenerated(ctx, 1000)` for dbhub and `StartGeneratedMongo(ctx, 1000)` for mongodb-mcp.
  - The latency dataset is `"pagila"` for dbhub and `"pagila-docs"` for mongodb-mcp. For mongodb-mcp, the queries are the questions' `Mongo` twins (as strings) in place of their SQL.
  - The cold-schema notes depend on the rival. For mongodb-mcp: "Cold schema: qh samples 100 documents from each of 1,000 collections and infers fields and references; mongodb-mcp-server's list-collections returns names only." Keep the second note ("Cold schema is the first schema call after process start …") for both.

In `micro.go`:

```go
// SchemaCall is the schema-discovery call measured for a server: qh's
// schema overview; dbhub's search_objects for tables at summary detail;
// mongodb-mcp-server's list-collections.
func SchemaCall(server string, ds *Dataset) (string, json.RawMessage) {
	switch server {
	case "qh":
		return "schema", json.RawMessage(fmt.Sprintf(`{"source":%q}`, ds.Name))
	case "mongodb-mcp":
		return "list-collections", json.RawMessage(fmt.Sprintf(`{"database":%q}`, mongoDB(ds)))
	}
	return "search_objects", json.RawMessage(`{"object_type":"table","detail_level":"summary","limit":1000}`)
}

// QueryCall is the query tool and argument shape for a server. On a MongoDB
// dataset q is a question's twin ({"collection", "pipeline"}): qh gets it as
// db.<collection>.aggregate(<pipeline>), mongodb-mcp-server through its
// aggregate tool.
func QueryCall(server string, ds *Dataset) (string, func(q string) json.RawMessage) {
	switch {
	case server == "qh" && ds.Engine == "mongodb":
		return "query", func(q string) json.RawMessage {
			var t struct {
				Collection string          `json:"collection"`
				Pipeline   json.RawMessage `json:"pipeline"`
			}
			_ = json.Unmarshal([]byte(q), &t)
			b, _ := json.Marshal(map[string]string{"source": ds.Name, "query": fmt.Sprintf("db[%q].aggregate(%s)", t.Collection, t.Pipeline)})
			return b
		}
	case server == "qh":
		return "query", func(q string) json.RawMessage {
			b, _ := json.Marshal(map[string]string{"source": ds.Name, "query": q})
			return b
		}
	case server == "mongodb-mcp":
		return "aggregate", func(q string) json.RawMessage {
			var t struct {
				Collection string          `json:"collection"`
				Pipeline   json.RawMessage `json:"pipeline"`
			}
			_ = json.Unmarshal([]byte(q), &t)
			b, _ := json.Marshal(map[string]any{"database": mongoDB(ds), "collection": t.Collection, "pipeline": t.Pipeline})
			return b
		}
	}
	return "execute_sql", func(q string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"sql": q})
		return b
	}
}

// mongoDB is the database named in a MongoDB dataset's connection string.
func mongoDB(ds *Dataset) string {
	u, err := url.Parse(ds.DSN)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

// StartGeneratedMongo creates n collections t0001… of 100 documents each,
// with a mix of scalar, nested and array fields, in database "gen".
func StartGeneratedMongo(ctx context.Context, n int) (*Dataset, func(), error) {
	ctr, err := tcmongodb.Run(ctx, "mongo:8")
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return nil, nil, err
	}
	base, err := ctr.ConnectionString(ctx)
	if err != nil {
		stop()
		return nil, nil, err
	}
	u, _ := url.Parse(base)
	u.Path = "/gen"
	client, err := mongo.Connect(options.Client().ApplyURI(u.String()))
	if err != nil {
		stop()
		return nil, nil, err
	}
	defer client.Disconnect(context.Background())
	sem := make(chan struct{}, 8)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			docs := make([]any, 100)
			for k := range docs {
				docs[k] = bson.D{{Key: "_id", Value: int32(k + 1)}, {Key: "name", Value: fmt.Sprintf("item %d", k)},
					{Key: "status", Value: []string{"new", "open", "closed"}[k%3]}, {Key: "amount", Value: float64(k) * 1.25},
					{Key: "created_at", Value: bson.NewDateTimeFromTime(time.Unix(int64(1700000000+k), 0))},
					{Key: "owner", Value: bson.D{{Key: "name", Value: "o"}, {Key: "email", Value: "o@example.com"}}},
					{Key: "tags", Value: bson.A{"a", "b"}}}
			}
			if _, err := client.Database("gen").Collection(fmt.Sprintf("t%04d", i)).InsertMany(ctx, docs); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		stop()
		return nil, nil, err
	}
	return &Dataset{Name: "gen", Engine: "mongodb", DSN: u.String()}, stop, nil
}
```

Update the micro call sites for the new signatures: `SchemaCall(server, gen)` and `QueryCall(server, ds)`. Then run `npx -y mongodb-mcp-server@3.0.5 --readOnly --telemetry disabled` once by hand and list its tools, or rely on `TestStartMongoMCP`'s failure output, to confirm the tool names `list-collections`, `find` and `aggregate` and their argument names (`database`, `collection`, `pipeline`). If they differ in 3.0.5, use the real ones and note the change in your report.

- [ ] **Step 5: Make the report rival-aware**

In `report.go`:
- Rename `Report.DBHubVersion` to `RivalVersion` and add `Rival string`. Add `func (r *Report) rival() string`, which returns `"dbhub"` when `Rival` is empty.
- `pairedRuns(results)` becomes `pairedRuns(results, rival)`. It pairs `"qh"` with `rival` in place of `"dbhub"`. Update every caller.
- `Criteria` changes:
  - Every target that names dbhub uses `r.rival()`: `"≥ 40% fewer than " + rival`, `"≥ " + rival` and `"fewer than " + rival`.
  - The cold-schema criterion depends on the rival. For `"mongodb-mcp"`, it uses `Label: "Cold schema build, 1,000 collections"`, `Target: "< 5 s"` and `Pass: d < 5*time.Second`. It is unchanged for dbhub.
- `Markdown` changes:
  - the title becomes `# qh vs <rival> benchmark — <date>`;
  - the header line says `<rival> <RivalVersion>` where it said `dbhub <version>`;
  - the column headers of the tool-token, micro-benchmark and per-question tables use the rival name;
  - the cold-schema row label reads `1,000 collections` for mongodb-mcp;
  - the per-question helpers that index `"dbhub"` use the rival.
- `Summarize`'s sort comment ("qh before dbhub") becomes "qh before its rival". The comparison itself works on any name if it sorts `"qh"` first explicitly. Check that it does, and fix it if it relies on `"qh" > "dbhub"`.

`grep -n dbhub harness/*.go` must then show only `StartDBHub`, `DefaultDBHubVersion`, comments about dbhub's own behaviour, and the `"dbhub"` default.

- [ ] **Step 6: Update the command**

`cmd/bench/main.go`:

```go
	var datasets, servers, out, rival, rivalVersion string
	flag.StringVar(&rival, "rival", "dbhub", "server qh is compared with: dbhub (SQL datasets) or mongodb-mcp (MongoDB datasets)")
	flag.StringVar(&rivalVersion, "rival-version", "", "the rival's npm version (default: the pinned one)")
	flag.StringVar(&datasets, "datasets", "", "datasets to run (default: pagila,sakila,chinook,lendco for dbhub; pagila-docs,lendco-docs for mongodb-mcp)")
	flag.StringVar(&servers, "servers", "", "servers to compare (default: qh and the rival)")
```

After `flag.Parse()`:

```go
	sqlSets, docSets := []string{"pagila", "sakila", "chinook", "lendco"}, []string{"pagila-docs", "lendco-docs"}
	allowed := sqlSets
	switch rival {
	case "dbhub":
		cfg.RivalVersion = harness.DefaultDBHubVersion
	case "mongodb-mcp":
		cfg.RivalVersion, allowed = harness.DefaultMongoMCPVersion, docSets
	default:
		usage(fmt.Errorf("-rival: unknown value %q (use dbhub or mongodb-mcp)", rival))
	}
	cfg.Rival = rival
	if rivalVersion != "" {
		cfg.RivalVersion = rivalVersion
	}
	if datasets == "" {
		datasets = strings.Join(allowed, ",")
	}
	if servers == "" {
		servers = "qh," + rival
	}
	var err error
	if cfg.Datasets, err = parseList("datasets", datasets, allowed...); err != nil {
		usage(err)
	}
	if cfg.Servers, err = parseList("servers", servers, "qh", rival); err != nil {
		usage(err)
	}
```

Remove the old `-dbhub-version` flag and its parsing. The package comment becomes "Command bench runs the qh benchmark against dbhub or mongodb-mcp-server (spec §9.2)."

- [ ] **Step 7: Document**

In `bench/README.md`:
- Change the opening line to name both rivals.
- Add the following section after "Running":

````markdown
## MongoDB

```bash
make bench ARGS="-rival mongodb-mcp -limit 1 -runs 1"   # smoke run first
make bench ARGS="-rival mongodb-mcp"                    # pagila-docs and lendco-docs, 3 runs
```

- **Rival:** MongoDB's official [`mongodb-mcp-server`](https://github.com/mongodb-js/mongodb-mcp-server), pinned to 3.0.5 and run with `--readOnly` and telemetry off.
- **Datasets:** `pagila-docs` and `lendco-docs` are Pagila and Lendco modelled as documents.
  - Addresses are embedded, films embed their actors and categories, and rentals and payments reference other collections by key.
  - They reuse the 60 SQL questions. Every question has an aggregate twin that a test checks against the SQL reference answer.
- **Fairness:**
  - Neither server gets a dataset description, because mongodb-mcp-server has no place for one.
  - Both servers cap results at 100 rows or documents by default.
  - mongodb-mcp-server serves one connection per process, so its tool-definition tokens are measured with one connection.
- **Cost:** about 360 conversations (60 questions × 3 runs × 2 servers), roughly half a SQL run.
````

- [ ] **Step 8: Run the tests and a free micro run**

```bash
cd bench
go vet ./... && go test ./... -short -count=1
go test ./harness -run 'MongoMCP|Docs' -count=1
make bench ARGS="-rival mongodb-mcp -skip-agent -skip-tokens"
```

Expected:
- every test passes;
- the `make` run writes `results/<timestamp>/report.md`, with a cold-schema figure for qh that passes `< 5 s` and query latency for both servers.

That `make` run needs no API key. Delete the results directory it created; benchmark results are committed only from the real runs, after merge approval.

- [ ] **Step 9: Commit**

```bash
git add bench
git commit -m "bench: compare qh with mongodb-mcp-server on the document datasets

The rival is now a setting: dbhub for the SQL datasets, mongodb-mcp for
pagila-docs and lendco-docs. Reports, criteria and micro-benchmarks name
whichever rival ran.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## After the last task (controller, not a subagent)

The paid benchmark is not part of any task. After the final review and the PR:
1. Ask the user before spending API credit.
2. Run `make bench ARGS="-rival mongodb-mcp -limit 1 -runs 1"` (smoke run), then `make bench ARGS="-rival mongodb-mcp"` (full run), with the key from `.env.local` as in earlier runs.
3. Commit `bench/results/<timestamp>/` and report the four spec §1 criteria.
