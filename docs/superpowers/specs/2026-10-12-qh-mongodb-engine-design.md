# qh — MongoDB engine (design spec)

Status: draft for review, 2026-10-12. This extends `2026-10-09-qh-design.md` and replaces its §4.4 ("NoSQL readiness") where they differ. That spec governs everything not covered here.

## 1. Purpose

Agents that work with MongoDB today reach it through MongoDB's official MCP server, `mongodb-mcp-server`. It registers about 20 tools, each with its own JSON-argument schema, and it returns Extended JSON documents. qh already wins on tokens against dbhub with two tools and TSV output. This plan brings the same approach to MongoDB, through the same two tools.

**Success criteria:**

- **Benchmark against `mongodb-mcp-server`** (§7):
  - at least 40% fewer total tokens per task;
  - accuracy at least equal to the rival's;
  - fewer tool calls per task.
- **The SQL surface does not change.** Tool definitions stay at 393 tokens, and a workspace with only SQL connections gets byte-identical tool definitions and instructions. A golden test proves both.
- **Cold schema build:** under 5 s on a generated database of 1,000 collections, at the default sample of 100 documents.
- **No restart needed:** a MongoDB connection created through the API can be queried by an already-connected agent on its next tool call.

## 2. Decisions

| Topic | Decision | Why |
|---|---|---|
| Query language | mongosh syntax, e.g. `db.orders.find({status: "paid"}).sort({total: -1}).limit(5)`. | Models write it fluently. Unquoted keys cost fewer tokens than the JSON-document form in v1 §4.4. |
| How it runs | A Go parser accepts a fixed subset (§4) and produces BSON for the official Go driver v2. No JavaScript is ever executed. | It is deterministic and needs no sandbox. The read/write class is known before contacting the server, and errors are exact. A JS engine (goja) or shelling out to `mongosh` would execute arbitrary code. |
| Read-write scope | Read-write connections allow document writes and admin calls (index and collection create/drop/rename, and `$out`/`$merge`). Read-only connections allow reads only. | This matches SQL read-write connections, which allow DDL. |
| Classification | `Engine` gains `Classify` and `ReferencedObjects`. Core calls them instead of `sqlx` directly. | Core then holds no SQL assumptions. SQL engines delegate to `sqlx`, so their behaviour does not change. |
| Namespaces | A namespace is a MongoDB database. | This mirrors Postgres schemas and MySQL databases, and `allowed_namespaces` works unchanged. |
| MCP surface | The tools and their descriptions are unchanged. When the workspace has a `mongodb` connection, one sentence is added to the instructions. | The token margin over dbhub is thin, so SQL-only workspaces must pay nothing. |
| Schema | Inferred from up to 100 sampled documents per collection, with verified references (§5). | MongoDB has no declared schema or foreign keys. Agents need field names, value hints and join targets. |
| Rival | `mongodb-mcp-server`, pinned to version 3.0.5. | It is the official server, and the one agents use today. |

## 3. The engine

### 3.1 Code layout

- **`internal/engine/mongodb/`**:
  - `mongodb.go`: registers the engine `mongodb`, with `Inspect`, `Open`, `CheckServerDSN` and `Classify`/`ReferencedObjects`.
  - `parse.go`: the lexer and parser of the mongosh subset (§4).
  - `command.go`: classification, and translation of a call into one database command.
  - `exec.go`: runs the command and flattens results.
  - `values.go`: cell rendering (§4.5).
  - `introspect.go`: builds the catalog (§5.1, §5.3).
  - `infer.go`: path and type inference (§5.2).
  - `errors.go`: maps driver errors to `qerr`.
- **`internal/engine/engine.go`:**
  - `Engine` gains `Classify(q string) (Access, error)` and `ReferencedObjects(q string) []string`.
  - `type Access = sqlx.Access` plus re-exported constants, which avoids an import cycle.
  - `Dialect()` moves to an optional interface, `SQLEngine`, which only SQL engines implement.
- **`internal/core/service.go`:** calls `t.eng.Classify` and `t.eng.ReferencedObjects` instead of `sqlx.Classify` and `sqlx.ReferencedTables`.
- **`internal/engine/enginetest/`:** the catalog checks split out as `RunCatalog`, which a document engine can pass. `Run` still calls `RunCatalog` plus its SQL checks.
- **`internal/mcpserver/server.go`:** adds the conditional instructions sentence (§6).
- **Dependency:** `go.mongodb.org/mongo-driver/v2`, the latest v2.9.x.

### 3.2 Connection

- **Accepted DSNs:** `mongodb://` and `mongodb+srv://`. `DSNFields` (structured fields) builds a `mongodb://` URL.
- **`Inspect`:**
  - Reports the first host, the port, the database in the path, and the user.
  - The redacted form replaces the password with `***`.
  - With several hosts, `Host` is the comma-joined list.
- **Pool:** `pool_size` sets the driver's `MaxPoolSize`.
- **Timeouts:** each operation runs under a context deadline equal to the connection timeout. The driver derives the command's `maxTimeMS` from that deadline, so the server stops the work first and the deadline is the backstop.
- **Dialling:** `spec.Dial`, when set, becomes the driver's `ContextDialer`, so netguard (server mode) and SSH tunnels apply to every host the driver contacts. SRV and TXT lookups use the system resolver; their results are still dialled through `spec.Dial`.
- **TLS:** `spec.TLS`, when set, replaces the URL's TLS options through `TLS.Config(host)`.
- **Passwords:** `spec.Password` (a resolved `password_ref`) replaces the URL's password. Errors are scrubbed of the password and the full DSN, the same way as for SQL engines.
- **Read preference and concerns:** the connection string's `readPreference` (with its tags and `maxStalenessSeconds`), `readConcernLevel` and write concern (`w`, `journal`) apply to every command. Reads, explain of a read, and introspection go by the read preference, and `find`, `aggregate`, `distinct` and `count` carry the read concern. Writes and admin calls carry the write concern and always go to the primary. The driver ignores `wtimeoutMS`.
- **Server mode (`CheckServerDSN`):** an allowlist.
  - Option names are lowercased exactly as the driver lowercases them, and non-ASCII names are refused.
  - Allowed: authentication (`authSource`, `authMechanism`); replica sets and topology (`replicaSet`, `directConnection`, `loadBalanced`, `srvMaxHosts`, `srvServiceName`); `tls` and `ssl`; read and write concerns and read preference; timeouts, pool sizes, compression, `appName` and `serverMonitoringMode`.
  - Everything else is refused, including the TLS options that read files and their `ssl*` aliases, `tlsInsecure`, `tlsAllowInvalid*` and `authMechanismProperties`. TLS material must come through qh's TLS fields instead.
  - `authMechanism` must be `SCRAM-SHA-1`, `SCRAM-SHA-256`, `PLAIN` or `MONGODB-X509`; the others take ambient credentials from the server host.
  - User info is split exactly as the driver splits it.
  - Errors never quote the DSN.
  - The driver's own HTTP requests (OCSP) go through `spec.Dial` too.
- **`Ping`:** runs `buildInfo` on `admin`, and returns `MongoDB <version>`.

### 3.3 Namespaces

- **Database in the DSN path** (`/shop`): that database is the only namespace introspected, and the default for `db.<coll>`. Without `allowed_namespaces`, `getSiblingDB` can still name other databases, and the server's authorisation applies.
- **No database in the path:**
  - every database except `admin`, `local` and `config` is listed;
  - the default for `db.<coll>` is `test`, as in mongosh;
  - a query names another database with `db.getSiblingDB("name").<coll>…`.
- **`allowed_namespaces`:** limits the databases introspected and the databases a query may name. A query naming another database is refused with `forbidden`, before qh contacts the server. So are `$out`, `$merge`, `$lookup` and `$unionWith` stages, at any depth, that name another database.

## 4. The query language

### 4.1 Shape

- Exactly one call per query: `db.<collection>.<method>(<args>)`, optionally followed by cursor modifiers. A trailing `;` is allowed; a second statement is an error.
- `db["odd-name"]` and `db.getCollection("odd-name")` reach names that are not identifiers.
- `db.getSiblingDB("x")` may come before the collection.
- Database-level calls: `db.getCollectionNames()`, `db.createCollection(name, opts)`.

### 4.2 Methods

| Kind | Methods | Access |
|---|---|---|
| Read | `find(filter, projection)`, `findOne(filter, projection)`, `aggregate(pipeline, options)` with no `$out`/`$merge` at any depth (including inside `$facet` and `$lookup` pipelines), `countDocuments(filter)`, `estimatedDocumentCount()`, `distinct(field, filter)`, `getIndexes()`, `db.getCollectionNames()` | Read |
| Cursor modifiers | On `find`: `.sort(spec)`, `.limit(n)`, `.skip(n)`, `.project(spec)` (or its alias `.projection(spec)`), `.hint(spec)`, `.collation(spec)`, `.count()` (becomes `countDocuments` with the filter). `findOne` takes only `.toArray()`, `.pretty()` and `.explain()`. `.toArray()` and `.pretty()` are accepted and ignored; `.explain()` or `.explain("…")` runs the explain path, always with `queryPlanner` verbosity | Same as the method |
| Write (read-write only) | `insertOne`, `insertMany`, `updateOne`, `updateMany`, `replaceOne`, `deleteOne`, `deleteMany`, `findOneAndUpdate`, `findOneAndReplace`, `findOneAndDelete`, `aggregate` with `$out` or `$merge` | Write |
| Admin (read-write only) | `createIndex`, `createIndexes`, `dropIndex`, `dropIndexes`, `drop`, `renameCollection`, `db.createCollection` | DDL |

- Any other method is refused with `invalid`, before qh contacts the server, and a "did you mean" is offered when one is within edit distance 2. This includes `db.runCommand`, `db.eval`, `mapReduce`, `bulkWrite` and `watch`.
- Explain is allowed on read-only connections for reads only. Its access is the access of the method it explains.

### 4.3 Literals

- **Objects and arrays:** unquoted identifier keys, quoted keys (`'` or `"`), keys starting with `$`, and trailing commas.
- **Strings:** `'…'` or `"…"`, with JSON escapes plus `\x` and `\u` escapes; surrogate pairs are checked. `\0` followed by a digit, and a backslash before a line break, are refused.
- **Other literals:**
  - numbers, which become int32, int64 or double the way mongosh reads them: integers that fit in 32 bits become int32;
  - `true`, `false` and `null`; `undefined` reads as null;
  - regex literals such as `/^ab/i`, which become a BSON regex.
- **Helpers:** `ObjectId("hex")`, `ISODate("…")`, `new Date("…")`, `Date("…")`, `NumberInt(n)`, `NumberLong(n|"n")`, `NumberDecimal("…")`, `UUID("…")`, `Timestamp(t, i)`, `MinKey()` and `MaxKey()`. `Date(n)` and `ISODate(n)` take milliseconds.
- **Duplicate keys** in an object are refused.
- **Comments:** `//` and `/* */` are skipped.
- **Refused:** any other expression, including arithmetic, variables, function literals, `new Date()` with no argument, and template strings. The error points at the first unsupported token.
- **Limits:** input must be valid UTF-8, and is capped at 256 KiB and nesting at 100 levels.

### 4.4 Row caps

- `find`: `limit = min(user limit, max_rows + 1)`. When the user gives no limit, the limit is `max_rows + 1`. A negative `.limit(n)` means `|n|`; a negative `.skip` is `invalid`.
- `aggregate`: a trailing `{$limit: max_rows + 1}` stage is added, except when the pipeline ends in `$out` or `$merge`.
- `distinct`: the result is cut to `max_rows + 1` values in memory.
- More rows than `max_rows` sets `Truncated`, as for SQL.

### 4.5 Results

- **Columns:** `_id` comes first, then the union of top-level keys across the returned documents, in the order they first appear.
- **Cells:**
  - strings and numbers print as they are; `Long` and `Decimal128` print exactly;
  - doubles print in plain decimal from 1e-6 to 1e21, and in exponent form otherwise;
  - booleans print `true` or `false`;
  - dates print in RFC 3339 (UTC, milliseconds);
  - ObjectId prints its hex;
  - UUID (binary subtype 4) prints the canonical form;
  - a regex prints `/p/f`;
  - timestamps print as `Timestamp(t, i)`, and MinKey and MaxKey as `MinKey` and `MaxKey`;
  - documents and arrays become compact relaxed Extended JSON, e.g. `{"city":"Lyon","zip":"69001"}`; one larger than 64 KiB renders only its leading elements;
  - other binary data, including subtype 3, becomes a binary cell (its byte size), as in SQL;
  - a key missing from a document and an explicit `null` both print as NULL.
- **`countDocuments`, `estimatedDocumentCount` and `.count()`:** one column, `count`.
- **`distinct`:** one column, named after the field.
- **`getIndexes` and `getCollectionNames`:** one row per index or collection.
- **Size:** a read stops at 16 MiB of documents, keeping at least one, and is marked truncated.
- **Writes:**
  - `RowsAffected` is the number inserted, modified plus upserted (for updates and replaces) or deleted;
  - `insertOne` and `insertMany` also return their `_id` values as rows, at most `max_rows` of them;
  - the `findOneAnd*` methods return the document, as a read does;
  - a write that fails part-way reports how much was applied.
- **Admin calls:** `RowsAffected = 0`, except `createIndex(es)`, which returns an `index` column with one row per index name.
- **Explain:** one row, one column `plan`, holding the compact JSON of `queryPlanner.winningPlan`.

### 4.6 Errors

| Situation | Category | Message |
|---|---|---|
| Parse error | `invalid` | `unexpected "}" at 1:34`, then the query line with a caret under the position |
| Unsupported method | `invalid` | `unsupported method "findd"`, with hint `did you mean find?` |
| Write or admin call on read-only | `read_only` | As for SQL |
| Database outside `allowed_namespaces` | `forbidden` | `database "x" is not allowed on this connection` |
| A read on a single collection returns no rows, and the collection does not exist | `query_error` with `UnknownTable` set | `collection "ordrs" does not exist`; core adds `did you mean orders?` from the catalog, as it does for SQL tables |
| Server error | `query_error` | The server's message, with code and codeName |
| `maxTimeMS` exceeded or context deadline | `timeout` | As for SQL |
| Authentication failure, network failure, server selection timeout | `connection_failed` | As for SQL. The pool is dropped and reopened, as for SQL. |

- **Existence check:** runs only after an empty result, using `listCollections` with a `name` filter, so successful queries pay nothing.
- **Unknown fields:** not flagged, because document fields are open-ended.

## 5. Schema inference

### 5.1 Building the catalog

The catalog uses the existing cache, TTL and shared invalidation (Plan 8).

- **Listing:** `listCollections` per allowed database, with `nameOnly` and `authorizedCollections`.
  - Collections, including time series, become `KindCollection`; views become `KindView`. Collections are listed before views.
  - `system.*` is skipped.
  - A database whose listing fails becomes an empty namespace; the catalog fails only when every database does.
- **Per collection:**
  - `count` gives `RowEstimate`;
  - `listIndexes` gives `Indexes`, in key order, with `unique`;
  - text, 2dsphere, hashed and TTL indexes label their columns: `bio:text`, `loc:2dsphere`, `k:hashed`, and ` ttl` after a TTL index's last column.
- **Sampling:**
  - Fields come from `$sample: {size: 100}`. For collections of 100 documents or fewer, a plain `find` with limit 100 is used instead.
  - Sampling runs 4 collections at a time, each with a 5 s timeout, and stops at 8 MiB of documents per collection.
  - A collection whose sampling fails or times out is listed with its count and indexes and no fields. The build still succeeds.
  - When the catalog's time limit is reached, the remaining collections are listed unsampled, with the comment `not sampled (time limit)`, and databases not yet listed appear empty.
- **Views** are sampled in the same way, through `find` with limit 100.

### 5.2 Fields

- **Paths:**
  - nested documents become dotted paths (`address.city`), to depth 4;
  - arrays of documents become `items[].sku`;
  - the parent of a nested path is also listed, with type `object` or `array<object>`.
  - At most 200 paths per collection, in first-seen order. The rest are summarised in the object's `Comment` as `…(+N fields)`.
- **Types:**
  - BSON type names: `string`, `int`, `long`, `double`, `decimal`, `bool`, `date`, `objectId`, `object`, `binData`, `uuid`, `regex` and `timestamp`;
  - arrays become `array<T>`, where `T` is the union of element types across documents;
  - mixed types become a union ordered by frequency, such as `int|string`;
  - `null` is not a type, and only affects nullability.
- **Nullability:** `Nullable = false` when the field is present and non-null in every sampled document. `_id` is the primary key. Every field is `Inferred = true`.
- **Sampled values:** a string field with at most 8 distinct values, seen in at least 50 sampled documents, and no sampled value longer than 40 characters, gets `Enum` set to those values plus a final `…`. The `…` tells the reader the list is what the sample contained, not a closed set.

### 5.3 References

MongoDB has no foreign keys. qh infers them so that `$lookup` targets show up as `→` and `←`, as SQL joins do.

- **Candidate:** a field named `<x>_id`, `<x>Id`, `<x>_key` or `<x>_ref`, whose sampled type matches the `_id` type of a target collection in the same database (case-insensitive). Embedded paths count too (`items[].product_id`).
  - Targets are tried in order: `<x>`, `<x>s`, the single collection ending in `_<x>`, then the single collection ending in `_<x>s`. Views are never targets.
  - Numeric `_id` types (int, long, double and decimal) match as one class.
- **Verification:** take up to 5 distinct sampled values per field, and run one `find({_id: {$in: values}})` per target collection, with a projection of `_id` only, 4 at a time. Keep the reference when at least 4 of 5 (or all, if fewer than 5) are found.
- Verified references become `ForeignKey` entries, and `Catalog.Link` fills `ReferencedBy` as for SQL.

### 5.4 The `schema` tool output

- **Overview (no arguments):** the existing compact form, one line per collection, with top-level fields only. Nested paths fold into their parent, as in `address{city,country,…}`, and keep their references (`items{product_id→products,…}`). References use `→`. Example: `customers ~599: _id PK, name, email, address{city,country,…}, store_id→stores`.
- **Detail (`object` or `pattern`):** the existing `writeObject` form. It shows:
  - every path with its type;
  - `NN`;
  - `→` and `←`;
  - sampled values;
  - the index line.
- **Format changes:** `format` changes only by folding dotted fields in the overview. No `(collection)` label is added: in a MongoDB connection every object is a collection unless marked `(view)`.

## 6. MCP and CLI

- **Tools:** the `schema` and `query` tools, and their descriptions and schemas, are unchanged.
- **Instructions:** when at least one connection in the workspace has engine `mongodb`, the connections line is followed by: `mongodb sources take mongosh syntax: db.<collection>.find(…)/aggregate([…]).` The sentence appears once, however many MongoDB connections there are.
- **CLI and REST:** `qh conn add NAME --engine mongodb --dsn mongodb://…`, and the REST equivalents, work as for other engines. Structured fields build a `mongodb://` URL. SSH tunnels, secret refs and TLS options apply unchanged.

## 7. Benchmark

- **Rival:** `mongodb-mcp-server@3.0.5` via `npx`, with `--readOnly`, the connection string, and telemetry disabled. Its tools need `connectionId: "preconfigured"`. Each server is the only tool source in its runs, and both get the same model, prompts, turn limit (30), output cap and token budget.
- **Datasets:** both are built by a Go loader from the existing Postgres benchmark containers.
  - **`pagila-docs`:** Pagila modelled the way a MongoDB user would model it.
    - Customers, stores and staff embed their address (with city and country).
    - Films embed `actors[]` (id and name) and `categories[]`, plus `language`.
    - `inventory`, `rentals` and `payments` are separate collections that reference others by `*_id`, with integer `_id` values taken from Pagila's keys.
  - **`lendco-docs`:** the same documents under Lendco's unfamiliar collection and field names.
- **Questions and answers:** the existing 30 Pagila and 30 Lendco questions and reference answers are reused. A dataset test runs each reference SQL against Postgres, and a hand-written aggregate twin against the MongoDB dataset, and requires equal answers. This proves every question is answerable from the documents.
- **Measurements:** the same as v1 §9.2:
  - tokens, tool calls, wall time and accuracy per task;
  - tool-definition tokens via the token-counting endpoint, measured for the rival with one connection;
  - a cold schema build micro-benchmark on a generated database of 1,000 collections. The rival's cold-schema call is `list-collections`, which returns names only, and it connects when it starts, so its figure excludes connecting.
- **Procedure:** a `-limit 1 -runs 1` smoke run first, then a full run: 60 questions × 3 runs × 2 servers, about 360 conversations. Results are committed under `bench/results/`.

## 8. Testing

- **Parser (no Docker):**
  - table tests for every method, modifier, helper and literal form;
  - classification tests, including `$out` and `$merge` nested in `$facet` and `$lookup`;
  - error positions and hints;
  - `FuzzParse`: the parser never panics, and every accepted input produces BSON that round-trips.
- **Engine (testcontainers, `mongo:7` and `mongo:8.2`; `mongo:8` (8.3) and 8.0 refuse Docker's 7.0 kernel, SERVER-121912):**
  - `enginetest.RunCatalog`;
  - queries and flattening against a fixture dataset in `testdata/`;
  - row caps and truncation;
  - `maxTimeMS` timeouts;
  - the unknown-collection error;
  - writes and admin calls on a read-write connection, and their refusal on read-only;
  - inference: types, unions, dotted paths, sampled values, verified and rejected references;
  - `spec.Dial` (netguard), TLS, and `CheckServerDSN`;
  - password redaction in errors.
- **Core:**
  - the read-only guard and cache invalidation through `Engine.Classify`;
  - `forbidden` for a database outside `allowed_namespaces`;
  - unknown-collection suggestions.
- **MCP:**
  - the golden test that SQL-only tool definitions and instructions are byte-identical to `main`;
  - the MongoDB sentence appears exactly once when a `mongodb` connection exists.
- **CLI:** `qh conn add --engine mongodb`, `qh schema` and `qh query` end to end.

## 9. Not in this version

- Change streams, transactions, GridFS and `bulkWrite`.
- Restricting `$where`, `$function` and `$accumulator` beyond what the server enforces. On read-only connections they are allowed as part of a read; operators who need to block them use a MongoDB role, or disable server-side JavaScript.
- Atlas Search and Vector Search stages: they pass through as ordinary aggregate stages, with no special schema support.
- Per-connection settings for sample size, depth or path caps.
- Reading JSON-Schema validators into the catalog.
