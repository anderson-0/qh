# qh Plan 6: Close the token gap — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut what qh adds to every model turn so the benchmark's last failing criterion passes: at least 40% fewer total tokens per task than dbhub.

**Architecture:** The tool definitions and the MCP instructions are re-sent on every model turn. This plan makes three changes:

- Remove the `sources` tool and fold its job into `schema`. Calling `schema` with no arguments in a workspace that does not have exactly one connection lists the connections.
- Shorten the fixed instruction sentence.
- Clip connection descriptions in the instructions.

Nothing changes in core, the engines, REST or the CLI.

**Tech Stack:** Go 1.25, `github.com/modelcontextprotocol/go-sdk` v1.8.0, the `bench/` harness (Anthropic Go SDK).

**Spec:** `docs/superpowers/specs/2026-10-09-qh-design.md`. The spec sections that matter here:

- §1 success criteria;
- §3.1 tools;
- §9.2 benchmark.

## Why (measured)

The last full run (`bench/results/2026-10-10T223215`, 360 paired runs) passed every criterion except tokens. qh used 38.2% fewer tokens than dbhub, against a target of ≥ 40%:

| Dataset | qh total tokens per task | dbhub |
|---|---|---|
| Pagila | 2,484 | 2,843 |
| Sakila | 2,403 | 2,885 |
| Chinook | 2,746 | 3,281 |
| Lendco | 5,176 | 11,710 |

On the three memorised datasets both servers answer in about one call. qh's lead there comes only from per-turn overhead. With `count_tokens`, the tool definitions plus the instructions (one Pagila connection) cost 453 tokens per turn:

| Part | Tokens per turn |
|---|---|
| `query` tool | 152 |
| `schema` tool | 159 |
| `sources` tool | 48 |
| connection line with Pagila's 86-character description | ≈ 70, of which the description is 37 |
| fixed sentence "Query directly if you know the names; otherwise call schema first. Errors on unknown names suggest close matches." | 34 |

Removing `sources` saves 48 tokens per turn. Shortening the sentence saves about 18, and clipping descriptions to 60 characters about 10. That is about 76 tokens per turn, or about 190 per task at qh's 2.5 turns per task. The estimate is about 42% fewer tokens than dbhub, and Task 3 measures it.

The sentence's first half stays. In the baseline run the instructions told the model to call `sources` and then `schema`, and it made 2.1 calls per question on Pagila. With "Query directly if you know the names" it makes 1.1. One extra call costs far more than the 15 tokens the clause takes.

## Spec adjustments

1. **§3.1 has two tools, not three.** Calling `schema` with no `source`, `pattern` or `object` returns the connection list when the workspace does not have exactly one connection. The list uses the old `sources` lines under a one-line header. With exactly one connection it returns the overview, as now.
   - §1 still holds: an already-connected agent finds a new connection with `schema` and no arguments, with no tool-list change.
   - A query naming a connection the agent has never seen still gets `error[unknown_source]` with `Available: …`.
2. **§1:** "All three tool definitions together cost ≤ 800 tokens" becomes "All tool definitions together…". The limit is unchanged.
3. **§3.1 instructions:** they now name the connections (this was done on `feat/perf-tokens`). Task 1 Step 6 rewrites the spec paragraph that still says "call `sources`".

## Global Constraints

- Tool definitions and instructions together must stay ≤ 800 tokens. `TestToolList` also holds `tools/list` JSON to ≤ 2,400 bytes.
- MCP tool errors stay one line, `error[<category>]: <message>. <hint>` (spec §3.4), returned as `isError` results.
- Commit trailer, on every commit: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Branch: `feat/plan-6-token-trims`, from `main` after `feat/perf-tokens` is merged.
- The benchmark run in Task 3 is paid. Run the smoke run first, then exactly one full run.

## Review Focus

1. **Workspace with no connections.** `schema` with no arguments must return the "-- no connections" line, not an error (Task 1 test).
2. **A second connection added while a session is open.** The session's instructions still say "source may be omitted". `schema` with no arguments must now list both connections (Task 1 test). `query` without `source` must fail with `unknown_source` naming both; core already tests this in `TestEmptySourceNeedsExactlyOneConnection`.
3. **`pattern` or `object` without `source` when there are several connections.** This must not silently list the connections and drop the filter. It returns the `unknown_source` error (Task 1 test).
4. **Descriptions with newlines, tabs or multi-byte characters.** Clipping must collapse whitespace and never split a UTF-8 character (Task 2 test).
5. **More than 20 connections.** The instructions must point to `schema`, not to the removed `sources` tool (Task 2 test).

---

### Task 1: Fold `sources` into `schema`

**Files:**
- Modify: `internal/mcpserver/server.go` (the `sources` registration, the `schema` handler, the `New` doc comment)
- Test: `internal/mcpserver/server_test.go`
- Modify tests that call the `sources` tool:
  - `internal/httpapi/mcp_test.go:49-57`
  - `internal/httpapi/invariants_test.go:69-72`
  - `cmd/qh/serve_e2e_test.go:153-156`
  - `cmd/qh/e2e_test.go:57-61`
- Modify docs: `README.md:3-10`, `docs/superpowers/specs/2026-10-09-qh-design.md` (§1 and §3.1)

**Interfaces:**
- Consumes these existing core methods, unchanged:
  - `(*core.Service).ListConnections(ctx, ws) ([]*core.ConnInfo, error)`
  - `(*core.Service).Sources(ctx, ws) (string, error)`: one line per connection, `name engine mode — description`, sorted by name; with none, `-- no connections. Add one with \`qh conn add\`\n`
  - `(*core.Service).Schema(ctx, ws, core.SchemaInput) (string, error)`
- Produces: the constant `severalHeader = "-- several connections; pass one as source\n"` in package `mcpserver`. Task 2 does not use it.

- [ ] **Step 1: Write the failing tests**

In `internal/mcpserver/server_test.go`, make `connect` also return the service. Rename the body to `connectSvc` and keep `connect` as a wrapper so the other tests are untouched:

```go
func connect(t *testing.T) *mcp.ClientSession {
	s, _ := connectSvc(t)
	return s
}

func connectSvc(t *testing.T) (*mcp.ClientSession, *core.Service) {
	t.Helper()
	// ... the current body of connect, unchanged ...
	return session, svc
}
```

Change `TestToolList` to expect the two tools:

```go
	if strings.Join(names, ",") != "query,schema" && strings.Join(names, ",") != "schema,query" {
		t.Fatalf("tools = %v", names)
	}
```

In `TestTools`, delete the `sources` call (the first `if` in the function).

Add:

```go
func TestSchemaWithoutArgumentsListsConnectionsWhenSeveral(t *testing.T) {
	s, svc := connectSvc(t)
	ctx := context.Background()
	// One connection: the overview, as before.
	if out, isErr := call(t, s, "schema", map[string]any{}); isErr || !strings.HasPrefix(out, "main: 3 tables, 1 view\n") {
		t.Fatalf("one connection: %q", out)
	}
	// A second connection appears after the session started.
	if _, _, err := svc.CreateConnection(ctx, store.DefaultWorkspace, core.ConnInput{
		Name: "crm", Engine: "sqlite", DSN: testutil.SQLiteFixture(t), Description: "CRM",
	}, false); err != nil {
		t.Fatal(err)
	}
	if out, isErr := call(t, s, "schema", map[string]any{}); isErr || out != severalHeader+"crm sqlite ro — CRM\nshop sqlite ro\n" {
		t.Fatalf("two connections: %q", out)
	}
	// A filter without a source is not silently dropped.
	if out, isErr := call(t, s, "schema", map[string]any{"pattern": "*order*"}); !isErr || !strings.HasPrefix(out, "error[unknown_source]") {
		t.Fatalf("pattern without source: %q (isError %v)", out, isErr)
	}
	if out, isErr := call(t, s, "schema", map[string]any{"source": "crm", "object": "orders"}); isErr || !strings.Contains(out, "→ users.id") {
		t.Fatalf("explicit source: %q", out)
	}
}

func TestSchemaWithoutArgumentsAndNoConnections(t *testing.T) {
	s, svc := connectSvc(t)
	if err := svc.DeleteConnection(context.Background(), store.DefaultWorkspace, "shop"); err != nil {
		t.Fatal(err)
	}
	if out, isErr := call(t, s, "schema", map[string]any{}); isErr || !strings.HasPrefix(out, "-- no connections") {
		t.Fatalf("no connections: %q (isError %v)", out, isErr)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/mcpserver -count=1`

Expected: FAIL.

- `TestToolList` fails with `tools = [query schema sources]`.
- `TestSchemaWithoutArguments…` does not compile (`undefined: severalHeader`).

- [ ] **Step 3: Implement**

In `internal/mcpserver/server.go`:

- Delete the whole `mcp.AddTool(s, &mcp.Tool{Name: "sources", …})` block.
- Change the doc comment of `New` to `// New returns an MCP server with the schema and query tools.`
- Add the constant above `New`:

```go
// severalHeader introduces the connection list that schema returns, with no
// arguments, in a workspace that does not have exactly one connection.
const severalHeader = "-- several connections; pass one as source\n"
```

Replace the `schema` handler body:

```go
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a SchemaArgs) (*mcp.CallToolResult, any, error) {
		w := ws(ctx)
		if a.Source == "" && a.Pattern == "" && a.Object == "" {
			list, err := svc.ListConnections(ctx, w)
			if err != nil {
				return text("", err)
			}
			if len(list) != 1 {
				out, err := svc.Sources(ctx, w)
				if err == nil && len(list) > 1 {
					out = severalHeader + out
				}
				return text(out, err)
			}
		}
		return text(svc.Schema(ctx, w, core.SchemaInput{Source: a.Source, Pattern: a.Pattern, Object: a.Object}))
	})
```

Leave the `schema` description unchanged. "No arguments: tables with columns…" is still true for the one-connection case. With several connections, the instructions already say "pass one as source".

- [ ] **Step 4: Run the mcpserver tests to verify they pass**

Run: `go test ./internal/mcpserver -count=1`

Expected: PASS.

- [ ] **Step 5: Update the other tests that call `sources`**

`internal/httpapi/mcp_test.go`. Workspace `default` has one connection, `shop`; workspace `other` has none.

```go
	if out, isErr := mcpCall(t, e, mine, "schema", map[string]any{}); isErr || !strings.HasPrefix(out, "main: 3 tables, 1 view\n") {
		t.Fatalf("schema: %q", out)
	}
```

and

```go
	if out, _ := mcpCall(t, e, theirs, "schema", map[string]any{}); !strings.HasPrefix(out, "-- no connections") {
		t.Fatalf("other workspace sees: %q", out)
	}
```

`internal/httpapi/invariants_test.go`. The key sees only `mine`. Exactly one visible connection makes `schema` return its overview, not the several-connections list:

```go
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "schema", Arguments: map[string]any{}})
	if err != nil || res.IsError || !strings.HasPrefix(res.Content[0].(*mcp.TextContent).Text, "main: 3 tables, 1 view\n") {
		t.Fatalf("mcp schema: %v %+v", err, res)
	}
```

`cmd/qh/serve_e2e_test.go`:

```go
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "schema", Arguments: map[string]any{}})
	if err != nil || res.IsError || !strings.HasPrefix(res.Content[0].(*mcp.TextContent).Text, "main: 3 tables, 1 view\n") {
		t.Fatalf("mcp schema: %v %+v", err, res)
	}
```

`cmd/qh/e2e_test.go`. After `shop2` is added there are two connections:

```go
	if out := text("schema", map[string]any{}); !strings.Contains(out, "shop2 sqlite ro") {
		t.Fatalf("schema lists connections: %q", out)
	}
```

Run: `go test ./internal/httpapi ./cmd/qh -count=1`

Expected: PASS.

- [ ] **Step 6: Update the docs**

In `README.md`, replace the opening paragraph and the tool table with:

```markdown
A database server for AI agents, built to use as few tokens as possible. It exposes two MCP tools,
whatever the number of connections, and names the connections in its MCP instructions:

| Tool | What it does |
|---|---|
| `schema` | Overview of tables with their columns, keys and foreign keys, or full definitions with types, foreign keys in both directions, enum values and indexes. With no arguments in a workspace with several connections, lists the connections |
| `query` | Runs one statement and returns TSV, 100 rows by default, with a footer that says when rows were cut |
```

Replace the sentence "An HTTP server mode with a REST API, and a benchmark against dbhub are planned (see `docs/superpowers/plans/`)." with:

```markdown
`qh serve` adds an HTTP server mode with a REST API, and `bench/` compares qh with dbhub (see `bench/README.md`).
```

In the spec:

- In §1, change "All three tool definitions together cost ≤ 800 tokens" to "All tool definitions together cost ≤ 800 tokens".
- In §3.1, delete the `sources` row.
- Change the `schema` row's Returns cell to: "No arguments: the connection list (`name engine ro/rw — description` per line) when the workspace does not have exactly one connection, else the overview. `pattern`/`object`: detailed definitions of matching objects". Make `source` optional in both rows: "optional when the workspace has one connection".
- Replace the paragraph starting "The server's MCP `instructions` field carries one short workflow hint" with:

```markdown
The server's MCP `instructions` field carries one short hint (query directly
when the names are known, otherwise call `schema` first) and names the
workspace's connections, up to 20, each description clipped to 60
characters. They are built at each `initialize`, so a new session sees the
current connections.
```

- [ ] **Step 7: Run everything and commit**

Run: `go vet ./... && go test ./... -count=1`

Expected: PASS. The engine suites start Docker containers.

```bash
git add internal/mcpserver internal/httpapi cmd/qh README.md docs/superpowers/specs/2026-10-09-qh-design.md
git commit -m "feat(mcp): fold sources into schema

schema with no arguments lists the connections when the workspace does
not have exactly one, so the sources tool, 48 tokens on every model turn,
is gone. The instructions already name the connections.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Shorten the instructions

**Files:**
- Modify: `internal/mcpserver/server.go` (`instructions`, `connectionsLine`, plus a new `maxDescription` and `clipDescription`)
- Test: `internal/mcpserver/server_test.go` (`TestConnectionsLine`, `TestInstructionsNameConnections`)

**Interfaces:**
- Consumes: `connectionsLine(list []*core.ConnInfo) string` and `maxListed = 20`, both existing.
- Produces:
  - `const maxDescription = 60`
  - `func clipDescription(s string) string`

- [ ] **Step 1: Write the failing tests**

In `TestConnectionsLine`, change the capped expectation to point at `schema`:

```go
	if got := connectionsLine(list); !strings.HasSuffix(got, "db19 (postgres, rw); and 2 more (schema lists all).") {
		t.Errorf("capped: %q", got)
	}
```

Add:

```go
func TestClipDescription(t *testing.T) {
	long := "Pagila: a DVD rental store (films, actors, customers, rentals, payments) on PostgreSQL"
	if got := clipDescription(long); got != "Pagila: a DVD rental store (films, actors, customers, renta…" || utf8.RuneCountInString(got) != maxDescription {
		t.Errorf("long: %q", got)
	}
	if got := clipDescription("Billing\n\treplica"); got != "Billing replica" {
		t.Errorf("whitespace: %q", got)
	}
	multi := strings.Repeat("é", 70)
	if got := clipDescription(multi); !utf8.ValidString(got) || utf8.RuneCountInString(got) != maxDescription {
		t.Errorf("multi-byte: %q", got)
	}
	if got := clipDescription("short"); got != "short" {
		t.Errorf("short: %q", got)
	}
}
```

Add `"unicode/utf8"` to the test file's imports.

`TestInstructionsNameConnections` keeps its assertions. It compares against the `instructions` constant, and the fixture connection has no description.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/mcpserver -run 'TestConnectionsLine|TestClipDescription' -count=1`

Expected: FAIL.

- The build fails with `undefined: clipDescription`.
- Once that compiles, the capped suffix still reads "(sources lists all)".

- [ ] **Step 3: Implement**

In `internal/mcpserver/server.go`, replace the `instructions` constant:

```go
const instructions = "Query directly if you know the names; else call schema first."
```

Add below `maxListed`:

```go
// maxDescription caps each connection description in the instructions, in
// characters; the full text stays in the schema connection list.
const maxDescription = 60

// clipDescription collapses whitespace and cuts s to maxDescription
// characters, ending a cut one with "…".
func clipDescription(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxDescription {
		return s
	}
	return string(r[:maxDescription-1]) + "…"
}
```

In `connectionsLine`, use it in `describe` and point the overflow at `schema`:

```go
		if c.Description != "" {
			s += ": " + clipDescription(c.Description)
		}
```

```go
		line += fmt.Sprintf("; and %d more (schema lists all)", len(list)-maxListed)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/mcpserver -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver
git commit -m "perf(mcp): shorten the instructions and clip connection descriptions

The fixed hint drops the sentence about error suggestions (the errors say
so themselves) and each connection description in the instructions is
clipped to 60 characters; together about 28 fewer tokens per model turn.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Measure

This task changes no code. It runs the paid benchmark once and reports the result.

**Files:**
- Modify: `bench/README.md`, only if it names the `sources` tool. Check with `grep -n sources bench/README.md` and fix the wording if it does.

**Interfaces:**
- Consumes: the committed code from Tasks 1–2.
- Produces: `bench/results/<timestamp>/report.md`. It stays untracked, like the earlier runs.

- [ ] **Step 1: Smoke run**

From the repo root:

```bash
(set -a; . ./.env.local; set +a; make bench ARGS="-limit 2 -runs 1 -skip-micro")
```

Expected:

- `0` aborted and `0` errors in the "Results by dataset" table.
- qh tool definitions around 380 tokens. The previous run measured 457.

- [ ] **Step 2: Full run**

```bash
(set -a; . ./.env.local; set +a; make bench)
```

It takes about 15 minutes.

- [ ] **Step 3: Report**

Compare the new report's "Success criteria" and "Results by dataset" tables with `bench/results/2026-10-10T223215/report.md`. Report tokens, accuracy, tool calls and wall time per dataset to the user, whether or not the 40% criterion passes.

Do not tune further in this plan. If qh's accuracy falls below dbhub's on any dataset, or Pagila/Sakila calls rise above 1.2 per task, report that as a regression of Task 2's shorter hint. The fix to propose is restoring the old sentence.
