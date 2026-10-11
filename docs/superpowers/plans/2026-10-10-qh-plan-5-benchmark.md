# qh Plan 5: Benchmark against dbhub — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A reproducible benchmark, `bench/`, that measures qh against bytebase/dbhub on real sample databases. It reports tokens, tool calls, wall time, answer accuracy, tool-definition size and latency against the spec's success criteria.

**Architecture:**
- `bench/` is a separate Go module, so its dependencies (Anthropic SDK, dataset loaders) never reach the qh binary. It contains:
  - `harness`, a library;
  - `cmd/bench`, the runner.
- Datasets:
  - Pagila (Postgres) and Sakila (MySQL) load into testcontainers.
  - Chinook (SQLite) is a file.
  - Each is downloaded once into a cache and checked against a pinned SHA-256.
- Servers under test:
  - qh: the binary built from the parent module, run as `qh mcp`.
  - dbhub: a pinned `npx @bytebase/dbhub@<version>`, run over stdio.
  - Both are driven through the go-sdk MCP client.
- Agent loop:
  - It drives a Claude model through a small `Model` interface. In production the interface is backed by the Anthropic Go SDK; in tests by a scripted fake.
  - The loop exposes the server's tools plus a `submit_answer` tool.
  - The model's answer is compared with ground truth computed by a reference SQL query.
- Micro-benchmarks call the MCP tools directly, without a model.
- The runner writes `bench/results/<date>/report.md` and `raw.jsonl`.

**Tech Stack:**
- Go 1.27.
- Already used by qh:
  - `github.com/modelcontextprotocol/go-sdk` v1.8.0;
  - `github.com/testcontainers/testcontainers-go` v0.44.0, with the postgres and mysql modules;
  - `github.com/jackc/pgx/v5` v5.11.0;
  - `github.com/go-sql-driver/mysql` v1.10.1;
  - `modernc.org/sqlite` v1.60.1.
- New, and only in `bench/go.mod`: `github.com/anthropics/anthropic-sdk-go` (latest v1 at implementation time).
- Node 22.5 or later with `npx`, for dbhub.

**Spec:** `docs/superpowers/specs/2026-10-09-qh-design.md`:
- §1 (success criteria);
- §9.1, the tool-definition size test;
- §9.2 (benchmark).

## Plan series

| Plan | Delivers |
|---|---|
| 1–4 | Done: local MVP, MySQL/MariaDB, server mode, operations (TLS, fields, query log, rotation, remote CLI) |
| **5 (this plan)** | `bench/`: datasets, 90 questions with ground truth, qh and dbhub drivers, agent harness, token counting, micro-benchmarks, report |

## Spec adjustments made while planning

1. **Answers come back through a `submit_answer` tool.** Its input is `{"rows": [[...], ...]}`. Comparison follows these rules:
   - Strings are trimmed and compared case-insensitively.
   - Numbers match within 1e-6, relative to their size. Numeric strings count as numbers.
   - Rows are compared as a multiset unless the question is marked `ordered`.
   - Every question names the columns it wants, so this measures tool efficiency, not guessing the answer shape.
2. **Tool output given to the model is capped at 100,000 characters,** with the note `[truncated by the benchmark harness]`. The cap is the same for both servers, much as agent clients cut long tool output. Each truncation is counted and reported, because dbhub has no default row cap.
3. **Tool-definition tokens** = `count_tokens(trivial tool + server tools) − count_tokens(trivial tool)`. Subtracting the baseline removes the fixed tool-use preamble that the API adds whenever any tool is present. The §1 criterion of 800 tokens or fewer is measured this way, for qh's three tools.
4. **Both servers run with read-only on and their own defaults otherwise.**
   - qh: `ro` connections, the default 100-row cap.
   - dbhub: `readonly = true` on `execute_sql`, no `max_rows`, which is dbhub's default.

   Both get the same connection descriptions.
5. **Five-connection scenario:** Chinook is registered under 1 to 5 names, and the tool-definition tokens are measured at each count. Agent runs use one connection per dataset, which keeps cost down.
6. **Micro-benchmarks, for each server:**
   - Cold schema: a fresh server process; time the first schema call on a generated 1,000-table Postgres database. That call is qh's `schema` overview, or dbhub's `search_objects` for tables at summary detail.
   - Query latency: p50/p95 over Pagila's reference queries, 20 timed runs each after 2 warm-ups.
7. **Pinned downloads.**
   - Datasets come from pinned commits or releases and are checked against SHA-256. The implementer records the hashes once, trust on first use.
   - dbhub is pinned to the npm version current at implementation time.

## Global Constraints

- **Separation from qh:**
  - `bench/` is its own module: `module github.com/anderson-0/qh/bench`.
  - qh's `go.mod` gains nothing.
  - `go test ./...` at the repo root does not enter `bench/`, because nested modules are excluded.
- **API key:**
  - Nothing in `bench/` needs `ANTHROPIC_API_KEY` except agent runs and token counting.
  - Tests that need it skip when it is unset.
  - Tests that need Docker or `npx` skip under `-short`, or when the tool is missing.
- **Fairness:** the same model, system prompt, user prompt, `max_tokens`, turn limit and output cap for both servers. The model id is a flag, defaulting to `claude-sonnet-5-5`.
- **No secrets:** database credentials are throwaway container credentials, and the API key is never logged or written to results.
- **Comparison rules:** numeric tolerance 1e-6, relative; sets compared ignoring order (spec §9.2).
- **Release gate (spec §1):**
  - at least 40% fewer total tokens per task than dbhub;
  - accuracy at least dbhub's;
  - fewer tool calls per task;
  - qh tool definitions of 800 tokens or fewer;
  - cold schema build on 1,000 tables under 1 s.

  The report evaluates each criterion and marks it pass or fail. It never hides a failure.

## Review Focus

1. **A reference query that returns nothing, or a tie the question doesn't cover,** would make a question unanswerable. Every reference SQL must return at least one row, and "most" questions use tie-safe SQL. → Task 2 `TestReferenceQueries`.
2. **A model that never calls `submit_answer`, or loops forever,** must end as a failed run with a reason, not hang. → Task 4 `TestRunStopsWithoutSubmit`, `TestRunTurnLimit`.
3. **A huge tool result**, such as dbhub dumping a whole table, must be capped and counted, not crash the API call. → Task 4 `TestRunCapsToolOutput`.
4. **A server process that fails to start**, such as npx being offline, must fail that server's runs with a clear error while the other server's runs continue. → Task 6 `TestRunnerServerStartFailure`.
5. **Partial results must still be written** when a long run is interrupted. → Task 6 `TestRunnerWritesRawIncrementally`.

---

## File map

```
bench/go.mod, go.sum                         separate module
bench/harness/datasets.go                    fetch + verify downloads; start Pagila/Sakila containers; open Chinook
bench/harness/questions/{pagila,sakila,chinook}.json   30 questions each, with reference SQL
bench/harness/questions.go                   Question type, Load
bench/harness/truth.go                       run reference SQL → rows
bench/harness/compare.go                     cell normalisation, row comparison
bench/harness/servers.go                     Server interface; qh and dbhub drivers over MCP stdio
bench/harness/agent.go                       Model interface, Run loop, submit_answer, output cap
bench/harness/anthropic.go                   Model + token counting over the Anthropic Go SDK
bench/harness/micro.go                       1,000-table generator, cold schema, query latency
bench/harness/report.go                      aggregation, success criteria, Markdown
bench/cmd/bench/main.go                      runner
bench/README.md, Makefile target `bench`, bench/.gitignore (.cache/, results/*/raw.jsonl is kept)
```

---

### Task 1: Bench module and datasets

**Files:**
- Create: `bench/go.mod`, `bench/.gitignore`, `bench/harness/datasets.go`
- Test: `bench/harness/datasets_test.go`

**Interfaces:**
- Produces:
  - `type harness.Dataset struct { Name, Engine, DSN, Description string; DB *sql.DB }`. `Engine` is `postgres`, `mysql` or `sqlite`. `DSN` is what the servers connect with: a URL for postgres and mysql, an absolute file path for sqlite.
  - `harness.CacheDir() string`, which returns `bench/.cache`, or `$QH_BENCH_CACHE` when set.
  - `harness.Fetch(ctx, cacheDir, name, url, sha256hex string) (path string, err error)`.
  - `harness.StartPagila(ctx) (*Dataset, func(), error)`.
  - `harness.StartSakila(ctx) (*Dataset, func(), error)`.
  - `harness.OpenChinook(ctx) (*Dataset, func(), error)`.
  - `harness.StartDataset(ctx, name string) (*Dataset, func(), error)`, which dispatches on `pagila`, `sakila` or `chinook`.

- [ ] **Step 1: Pin the downloads (one-time, manual)**

These hashes are recorded on first use. Run each command, check that the output looks right, then put the values into the `sources` table in Step 3.

1. **Pagila.**
   - `gh api repos/devrimgunduz/pagila/commits/master --jq .sha` gives the commit to pin, PAGILA_SHA.
   - The files are `https://raw.githubusercontent.com/devrimgunduz/pagila/<PAGILA_SHA>/pagila-schema.sql` and `.../pagila-data.sql`.
2. **Sakila.** The file is `https://downloads.mysql.com/docs/sakila-db.tar.gz`. It contains `sakila-db/sakila-schema.sql` and `sakila-db/sakila-data.sql`.
3. **Chinook.**
   - `gh api repos/lerocha/chinook-database/commits/master --jq .sha` gives CHINOOK_SHA.
   - Find the SQLite file's path with `gh api repos/lerocha/chinook-database/git/trees/<CHINOOK_SHA>?recursive=1 --jq '.tree[].path' | grep -i 'sqlite$'`.
   - The file is `https://raw.githubusercontent.com/lerocha/chinook-database/<CHINOOK_SHA>/<that path>`.
4. For each file, `curl -sSL <url> | shasum -a 256` gives the hash to pin.

- [ ] **Step 2: Write the failing test**

`bench/harness/datasets_test.go`:
```go
package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchVerifiesAndCaches(t *testing.T) {
	body := "hello dataset\n"
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte(body))
	}))
	defer srv.Close()
	sum := sha256.Sum256([]byte(body))
	good := hex.EncodeToString(sum[:])
	dir := t.TempDir()
	path, err := Fetch(context.Background(), dir, "x.sql", srv.URL, good)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != body || filepath.Dir(path) != dir {
		t.Fatalf("content %q at %s", b, path)
	}
	if _, err := Fetch(context.Background(), dir, "x.sql", srv.URL, good); err != nil || hits != 1 {
		t.Fatalf("second fetch should hit the cache: hits=%d err=%v", hits, err)
	}
	_, err = Fetch(context.Background(), t.TempDir(), "y.sql", srv.URL, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestDatasets(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads datasets and needs Docker")
	}
	for name, want := range map[string]struct {
		query string
		n     int
	}{
		"pagila":  {"SELECT count(*) FROM film", 1000},
		"sakila":  {"SELECT count(*) FROM film", 1000},
		"chinook": {"SELECT count(*) FROM Track", 3503},
	} {
		t.Run(name, func(t *testing.T) {
			ds, stop, err := StartDataset(context.Background(), name)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			var n int
			if err := ds.DB.QueryRow(want.query).Scan(&n); err != nil || n != want.n {
				t.Fatalf("%s: %d %v", want.query, n, err)
			}
			if ds.Name != name || ds.DSN == "" || ds.Description == "" {
				t.Fatalf("dataset fields: %+v", ds)
			}
		})
	}
}
```

- [ ] **Step 3: Implement**

`bench/go.mod`: create it with `cd bench && go mod init github.com/anderson-0/qh/bench`. Then set the `go 1.27` directive, and add the dependencies with `go get`:
- `github.com/jackc/pgx/v5@v5.11.0`
- `github.com/go-sql-driver/mysql@v1.10.1`
- `modernc.org/sqlite@v1.60.1`
- `github.com/testcontainers/testcontainers-go@v0.44.0`
- `.../modules/postgres@v0.44.0`
- `.../modules/mysql@v0.44.0`

`bench/.gitignore`:
```
.cache/
```

`bench/harness/datasets.go`:
```go
// Package harness runs the qh vs dbhub benchmark: datasets, questions,
// servers under test, the agent loop and the report.
package harness

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite"
)

// Dataset is a loaded sample database.
type Dataset struct {
	Name        string
	Engine      string // postgres, mysql or sqlite
	DSN         string // what the servers under test connect with
	Description string // the same text is given to both servers
	DB          *sql.DB
}

// source is one pinned download.
type source struct{ file, url, sha256 string }

// Pinned in Task 1 Step 1. Replace the placeholders with the recorded values.
var sources = map[string]source{
	"pagila-schema": {"pagila-schema.sql", "https://raw.githubusercontent.com/devrimgunduz/pagila/PAGILA_SHA/pagila-schema.sql", "SHA256_PAGILA_SCHEMA"},
	"pagila-data":   {"pagila-data.sql", "https://raw.githubusercontent.com/devrimgunduz/pagila/PAGILA_SHA/pagila-data.sql", "SHA256_PAGILA_DATA"},
	"sakila":        {"sakila-db.tar.gz", "https://downloads.mysql.com/docs/sakila-db.tar.gz", "SHA256_SAKILA"},
	"chinook":       {"chinook.sqlite", "https://raw.githubusercontent.com/lerocha/chinook-database/CHINOOK_SHA/CHINOOK_PATH", "SHA256_CHINOOK"},
}

// CacheDir is where downloads are kept: $QH_BENCH_CACHE, or bench/.cache.
func CacheDir() string {
	if d := os.Getenv("QH_BENCH_CACHE"); d != "" {
		return d
	}
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", ".cache")
}

// Fetch downloads url into cacheDir/name unless it is already there, and
// checks its SHA-256 either way.
func Fetch(ctx context.Context, cacheDir, name, url, sha256hex string) (string, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(cacheDir, name)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := download(ctx, url, path); err != nil {
			return "", err
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != sha256hex {
		os.Remove(path)
		return "", fmt.Errorf("%s: sha256 mismatch: got %s, want %s", name, got, sha256hex)
	}
	return path, nil
}

func download(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, res.StatusCode)
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fetchSource(ctx context.Context, key string) (string, error) {
	s := sources[key]
	return Fetch(ctx, CacheDir(), s.file, s.url, s.sha256)
}

// StartDataset starts or opens the named dataset.
func StartDataset(ctx context.Context, name string) (*Dataset, func(), error) {
	switch name {
	case "pagila":
		return StartPagila(ctx)
	case "sakila":
		return StartSakila(ctx)
	case "chinook":
		return OpenChinook(ctx)
	}
	return nil, nil, fmt.Errorf("unknown dataset %q (want pagila, sakila or chinook)", name)
}

// StartPagila loads Pagila into a Postgres 17 container.
func StartPagila(ctx context.Context) (*Dataset, func(), error) {
	schema, err := fetchSource(ctx, "pagila-schema")
	if err != nil {
		return nil, nil, err
	}
	data, err := fetchSource(ctx, "pagila-data")
	if err != nil {
		return nil, nil, err
	}
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("pagila"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("bench-pw"),
		tcpostgres.WithInitScripts(schema, data), tcpostgres.BasicWaitStrategies())
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return nil, nil, err
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return nil, nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		stop()
		return nil, nil, err
	}
	return &Dataset{Name: "pagila", Engine: "postgres", DSN: dsn, DB: db,
			Description: "Pagila: a DVD rental store (films, actors, customers, rentals, payments) on PostgreSQL"},
		func() { db.Close(); stop() }, nil
}

// StartSakila loads Sakila into a MySQL 8.4 container.
func StartSakila(ctx context.Context) (*Dataset, func(), error) {
	archive, err := fetchSource(ctx, "sakila")
	if err != nil {
		return nil, nil, err
	}
	dir := filepath.Join(CacheDir(), "sakila")
	if err := untar(archive, dir); err != nil {
		return nil, nil, err
	}
	schema, data := filepath.Join(dir, "sakila-db", "sakila-schema.sql"), filepath.Join(dir, "sakila-db", "sakila-data.sql")
	ctr, err := tcmysql.Run(ctx, "mysql:8.4",
		tcmysql.WithDatabase("sakila"), tcmysql.WithUsername("bench"), tcmysql.WithPassword("bench-pw"),
		tcmysql.WithScripts(schema, data))
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return nil, nil, err
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		stop()
		return nil, nil, err
	}
	port, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		stop()
		return nil, nil, err
	}
	url := fmt.Sprintf("mysql://bench:bench-pw@%s:%s/sakila", host, port.Port())
	db, err := sql.Open("mysql", fmt.Sprintf("bench:bench-pw@tcp(%s:%s)/sakila", host, port.Port()))
	if err != nil {
		stop()
		return nil, nil, err
	}
	return &Dataset{Name: "sakila", Engine: "mysql", DSN: url, DB: db,
			Description: "Sakila: a DVD rental store (films, actors, customers, rentals, payments) on MySQL"},
		func() { db.Close(); stop() }, nil
}

// untar extracts the .sql files of a .tar.gz into dir (skipped when present).
func untar(archive, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "sakila-db", "sakila-data.sql")); err == nil {
		return nil
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(h.Name, ".sql") || strings.Contains(h.Name, "..") {
			continue
		}
		out := filepath.Join(dir, filepath.Clean(h.Name))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		w, err := os.Create(out)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, tr); err != nil {
			w.Close()
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	}
}

// OpenChinook copies the cached Chinook file to a temp dir, so runs never
// change the cached copy.
func OpenChinook(ctx context.Context) (*Dataset, func(), error) {
	src, err := fetchSource(ctx, "chinook")
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "qh-bench-chinook-")
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "chinook.sqlite")
	b, err := os.ReadFile(src)
	if err == nil {
		err = os.WriteFile(path, b, 0o644)
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	return &Dataset{Name: "chinook", Engine: "sqlite", DSN: path, DB: db,
			Description: "Chinook: a digital music store (artists, albums, tracks, playlists, customers, invoices) on SQLite"},
		func() { db.Close(); os.RemoveAll(dir) }, nil
}
```
If `tcpostgres.WithInitScripts` or `tcmysql.WithScripts` cannot load a file, adjust the loading and report the change. Two known causes:
- Pagila's schema sets object owners to `postgres`, so the container user must be `postgres`. The code already sets it.
- Sakila's functions need `log_bin_trust_function_creators`. If loading fails on them, run the scripts as root.

The test is what counts: film counts of 1000, 1000, and a Chinook track count of 3503.

- [ ] **Step 4: Run the tests**

Run, from `bench/`: `go test -short ./harness/ -run Fetch && go test ./harness/ -run TestDatasets && go vet ./...`
Expected: PASS. The first full run downloads about 10 MB.

- [ ] **Step 5: Commit**

```bash
git add bench/go.mod bench/go.sum bench/.gitignore bench/harness/datasets.go bench/harness/datasets_test.go
git commit -m "feat(bench): add the benchmark module with pinned Pagila, Sakila and Chinook datasets"
```

---

### Task 2: Questions, ground truth and answer comparison

**Files:**
- Create:
  - `bench/harness/questions/pagila.json`, `sakila.json`, `chinook.json`
  - `bench/harness/questions.go`, `bench/harness/truth.go`, `bench/harness/compare.go`
- Test: `bench/harness/compare_test.go`, `bench/harness/questions_test.go`

**Interfaces:**
- Consumes: `Dataset`, `StartDataset` (Task 1).
- Produces:
  - `type harness.Question struct { ID, Kind, Question, SQL string; Ordered bool }`
    - JSON tags: `id`, `kind`, `question`, `sql`, `ordered`.
    - `Kind` is one of `lookup`, `join`, `enum`, `aggregate`, `large`, `misspell`.
  - `harness.LoadQuestions(dataset string) ([]Question, error)` reads the embedded file.
  - `harness.Truth(ctx, db *sql.DB, query string) ([][]any, error)` returns the rows. Each value is nil, a string, an int64 or a float64; `[]byte` becomes string.
  - `harness.Equal(want, got [][]any, ordered bool) bool`.
  - `harness.PromptFor(q Question) string`, the user prompt sent to the model.

- [ ] **Step 1: Write the questions**

These question files are binding. Each question names the columns it wants, and every "most" question is written to be safe against ties.

`bench/harness/questions/pagila.json`:
```json
[
  {"id":"pagila-01","kind":"lookup","question":"How many films are in the database? Return: count.","sql":"SELECT count(*) FROM film"},
  {"id":"pagila-02","kind":"lookup","question":"What is the email address of the customer MARY SMITH? Return: email.","sql":"SELECT email FROM customer WHERE first_name = 'MARY' AND last_name = 'SMITH'"},
  {"id":"pagila-03","kind":"lookup","question":"What is the rental rate of the film ACADEMY DINOSAUR? Return: rental rate.","sql":"SELECT rental_rate FROM film WHERE title = 'ACADEMY DINOSAUR'"},
  {"id":"pagila-04","kind":"lookup","question":"How many actors have the last name GUINESS? Return: count.","sql":"SELECT count(*) FROM actor WHERE last_name = 'GUINESS'"},
  {"id":"pagila-05","kind":"join","question":"In which language is the film ALIEN CENTER? Return: language name.","sql":"SELECT l.name FROM film f JOIN language l ON l.language_id = f.language_id WHERE f.title = 'ALIEN CENTER'"},
  {"id":"pagila-06","kind":"join","question":"List the titles of all films the actor PENELOPE GUINESS appears in. Return: title.","sql":"SELECT f.title FROM actor a JOIN film_actor fa ON fa.actor_id = a.actor_id JOIN film f ON f.film_id = fa.film_id WHERE a.first_name = 'PENELOPE' AND a.last_name = 'GUINESS'"},
  {"id":"pagila-07","kind":"join","question":"How many films are in the Horror category? Return: count.","sql":"SELECT count(*) FROM film_category fc JOIN category c ON c.category_id = fc.category_id WHERE c.name = 'Horror'"},
  {"id":"pagila-08","kind":"join","question":"Which category has the most films? If several tie, return all of them. Return: category name.","sql":"WITH n AS (SELECT c.name, count(*) AS k FROM category c JOIN film_category fc ON fc.category_id = c.category_id GROUP BY c.name) SELECT name FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"pagila-09","kind":"join","question":"Which actor appears in the most films? If several tie, return all of them. Return: first name, last name.","sql":"WITH n AS (SELECT a.actor_id, a.first_name, a.last_name, count(*) AS k FROM actor a JOIN film_actor fa ON fa.actor_id = a.actor_id GROUP BY a.actor_id, a.first_name, a.last_name) SELECT first_name, last_name FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"pagila-10","kind":"join","question":"In which city and country does the customer MARY SMITH live? Return: city, country.","sql":"SELECT ci.city, co.country FROM customer cu JOIN address ad ON ad.address_id = cu.address_id JOIN city ci ON ci.city_id = ad.city_id JOIN country co ON co.country_id = ci.country_id WHERE cu.first_name = 'MARY' AND cu.last_name = 'SMITH'"},
  {"id":"pagila-11","kind":"join","question":"How many customers live in Canada? Return: count.","sql":"SELECT count(*) FROM customer cu JOIN address ad ON ad.address_id = cu.address_id JOIN city ci ON ci.city_id = ad.city_id JOIN country co ON co.country_id = ci.country_id WHERE co.country = 'Canada'"},
  {"id":"pagila-12","kind":"join","question":"How many copies (inventory items) of the film ACADEMY DINOSAUR does store 1 have? Return: count.","sql":"SELECT count(*) FROM inventory i JOIN film f ON f.film_id = i.film_id WHERE f.title = 'ACADEMY DINOSAUR' AND i.store_id = 1"},
  {"id":"pagila-13","kind":"join","question":"Which staff member processed the most rentals? If several tie, return all of them. Return: first name, last name.","sql":"WITH n AS (SELECT s.staff_id, s.first_name, s.last_name, count(*) AS k FROM staff s JOIN rental r ON r.staff_id = s.staff_id GROUP BY s.staff_id, s.first_name, s.last_name) SELECT first_name, last_name FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"pagila-14","kind":"join","question":"Which categories is the film AFFAIR PREJUDICE in? Return: category name.","sql":"SELECT c.name FROM film f JOIN film_category fc ON fc.film_id = f.film_id JOIN category c ON c.category_id = fc.category_id WHERE f.title = 'AFFAIR PREJUDICE'"},
  {"id":"pagila-15","kind":"join","question":"What is the total amount paid by the customer ELEANOR HUNT? Return: total amount.","sql":"SELECT sum(p.amount) FROM payment p JOIN customer c ON c.customer_id = p.customer_id WHERE c.first_name = 'ELEANOR' AND c.last_name = 'HUNT'"},
  {"id":"pagila-16","kind":"enum","question":"How many films are rated PG-13? Return: count.","sql":"SELECT count(*) FROM film WHERE rating = 'PG-13'"},
  {"id":"pagila-17","kind":"enum","question":"What is the average length in minutes of films rated NC-17, rounded to two decimals? Return: average length.","sql":"SELECT round(avg(length), 2) FROM film WHERE rating = 'NC-17'"},
  {"id":"pagila-18","kind":"enum","question":"How many films rated G are in the Children category? Return: count.","sql":"SELECT count(*) FROM film f JOIN film_category fc ON fc.film_id = f.film_id JOIN category c ON c.category_id = fc.category_id WHERE f.rating = 'G' AND c.name = 'Children'"},
  {"id":"pagila-19","kind":"enum","question":"For each film rating, how many films have that rating? Return: rating, film count.","sql":"SELECT rating::text, count(*) FROM film GROUP BY rating"},
  {"id":"pagila-20","kind":"aggregate","question":"What is the average rental rate across all films, rounded to two decimals? Return: average.","sql":"SELECT round(avg(rental_rate), 2) FROM film"},
  {"id":"pagila-21","kind":"aggregate","question":"How many rentals have not been returned (they have no return date)? Return: count.","sql":"SELECT count(*) FROM rental WHERE return_date IS NULL"},
  {"id":"pagila-22","kind":"aggregate","question":"Which country has the most customers? If several tie, return all of them. Return: country.","sql":"WITH n AS (SELECT co.country, count(*) AS k FROM customer cu JOIN address ad ON ad.address_id = cu.address_id JOIN city ci ON ci.city_id = ad.city_id JOIN country co ON co.country_id = ci.country_id GROUP BY co.country) SELECT country FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"pagila-23","kind":"aggregate","question":"Which film was rented the most times? If several tie, return all of them. Return: title, rental count.","sql":"WITH n AS (SELECT f.film_id, f.title, count(*) AS k FROM rental r JOIN inventory i ON i.inventory_id = r.inventory_id JOIN film f ON f.film_id = i.film_id GROUP BY f.film_id, f.title) SELECT title, k FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"pagila-24","kind":"aggregate","question":"How many distinct customers have rented at least one film in the Horror category? Return: count.","sql":"SELECT count(DISTINCT r.customer_id) FROM rental r JOIN inventory i ON i.inventory_id = r.inventory_id JOIN film_category fc ON fc.film_id = i.film_id JOIN category c ON c.category_id = fc.category_id WHERE c.name = 'Horror'"},
  {"id":"pagila-25","kind":"aggregate","question":"What is the longest film length in minutes, and which films have it? Return: title, length.","sql":"SELECT title, length FROM film WHERE length = (SELECT max(length) FROM film)"},
  {"id":"pagila-26","kind":"aggregate","question":"What is the average total paid per paying customer (sum of all payments divided by the number of distinct customers who paid), rounded to two decimals? Return: average.","sql":"SELECT round(sum(amount) / count(DISTINCT customer_id), 2) FROM payment"},
  {"id":"pagila-27","kind":"large","question":"What is the total of all payments ever received? Return: total amount.","sql":"SELECT sum(amount) FROM payment"},
  {"id":"pagila-28","kind":"large","question":"Which customer has made the most payments? If several tie, return all of them. Return: first name, last name.","sql":"WITH n AS (SELECT c.customer_id, c.first_name, c.last_name, count(*) AS k FROM customer c JOIN payment p ON p.customer_id = c.customer_id GROUP BY c.customer_id, c.first_name, c.last_name) SELECT first_name, last_name FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"pagila-29","kind":"misspell","question":"Use the film table's film_length column to find the length in minutes of the film ACE GOLDFINGER. Return: length.","sql":"SELECT length FROM film WHERE title = 'ACE GOLDFINGER'"},
  {"id":"pagila-30","kind":"misspell","question":"Look up the customer whose customer_name is MARY SMITH and count her rentals. Return: count.","sql":"SELECT count(*) FROM rental r JOIN customer c ON c.customer_id = r.customer_id WHERE c.first_name = 'MARY' AND c.last_name = 'SMITH'"}
]
```

`bench/harness/questions/sakila.json`: copy pagila.json and make three changes:
- In every `id`, replace `pagila-` with `sakila-`.
- In `sakila-19`, the SQL becomes `SELECT rating, count(*) FROM film GROUP BY rating`, since MySQL has no `::text` cast.
- Leave every other question and every other SQL unchanged. They are valid MySQL 8 as written: CTEs, `round`, `count(DISTINCT …)`.

`bench/harness/questions/chinook.json`:
```json
[
  {"id":"chinook-01","kind":"lookup","question":"How many tracks are in the database? Return: count.","sql":"SELECT count(*) FROM Track"},
  {"id":"chinook-02","kind":"lookup","question":"What is the email address of the customer Leonie Köhler? Return: email.","sql":"SELECT Email FROM Customer WHERE FirstName = 'Leonie' AND LastName = 'Köhler'"},
  {"id":"chinook-03","kind":"lookup","question":"Which country is the customer with id 10 from? Return: country.","sql":"SELECT Country FROM Customer WHERE CustomerId = 10"},
  {"id":"chinook-04","kind":"lookup","question":"What is the job title of the employee Andrew Adams? Return: title.","sql":"SELECT Title FROM Employee WHERE FirstName = 'Andrew' AND LastName = 'Adams'"},
  {"id":"chinook-05","kind":"lookup","question":"How many artists are in the database? Return: count.","sql":"SELECT count(*) FROM Artist"},
  {"id":"chinook-06","kind":"join","question":"List the titles of all albums by the artist AC/DC. Return: album title.","sql":"SELECT a.Title FROM Album a JOIN Artist ar ON ar.ArtistId = a.ArtistId WHERE ar.Name = 'AC/DC'"},
  {"id":"chinook-07","kind":"join","question":"How many tracks are on the album Let There Be Rock? Return: count.","sql":"SELECT count(*) FROM Track t JOIN Album a ON a.AlbumId = t.AlbumId WHERE a.Title = 'Let There Be Rock'"},
  {"id":"chinook-08","kind":"join","question":"Which genre has the most tracks? If several tie, return all of them. Return: genre name.","sql":"WITH n AS (SELECT g.Name, count(*) AS k FROM Genre g JOIN Track t ON t.GenreId = g.GenreId GROUP BY g.GenreId, g.Name) SELECT Name FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"chinook-09","kind":"join","question":"List the names of the tracks in the playlist named Grunge. Return: track name.","sql":"SELECT t.Name FROM Playlist p JOIN PlaylistTrack pt ON pt.PlaylistId = p.PlaylistId JOIN Track t ON t.TrackId = pt.TrackId WHERE p.Name = 'Grunge'"},
  {"id":"chinook-10","kind":"join","question":"Who is the support representative for the customer Luís Gonçalves? Return: first name, last name.","sql":"SELECT e.FirstName, e.LastName FROM Customer c JOIN Employee e ON e.EmployeeId = c.SupportRepId WHERE c.FirstName = 'Luís' AND c.LastName = 'Gonçalves'"},
  {"id":"chinook-11","kind":"join","question":"Which artist has the most albums? If several tie, return all of them. Return: artist name.","sql":"WITH n AS (SELECT ar.Name, count(*) AS k FROM Artist ar JOIN Album a ON a.ArtistId = ar.ArtistId GROUP BY ar.ArtistId, ar.Name) SELECT Name FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"chinook-12","kind":"join","question":"What is the total amount invoiced to customers who live in Brazil, rounded to two decimals? Return: total.","sql":"SELECT round(sum(i.Total), 2) FROM Invoice i JOIN Customer c ON c.CustomerId = i.CustomerId WHERE c.Country = 'Brazil'"},
  {"id":"chinook-13","kind":"join","question":"Which employee is the support representative for the most customers? If several tie, return all of them. Return: first name, last name.","sql":"WITH n AS (SELECT e.EmployeeId, e.FirstName, e.LastName, count(*) AS k FROM Employee e JOIN Customer c ON c.SupportRepId = e.EmployeeId GROUP BY e.EmployeeId, e.FirstName, e.LastName) SELECT FirstName, LastName FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"chinook-14","kind":"join","question":"Which employees report directly to Nancy Edwards? Return: first name, last name.","sql":"SELECT e.FirstName, e.LastName FROM Employee e JOIN Employee m ON m.EmployeeId = e.ReportsTo WHERE m.FirstName = 'Nancy' AND m.LastName = 'Edwards'"},
  {"id":"chinook-15","kind":"join","question":"Which artist recorded the track Smells Like Teen Spirit? Return: artist name.","sql":"SELECT DISTINCT ar.Name FROM Track t JOIN Album a ON a.AlbumId = t.AlbumId JOIN Artist ar ON ar.ArtistId = a.ArtistId WHERE t.Name = 'Smells Like Teen Spirit'"},
  {"id":"chinook-16","kind":"enum","question":"How many tracks use the media type Protected AAC audio file? Return: count.","sql":"SELECT count(*) FROM Track t JOIN MediaType m ON m.MediaTypeId = t.MediaTypeId WHERE m.Name = 'Protected AAC audio file'"},
  {"id":"chinook-17","kind":"enum","question":"How many tracks belong to the Jazz genre? Return: count.","sql":"SELECT count(*) FROM Track t JOIN Genre g ON g.GenreId = t.GenreId WHERE g.Name = 'Jazz'"},
  {"id":"chinook-18","kind":"enum","question":"List the distinct composers of tracks in the Blues genre, ignoring tracks with no composer. Return: composer.","sql":"SELECT DISTINCT t.Composer FROM Track t JOIN Genre g ON g.GenreId = t.GenreId WHERE g.Name = 'Blues' AND t.Composer IS NOT NULL"},
  {"id":"chinook-19","kind":"enum","question":"Which media type is used by the most tracks? If several tie, return all of them. Return: media type name.","sql":"WITH n AS (SELECT m.Name, count(*) AS k FROM MediaType m JOIN Track t ON t.MediaTypeId = m.MediaTypeId GROUP BY m.MediaTypeId, m.Name) SELECT Name FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"chinook-20","kind":"aggregate","question":"What is the average track length in milliseconds, rounded to the nearest integer? Return: average.","sql":"SELECT round(avg(Milliseconds)) FROM Track"},
  {"id":"chinook-21","kind":"aggregate","question":"How many invoices have a total greater than 10? Return: count.","sql":"SELECT count(*) FROM Invoice WHERE Total > 10"},
  {"id":"chinook-22","kind":"aggregate","question":"Which billing country has the highest total invoiced amount? If several tie, return all of them. Return: country.","sql":"WITH n AS (SELECT BillingCountry, sum(Total) AS k FROM Invoice GROUP BY BillingCountry) SELECT BillingCountry FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"chinook-23","kind":"aggregate","question":"Who are the three customers who spent the most in total? Return: first name, last name, total spent, ordered from highest to lowest.","ordered":true,"sql":"SELECT c.FirstName, c.LastName, round(sum(i.Total), 2) AS spent FROM Customer c JOIN Invoice i ON i.CustomerId = c.CustomerId GROUP BY c.CustomerId, c.FirstName, c.LastName ORDER BY spent DESC LIMIT 3"},
  {"id":"chinook-24","kind":"aggregate","question":"How many tracks cost more than 0.99? Return: count.","sql":"SELECT count(*) FROM Track WHERE UnitPrice > 0.99"},
  {"id":"chinook-25","kind":"aggregate","question":"How many tracks does each genre have? Return: genre name, track count.","sql":"SELECT g.Name, count(*) FROM Genre g JOIN Track t ON t.GenreId = g.GenreId GROUP BY g.GenreId, g.Name"},
  {"id":"chinook-26","kind":"aggregate","question":"What is the total length of all Classical genre tracks in minutes, rounded to one decimal? Return: minutes.","sql":"SELECT round(sum(t.Milliseconds) / 60000.0, 1) FROM Track t JOIN Genre g ON g.GenreId = t.GenreId WHERE g.Name = 'Classical'"},
  {"id":"chinook-27","kind":"aggregate","question":"How many customers are there in each country? Return: country, customer count.","sql":"SELECT Country, count(*) FROM Customer GROUP BY Country"},
  {"id":"chinook-28","kind":"large","question":"What is the total revenue from all invoice lines (unit price times quantity), rounded to two decimals? Return: revenue.","sql":"SELECT round(sum(UnitPrice * Quantity), 2) FROM InvoiceLine"},
  {"id":"chinook-29","kind":"large","question":"Which playlist contains the most tracks? If several tie, return all of them. Return: playlist name.","sql":"WITH n AS (SELECT p.PlaylistId, p.Name, count(*) AS k FROM Playlist p JOIN PlaylistTrack pt ON pt.PlaylistId = p.PlaylistId GROUP BY p.PlaylistId, p.Name) SELECT DISTINCT Name FROM n WHERE k = (SELECT max(k) FROM n)"},
  {"id":"chinook-30","kind":"misspell","question":"Using the invoice line column named qty, what is the total quantity sold on invoice 1? Return: quantity.","sql":"SELECT sum(Quantity) FROM InvoiceLine WHERE InvoiceId = 1"}
]
```

- [ ] **Step 2: Write the failing tests**

`bench/harness/compare_test.go`:
```go
package harness

import "testing"

func TestEqual(t *testing.T) {
	cases := []struct {
		name      string
		want, got [][]any
		ordered   bool
		eq        bool
	}{
		{"number vs numeric string", [][]any{{"4.99"}}, [][]any{{4.99}}, false, true},
		{"float tolerance", [][]any{{190.1}}, [][]any{{190.10000000000002}}, false, true},
		{"different numbers", [][]any{{1000.0}}, [][]any{{999.0}}, false, false},
		{"case and space insensitive", [][]any{{"MARY.SMITH@x.org "}}, [][]any{{"mary.smith@x.org"}}, false, true},
		{"set ignores order", [][]any{{"a"}, {"b"}}, [][]any{{"b"}, {"a"}}, false, true},
		{"ordered respects order", [][]any{{"a"}, {"b"}}, [][]any{{"b"}, {"a"}}, true, false},
		{"multiset counts", [][]any{{"a"}, {"a"}}, [][]any{{"a"}, {"b"}}, false, false},
		{"row count differs", [][]any{{"a"}}, [][]any{{"a"}, {"a"}}, false, false},
		{"width differs", [][]any{{"a", 1}}, [][]any{{"a"}}, false, false},
		{"nulls", [][]any{{nil}}, [][]any{{nil}}, false, true},
		{"null vs string", [][]any{{nil}}, [][]any{{"null"}}, false, false},
		{"integer as float", [][]any{{int64(1000)}}, [][]any{{1000.0}}, false, true},
		{"big relative tolerance", [][]any{{67416.51}}, [][]any{{67416.51000000001}}, false, true},
	}
	for _, c := range cases {
		if got := Equal(c.want, c.got, c.ordered); got != c.eq {
			t.Errorf("%s: Equal = %v, want %v", c.name, got, c.eq)
		}
	}
}
```

`bench/harness/questions_test.go`:
```go
package harness

import (
	"context"
	"strings"
	"testing"
)

func TestQuestionFiles(t *testing.T) {
	kinds := map[string]bool{"lookup": true, "join": true, "enum": true, "aggregate": true, "large": true, "misspell": true}
	for _, ds := range []string{"pagila", "sakila", "chinook"} {
		qs, err := LoadQuestions(ds)
		if err != nil {
			t.Fatal(err)
		}
		if len(qs) != 30 {
			t.Fatalf("%s: %d questions", ds, len(qs))
		}
		seen := map[string]bool{}
		got := map[string]bool{}
		for _, q := range qs {
			if seen[q.ID] || !strings.HasPrefix(q.ID, ds+"-") || !kinds[q.Kind] || q.SQL == "" || !strings.Contains(q.Question, "Return:") {
				t.Fatalf("%s: bad question %+v", ds, q)
			}
			seen[q.ID], got[q.Kind] = true, true
		}
		for _, k := range []string{"lookup", "join", "aggregate", "large", "misspell"} {
			if !got[k] {
				t.Fatalf("%s has no %s question", ds, k)
			}
		}
		if !strings.Contains(PromptFor(qs[0]), qs[0].Question) {
			t.Fatal("prompt does not carry the question")
		}
	}
	if _, err := LoadQuestions("nope"); err == nil {
		t.Fatal("unknown dataset loaded")
	}
}

func TestReferenceQueries(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the datasets (Docker)")
	}
	for _, name := range []string{"pagila", "sakila", "chinook"} {
		t.Run(name, func(t *testing.T) {
			ds, stop, err := StartDataset(context.Background(), name)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			qs, _ := LoadQuestions(name)
			for _, q := range qs {
				rows, err := Truth(context.Background(), ds.DB, q.SQL)
				if err != nil {
					t.Errorf("%s: %v", q.ID, err)
					continue
				}
				if len(rows) == 0 || len(rows[0]) == 0 || (len(rows) == 1 && len(rows[0]) == 1 && rows[0][0] == nil) {
					t.Errorf("%s: empty or null ground truth %v", q.ID, rows)
				}
				if !Equal(rows, rows, q.Ordered) {
					t.Errorf("%s: ground truth does not equal itself", q.ID)
				}
			}
		})
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `cd bench && go test -short ./harness/ -run 'Equal|QuestionFiles'`
Expected: FAIL to build: `undefined: Equal`, `undefined: LoadQuestions`.

- [ ] **Step 4: Implement**

`bench/harness/questions.go`:
```go
package harness

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed questions/*.json
var questionFiles embed.FS

// Question is one benchmark question with its reference query.
type Question struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // lookup, join, enum, aggregate, large, misspell
	Question string `json:"question"`
	SQL      string `json:"sql"`
	Ordered  bool   `json:"ordered,omitempty"`
}

// LoadQuestions returns the questions for a dataset.
func LoadQuestions(dataset string) ([]Question, error) {
	b, err := questionFiles.ReadFile("questions/" + dataset + ".json")
	if err != nil {
		return nil, fmt.Errorf("no questions for dataset %q", dataset)
	}
	var qs []Question
	if err := json.Unmarshal(b, &qs); err != nil {
		return nil, fmt.Errorf("questions/%s.json: %w", dataset, err)
	}
	return qs, nil
}

// SystemPrompt is the same for every server and question.
const SystemPrompt = "You are a careful data analyst. Answer the user's question by exploring and querying the database " +
	"with the available tools. When you know the answer, call submit_answer exactly once with the result rows " +
	"(one array per row, columns in the order the question asks for). Do not guess values you have not queried."

// PromptFor is the user message for a question.
func PromptFor(q Question) string { return q.Question }
```

`bench/harness/truth.go`:
```go
package harness

import (
	"context"
	"database/sql"
)

// Truth runs a reference query and returns its rows. []byte values become
// strings; numbers stay numeric where the driver returns them so.
func Truth(ctx context.Context, db *sql.DB, query string) ([][]any, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		out = append(out, vals)
	}
	return out, rows.Err()
}
```

`bench/harness/compare.go`:
```go
package harness

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// cell is a normalised value: a number, a lower-cased trimmed string, or null.
type cell struct {
	null  bool
	isNum bool
	num   float64
	str   string
}

func normalize(v any) cell {
	switch x := v.(type) {
	case nil:
		return cell{null: true}
	case float64:
		return cell{isNum: true, num: x}
	case float32:
		return cell{isNum: true, num: float64(x)}
	case int64:
		return cell{isNum: true, num: float64(x)}
	case int:
		return cell{isNum: true, num: float64(x)}
	case json.Number:
		f, _ := x.Float64()
		return cell{isNum: true, num: f}
	case bool:
		return cell{str: strconv.FormatBool(x)}
	case time.Time:
		return cell{str: strings.ToLower(x.UTC().Format(time.RFC3339))}
	case string:
		s := strings.TrimSpace(x)
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return cell{isNum: true, num: f}
		}
		return cell{str: strings.ToLower(s)}
	}
	return cell{str: strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))}
}

func (a cell) equal(b cell) bool {
	switch {
	case a.null || b.null:
		return a.null && b.null
	case a.isNum && b.isNum:
		scale := math.Max(1, math.Max(math.Abs(a.num), math.Abs(b.num)))
		return math.Abs(a.num-b.num) <= 1e-6*scale
	case a.isNum || b.isNum:
		return false
	}
	return a.str == b.str
}

func rowEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !normalize(a[i]).equal(normalize(b[i])) {
			return false
		}
	}
	return true
}

// Equal compares answer rows with the ground truth: numbers within a
// relative 1e-6, strings trimmed and case-insensitive, rows as a multiset
// unless ordered.
func Equal(want, got [][]any, ordered bool) bool {
	if len(want) != len(got) {
		return false
	}
	if ordered {
		for i := range want {
			if !rowEqual(want[i], got[i]) {
				return false
			}
		}
		return true
	}
	used := make([]bool, len(got))
	for _, w := range want {
		found := false
		for j, g := range got {
			if !used[j] && rowEqual(w, g) {
				used[j], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
```

- [ ] **Step 5: Run the tests**

Run: `cd bench && go test -short ./harness/ && go test ./harness/ -run TestReferenceQueries && go vet ./...`
Expected: PASS. If a reference query fails on one engine, fix that dataset's SQL so it asks the same question, and record the change in the report. A typical cause is a type cast or an identifier's case.

- [ ] **Step 6: Commit**

```bash
git add bench/harness
git commit -m "feat(bench): add 90 questions with reference SQL, ground truth and answer comparison"
```

---

### Task 3: Servers under test: qh and dbhub over MCP stdio

**Files:**
- Create: `bench/harness/servers.go`
- Test: `bench/harness/servers_test.go`

**Interfaces:**
- Consumes: `Dataset` (Task 1).
- Produces:
  - `type harness.Conn struct { Name, Engine, DSN, Description string }`
  - `harness.ConnFor(ds *Dataset) Conn`
  - The `Server` interface:
    ```go
    type Server interface {
        Name() string
        Tools() []*mcp.Tool
        Call(ctx context.Context, tool string, args json.RawMessage) (text string, isError bool)
        Close() error
    }
    ```
    A protocol error, such as an unknown tool, is returned as `isError` text, never as a Go error.
  - `harness.BuildQH(ctx) (bin string, cleanup func(), err error)` builds `../cmd/qh` from the parent module.
  - `harness.StartQH(ctx, bin string, conns []Conn) (Server, error)` runs `qh --home <temp> mcp` after `qh conn add` for each connection.
  - `harness.DefaultDBHubVersion`, a constant pinned in Step 1.
  - `harness.StartDBHub(ctx, version string, conns []Conn) (Server, error)` writes `dbhub.toml` and runs `npx -y @bytebase/dbhub@<version> --transport stdio --config dbhub.toml`.
  - `harness.FindTool(tools []*mcp.Tool, prefix string) string` returns the first tool name with that prefix, or "".

- [ ] **Step 1: Pin dbhub**

Run `npm view @bytebase/dbhub version`, and put the result into `DefaultDBHubVersion`. Then, with any SQLite file, run:

`npx -y @bytebase/dbhub@<version> --transport stdio --config <a dbhub.toml with one sqlite source>`

Check two things:
- It starts.
- The `[[tools]]` table with `name = "execute_sql"`, `source = …` and `readonly = true` is accepted.

If the TOML keys differ in that version, follow the version's own `dbhub.toml.example`, and record the difference in your report.

- [ ] **Step 2: Write the failing tests**

`bench/harness/servers_test.go`:
```go
package harness

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

func chinook(t *testing.T) *Dataset {
	t.Helper()
	if testing.Short() {
		t.Skip("downloads Chinook")
	}
	ds, stop, err := OpenChinook(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return ds
}

func TestQHServer(t *testing.T) {
	ds := chinook(t)
	ctx := context.Background()
	bin, cleanup, err := BuildQH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	srv, err := StartQH(ctx, bin, []Conn{ConnFor(ds)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	var names []string
	for _, tl := range srv.Tools() {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "query,schema,sources" || srv.Name() != "qh" {
		t.Fatalf("tools %v", names)
	}
	out, isErr := srv.Call(ctx, "query", json.RawMessage(`{"source":"chinook","query":"SELECT count(*) AS n FROM Track"}`))
	if isErr || !strings.HasPrefix(out, "n\n3503\n") {
		t.Fatalf("query: %q %v", out, isErr)
	}
	if out, isErr := srv.Call(ctx, "nope", json.RawMessage(`{}`)); !isErr || out == "" {
		t.Fatalf("unknown tool: %q %v", out, isErr)
	}
}

func TestDBHubServer(t *testing.T) {
	ds := chinook(t)
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not installed")
	}
	ctx := context.Background()
	srv, err := StartDBHub(ctx, DefaultDBHubVersion, []Conn{ConnFor(ds)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	exec := FindTool(srv.Tools(), "execute_sql")
	if exec == "" || srv.Name() != "dbhub" {
		t.Fatalf("tools: %v", srv.Tools())
	}
	out, isErr := srv.Call(ctx, exec, json.RawMessage(`{"sql":"SELECT count(*) AS n FROM Track"}`))
	if isErr || !strings.Contains(out, "3503") {
		t.Fatalf("execute_sql: %q %v", out, isErr)
	}
	if out, isErr := srv.Call(ctx, exec, json.RawMessage(`{"sql":"DELETE FROM Track"}`)); !isErr && !strings.Contains(strings.ToLower(out), "read") {
		t.Fatalf("dbhub accepted a write in read-only mode: %q", out)
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `cd bench && go test ./harness/ -run 'QHServer|DBHubServer'`
Expected: FAIL to build (`undefined: BuildQH`, …).

- [ ] **Step 4: Implement**

`bench/harness/servers.go`:
```go
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultDBHubVersion is the dbhub release benchmarked (pinned in Task 3 Step 1).
const DefaultDBHubVersion = "DBHUB_VERSION"

// Conn is one connection registered with a server under test.
type Conn struct{ Name, Engine, DSN, Description string }

// ConnFor is the connection for a dataset, named after it.
func ConnFor(ds *Dataset) Conn {
	return Conn{Name: ds.Name, Engine: ds.Engine, DSN: ds.DSN, Description: ds.Description}
}

// Server is a database MCP server under test.
type Server interface {
	Name() string
	Tools() []*mcp.Tool
	// Call runs a tool. Protocol errors (an unknown tool, bad arguments) come
	// back as error text, the way an agent client shows them to the model.
	Call(ctx context.Context, tool string, args json.RawMessage) (text string, isError bool)
	Close() error
}

type mcpServer struct {
	name    string
	session *mcp.ClientSession
	tools   []*mcp.Tool
	cleanup func()
}

func connect(ctx context.Context, name string, cmd *exec.Cmd, cleanup func()) (*mcpServer, error) {
	cmd.Stderr = nil // keep server logs out of the benchmark output
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute) // npx may download on first use
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "qh-bench", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	var tools []*mcp.Tool
	for tl, err := range session.Tools(ctx, nil) {
		if err != nil {
			session.Close()
			cleanup()
			return nil, fmt.Errorf("%s: list tools: %w", name, err)
		}
		tools = append(tools, tl)
	}
	return &mcpServer{name: name, session: session, tools: tools, cleanup: cleanup}, nil
}

func (s *mcpServer) Name() string       { return s.name }
func (s *mcpServer) Tools() []*mcp.Tool { return s.tools }

func (s *mcpServer) Call(ctx context.Context, tool string, args json.RawMessage) (string, bool) {
	var a map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "invalid tool arguments: " + err.Error(), true
		}
	}
	res, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: a})
	if err != nil {
		return "tool call failed: " + err.Error(), true
	}
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String(), res.IsError
}

func (s *mcpServer) Close() error {
	err := s.session.Close()
	s.cleanup()
	return err
}

// FindTool returns the first tool whose name starts with prefix, or "".
func FindTool(tools []*mcp.Tool, prefix string) string {
	for _, t := range tools {
		if strings.HasPrefix(t.Name, prefix) {
			return t.Name
		}
	}
	return ""
}

// repoRoot is the qh module root, the parent of bench/.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// BuildQH builds the qh binary from the parent module into a temp dir.
func BuildQH(ctx context.Context) (string, func(), error) {
	dir, err := os.MkdirTemp("", "qh-bench-bin-")
	if err != nil {
		return "", nil, err
	}
	bin := filepath.Join(dir, "qh")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/qh")
	cmd.Dir = repoRoot()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("build qh: %v\n%s", err, out)
	}
	return bin, func() { os.RemoveAll(dir) }, nil
}

// StartQH registers conns in a fresh local store and serves MCP over stdio.
func StartQH(ctx context.Context, bin string, conns []Conn) (Server, error) {
	home, err := os.MkdirTemp("", "qh-bench-home-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { os.RemoveAll(home) }
	for _, c := range conns {
		add := exec.CommandContext(ctx, bin, "--home", home, "conn", "add", c.Name, "--engine", c.Engine, "--dsn", "-", "--description", c.Description)
		add.Stdin = strings.NewReader(c.DSN + "\n")
		add.Env = withoutEnv(os.Environ(), "QH_MASTER_KEY", "QH_SERVER", "QH_KEY", "QH_WORKSPACE")
		if out, err := add.CombinedOutput(); err != nil {
			cleanup()
			return nil, fmt.Errorf("qh conn add %s: %v: %s", c.Name, err, strings.TrimSpace(string(out)))
		}
	}
	cmd := exec.Command(bin, "--home", home, "mcp")
	cmd.Env = withoutEnv(os.Environ(), "QH_MASTER_KEY", "QH_SERVER", "QH_KEY", "QH_WORKSPACE")
	return connect(ctx, "qh", cmd, cleanup)
}

func withoutEnv(env []string, names ...string) []string {
	var out []string
	for _, kv := range env {
		keep := true
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				keep = false
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func dbhubDSN(c Conn) string {
	if c.Engine == "sqlite" {
		return "sqlite://" + c.DSN // c.DSN is absolute, so this is sqlite:///path
	}
	return c.DSN
}

// StartDBHub writes a dbhub.toml for conns (read-only execute_sql, dbhub's
// defaults otherwise) and serves dbhub over stdio through npx.
func StartDBHub(ctx context.Context, version string, conns []Conn) (Server, error) {
	if _, err := exec.LookPath("npx"); err != nil {
		return nil, errors.New("start dbhub: npx is not installed (dbhub needs Node 22.5 or later)")
	}
	dir, err := os.MkdirTemp("", "qh-bench-dbhub-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	var b strings.Builder
	for _, c := range conns {
		fmt.Fprintf(&b, "[[sources]]\nid = %s\ndsn = %s\ndescription = %s\n\n", tomlString(c.Name), tomlString(dbhubDSN(c)), tomlString(c.Description))
		fmt.Fprintf(&b, "[[tools]]\nname = \"execute_sql\"\nsource = %s\nreadonly = true\n\n", tomlString(c.Name))
	}
	path := filepath.Join(dir, "dbhub.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		cleanup()
		return nil, err
	}
	cmd := exec.Command("npx", "-y", "@bytebase/dbhub@"+version, "--transport", "stdio", "--config", path)
	cmd.Dir = dir
	return connect(ctx, "dbhub", cmd, cleanup)
}
```
If the go-sdk v1.8.0 client API differs from the code above, use the SDK's real names and record each change in your report. Check with `go doc github.com/modelcontextprotocol/go-sdk/mcp ClientSession` and `CommandTransport`. In particular, `session.Tools` is an iterator; if it isn't, call `ListTools` instead.

- [ ] **Step 5: Run the tests**

Run: `cd bench && go test ./harness/ -run 'QHServer|DBHubServer' -v && go vet ./...`
Expected: both tests PASS. The dbhub test downloads the pinned package the first time.

- [ ] **Step 6: Commit**

```bash
git add bench/harness/servers.go bench/harness/servers_test.go bench/go.mod bench/go.sum
git commit -m "feat(bench): drive qh and a pinned dbhub over MCP stdio"
```

---

### Task 4: Agent loop, Anthropic model and token counting

**Files:**
- Create: `bench/harness/agent.go`, `bench/harness/anthropic.go`
- Test: `bench/harness/agent_test.go`, `bench/harness/anthropic_test.go`

**Interfaces:**
- Consumes:
  - `Server`, `FindTool`, `BuildQH`, `StartQH`, `ConnFor` (Task 3);
  - `Question`, `Equal`, `SystemPrompt`, `PromptFor` (Task 2);
  - `OpenChinook` (Task 1).
- Produces:
  - Types: `ToolDef{Name, Description string; Schema map[string]any}`, `ToolCall{ID, Name string; Input json.RawMessage}`, `ToolResult{ID, Content string; IsError bool}`, `Usage{Input, Output int64}`, `Reply{Calls []ToolCall; Usage Usage}`.
  - `type Model interface { Start(ctx, system, user string, tools []ToolDef) (*Reply, error); Continue(ctx, results []ToolResult) (*Reply, error) }`.
  - `type ModelFactory func() Model`.
  - `harness.ToolDefs(tools []*mcp.Tool) []ToolDef`.
  - `harness.SubmitTool ToolDef`.
  - Constants `OutputCap = 100000` and `TruncationNote`.
  - `harness.Result`, with JSON tags as in the code.
  - `harness.Run(ctx, m Model, srv Server, ds string, q Question, truth [][]any, run, maxTurns int) Result`.
  - `harness.NewAnthropic(apiKey, model string) ModelFactory`.
  - `harness.CountToolTokens(ctx, apiKey, model string, tools []ToolDef) (int64, error)`.

- [ ] **Step 1: Write the failing tests**

`bench/harness/agent_test.go`:
```go
package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeServer answers every call with out (or errOut when isErr).
type fakeServer struct {
	out   string
	isErr bool
	calls int
}

func (f *fakeServer) Name() string { return "fake" }
func (f *fakeServer) Tools() []*mcp.Tool {
	return []*mcp.Tool{{Name: "query", Description: "run SQL", InputSchema: map[string]any{"type": "object"}}}
}
func (f *fakeServer) Call(context.Context, string, json.RawMessage) (string, bool) {
	f.calls++
	return f.out, f.isErr
}
func (f *fakeServer) Close() error { return nil }

// script replays replies and records what it was sent.
type script struct {
	replies []*Reply
	n       int
	tools   []ToolDef
	sent    [][]ToolResult
}

func (s *script) next() (*Reply, error) {
	if s.n >= len(s.replies) {
		return &Reply{Calls: []ToolCall{{ID: "x", Name: "query", Input: json.RawMessage(`{}`)}}, Usage: Usage{Input: 1, Output: 1}}, nil
	}
	s.n++
	return s.replies[s.n-1], nil
}
func (s *script) Start(_ context.Context, _, _ string, tools []ToolDef) (*Reply, error) {
	s.tools = tools
	return s.next()
}
func (s *script) Continue(_ context.Context, r []ToolResult) (*Reply, error) {
	s.sent = append(s.sent, r)
	return s.next()
}

func call(id, name, input string) ToolCall {
	return ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

var q1 = Question{ID: "chinook-01", Kind: "lookup", Question: "How many tracks? Return: count.", SQL: "SELECT count(*) FROM Track"}

func TestRunSubmitsCorrectAnswer(t *testing.T) {
	s := &script{replies: []*Reply{
		{Calls: []ToolCall{call("a", "query", `{"query":"SELECT count(*) FROM Track"}`)}, Usage: Usage{Input: 100, Output: 20}},
		{Calls: []ToolCall{call("b", "submit_answer", `{"rows":[[3503]]}`)}, Usage: Usage{Input: 150, Output: 10}},
	}}
	srv := &fakeServer{out: "n\n3503\n"}
	r := Run(context.Background(), s, srv, "chinook", q1, [][]any{{int64(3503)}}, 1, 10)
	if !r.Correct || !r.Submitted || r.ToolCalls != 1 || r.Turns != 2 || r.InputTokens != 250 || r.OutputTokens != 30 || r.Error != "" {
		t.Fatalf("%+v", r)
	}
	if r.Dataset != "chinook" || r.Server != "fake" || r.Question != "chinook-01" || r.Run != 1 || r.WallMS < 0 {
		t.Fatalf("labels: %+v", r)
	}
	if len(s.tools) != 2 || s.tools[len(s.tools)-1].Name != "submit_answer" {
		t.Fatalf("tools offered: %+v", s.tools)
	}
}

func TestRunWrongAnswer(t *testing.T) {
	s := &script{replies: []*Reply{{Calls: []ToolCall{call("b", "submit_answer", `{"rows":[[3502]]}`)}}}}
	if r := Run(context.Background(), s, &fakeServer{}, "chinook", q1, [][]any{{int64(3503)}}, 1, 10); r.Correct || !r.Submitted {
		t.Fatalf("%+v", r)
	}
}

func TestRunStopsWithoutSubmit(t *testing.T) {
	s := &script{replies: []*Reply{{}}}
	r := Run(context.Background(), s, &fakeServer{}, "chinook", q1, [][]any{{int64(1)}}, 1, 10)
	if r.Correct || r.Submitted || r.Error != "the model stopped without calling submit_answer" {
		t.Fatalf("%+v", r)
	}
}

func TestRunTurnLimit(t *testing.T) {
	s := &script{} // always calls query
	r := Run(context.Background(), s, &fakeServer{out: "x"}, "chinook", q1, [][]any{{int64(1)}}, 1, 3)
	if r.Correct || r.Turns != 3 || r.Error != "turn limit (3) reached without submit_answer" {
		t.Fatalf("%+v", r)
	}
}

func TestRunCapsToolOutput(t *testing.T) {
	s := &script{replies: []*Reply{
		{Calls: []ToolCall{call("a", "query", `{}`)}},
		{Calls: []ToolCall{call("b", "submit_answer", `{"rows":[[1]]}`)}},
	}}
	r := Run(context.Background(), s, &fakeServer{out: strings.Repeat("x", 250000)}, "chinook", q1, [][]any{{int64(1)}}, 1, 10)
	got := s.sent[0][0].Content
	if r.Truncations != 1 || len(got) != OutputCap+len(TruncationNote) || !strings.HasSuffix(got, TruncationNote) {
		t.Fatalf("truncations %d, len %d", r.Truncations, len(got))
	}
}

func TestRunPassesToolErrorsAndBadSubmits(t *testing.T) {
	s := &script{replies: []*Reply{
		{Calls: []ToolCall{call("a", "query", `{}`)}},
		{Calls: []ToolCall{call("b", "submit_answer", `{"rows":"3503"}`)}},
		{Calls: []ToolCall{call("c", "submit_answer", `{"rows":[[3503]]}`)}},
	}}
	r := Run(context.Background(), s, &fakeServer{out: "error[query_error]: no such column", isErr: true}, "chinook", q1, [][]any{{int64(3503)}}, 1, 10)
	if !r.Correct || !s.sent[0][0].IsError || !s.sent[1][0].IsError || !strings.Contains(s.sent[1][0].Content, "rows must be an array of arrays") {
		t.Fatalf("%+v / %+v", r, s.sent)
	}
}
```

`bench/harness/anthropic_test.go`:
```go
package harness

import (
	"context"
	"os"
	"testing"
)

// TestToolDefinitionBudget is spec §9.1: qh's three tool definitions cost at
// most 800 tokens.
func TestToolDefinitionBudget(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	ds := chinook(t)
	ctx := context.Background()
	bin, cleanup, err := BuildQH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	srv, err := StartQH(ctx, bin, []Conn{ConnFor(ds)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	n, err := CountToolTokens(ctx, key, DefaultModel, ToolDefs(srv.Tools()))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("qh tool definitions: %d tokens", n)
	if n <= 0 || n > 800 {
		t.Fatalf("qh tool definitions cost %d tokens, budget 800", n)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd bench && go test -short ./harness/ -run 'TestRun'`
Expected: FAIL to build (`undefined: Run`, `undefined: Reply`).

- [ ] **Step 3: Implement**

Add the SDK: `cd bench && go get github.com/anthropics/anthropic-sdk-go@latest`.

`bench/harness/agent.go`:
```go
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultModel is the Claude model the benchmark drives unless -model says otherwise.
const DefaultModel = "claude-sonnet-5-5"

// ToolDef is a tool offered to the model.
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // JSON schema of the input
}

// ToolCall is a tool use requested by the model.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers one ToolCall.
type ToolResult struct {
	ID      string
	Content string
	IsError bool
}

// Usage is the token usage of one model reply (input includes cache reads and writes).
type Usage struct{ Input, Output int64 }

// Reply is one model turn.
type Reply struct {
	Calls []ToolCall
	Usage Usage
}

// Model is one conversation with a model. Start opens it; Continue sends
// the results of the previous reply's tool calls.
type Model interface {
	Start(ctx context.Context, system, user string, tools []ToolDef) (*Reply, error)
	Continue(ctx context.Context, results []ToolResult) (*Reply, error)
}

// ModelFactory returns a fresh conversation.
type ModelFactory func() Model

const (
	// OutputCap is the most tool output, in characters, passed to the model.
	OutputCap = 100_000
	// TruncationNote ends a capped tool output.
	TruncationNote = "\n[truncated by the benchmark harness]"
)

// SubmitTool is how the model returns its answer.
var SubmitTool = ToolDef{
	Name:        "submit_answer",
	Description: "Submit the final answer. Call it exactly once, when you are done.",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"rows": map[string]any{
				"type":        "array",
				"description": "One array per result row, with the columns in the order the question asks for.",
				"items":       map[string]any{"type": "array", "items": map[string]any{}},
			},
		},
		"required": []any{"rows"},
	},
}

// ToolDefs converts MCP tools into tool definitions for the model.
func ToolDefs(tools []*mcp.Tool) []ToolDef {
	out := make([]ToolDef, 0, len(tools))
	for _, t := range tools {
		schema := map[string]any{"type": "object"}
		if b, err := json.Marshal(t.InputSchema); err == nil {
			_ = json.Unmarshal(b, &schema)
		}
		out = append(out, ToolDef{Name: t.Name, Description: t.Description, Schema: schema})
	}
	return out
}

// Result is one run of one question against one server.
type Result struct {
	Dataset      string  `json:"dataset"`
	Server       string  `json:"server"`
	Question     string  `json:"question"`
	Kind         string  `json:"kind"`
	Run          int     `json:"run"`
	Correct      bool    `json:"correct"`
	Submitted    bool    `json:"submitted"`
	Answer       [][]any `json:"answer,omitempty"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	ToolCalls    int     `json:"tool_calls"`
	Turns        int     `json:"turns"`
	Truncations  int     `json:"truncations"`
	WallMS       int64   `json:"wall_ms"`
	Error        string  `json:"error,omitempty"`
}

// Run asks one question. The model may call the server's tools until it
// calls submit_answer, stops, or reaches maxTurns replies.
func Run(ctx context.Context, m Model, srv Server, ds string, q Question, truth [][]any, run, maxTurns int) Result {
	r := Result{Dataset: ds, Server: srv.Name(), Question: q.ID, Kind: q.Kind, Run: run}
	start := time.Now()
	defer func() { r.WallMS = time.Since(start).Milliseconds() }()
	tools := append(ToolDefs(srv.Tools()), SubmitTool)
	reply, err := m.Start(ctx, SystemPrompt, PromptFor(q), tools)
	for {
		if err != nil {
			r.Error = "model: " + err.Error()
			return r
		}
		r.Turns++
		r.InputTokens += reply.Usage.Input
		r.OutputTokens += reply.Usage.Output
		if len(reply.Calls) == 0 {
			r.Error = "the model stopped without calling submit_answer"
			return r
		}
		var results []ToolResult
		for _, c := range reply.Calls {
			if c.Name == SubmitTool.Name {
				var in struct{ Rows [][]any }
				if err := json.Unmarshal(c.Input, &in); err != nil {
					results = append(results, ToolResult{ID: c.ID, IsError: true, Content: "rows must be an array of arrays: " + err.Error()})
					continue
				}
				r.Submitted, r.Answer = true, in.Rows
				r.Correct = Equal(truth, in.Rows, q.Ordered)
				results = append(results, ToolResult{ID: c.ID, Content: "answer recorded"})
				continue
			}
			r.ToolCalls++
			out, isErr := srv.Call(ctx, c.Name, c.Input)
			if len(out) > OutputCap {
				out = out[:OutputCap] + TruncationNote
				r.Truncations++
			}
			results = append(results, ToolResult{ID: c.ID, Content: out, IsError: isErr})
		}
		if r.Submitted {
			return r
		}
		if r.Turns >= maxTurns {
			r.Error = fmt.Sprintf("turn limit (%d) reached without submit_answer", maxTurns)
			return r
		}
		reply, err = m.Continue(ctx, results)
	}
}
```
`out[:OutputCap]` can split a UTF-8 character. If the API rejects invalid UTF-8, back off to a rune boundary, and keep the test's expected length: cap the content at `OutputCap` bytes or fewer, and change the test to `<=` if necessary. Record any such change.

`bench/harness/anthropic.go`. Write this against the SDK's v1 API. The names below follow anthropic-sdk-go v1; check each one with `go doc github.com/anthropics/anthropic-sdk-go` and use the real names. The behaviour is binding: same parameters for every request, usage summed across input, cache creation and cache reads, and tool uses collected in order.
```go
package harness

import (
	"context"
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const maxTokens = 2048

type anthropicModel struct {
	client   anthropic.Client
	model    string
	system   string
	tools    []anthropic.ToolUnionParam
	messages []anthropic.MessageParam
}

// NewAnthropic returns a factory of conversations with model.
func NewAnthropic(apiKey, model string) ModelFactory {
	client := anthropic.NewClient(option.WithAPIKey(apiKey), option.WithMaxRetries(6))
	return func() Model { return &anthropicModel{client: client, model: model} }
}

func toolParam(t ToolDef) anthropic.ToolParam {
	p := anthropic.ToolParam{Name: t.Name, Description: anthropic.String(t.Description)}
	p.InputSchema.Properties = t.Schema["properties"]
	if req, ok := t.Schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				p.InputSchema.Required = append(p.InputSchema.Required, s)
			}
		}
	}
	return p
}

func (m *anthropicModel) Start(ctx context.Context, system, user string, tools []ToolDef) (*Reply, error) {
	m.system = system
	for _, t := range tools {
		p := toolParam(t)
		m.tools = append(m.tools, anthropic.ToolUnionParam{OfTool: &p})
	}
	m.messages = []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))}
	return m.send(ctx)
}

func (m *anthropicModel) Continue(ctx context.Context, results []ToolResult) (*Reply, error) {
	blocks := make([]anthropic.ContentBlockParamUnion, len(results))
	for i, r := range results {
		blocks[i] = anthropic.NewToolResultBlock(r.ID, r.Content, r.IsError)
	}
	m.messages = append(m.messages, anthropic.NewUserMessage(blocks...))
	return m.send(ctx)
}

func (m *anthropicModel) send(ctx context.Context) (*Reply, error) {
	msg, err := m.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(m.model),
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: m.system}},
		Messages:  m.messages,
		Tools:     m.tools,
	})
	if err != nil {
		return nil, err
	}
	m.messages = append(m.messages, msg.ToParam())
	u := msg.Usage
	r := &Reply{Usage: Usage{Input: u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens, Output: u.OutputTokens}}
	for _, b := range msg.Content {
		if tu, ok := b.AsAny().(anthropic.ToolUseBlock); ok {
			r.Calls = append(r.Calls, ToolCall{ID: tu.ID, Name: tu.Name, Input: json.RawMessage(tu.Input)})
		}
	}
	return r, nil
}

// noopTool is the baseline for CountToolTokens: the API adds a fixed
// tool-use preamble whenever any tool is present, so tool cost is measured
// as count(noop + tools) - count(noop).
var noopTool = ToolDef{Name: "noop", Description: "Does nothing.", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}

// CountToolTokens measures what the tool definitions add to every request.
func CountToolTokens(ctx context.Context, apiKey, model string, tools []ToolDef) (int64, error) {
	client := anthropic.NewClient(option.WithAPIKey(apiKey), option.WithMaxRetries(6))
	count := func(defs []ToolDef) (int64, error) {
		var params []anthropic.MessageCountTokensToolUnionParam
		for _, t := range defs {
			p := toolParam(t)
			params = append(params, anthropic.MessageCountTokensToolUnionParam{OfTool: &p})
		}
		res, err := client.Messages.CountTokens(ctx, anthropic.MessageCountTokensParams{
			Model:    anthropic.Model(model),
			Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
			Tools:    params,
		})
		if err != nil {
			return 0, err
		}
		return res.InputTokens, nil
	}
	base, err := count([]ToolDef{noopTool})
	if err != nil {
		return 0, err
	}
	with, err := count(append([]ToolDef{noopTool}, tools...))
	if err != nil {
		return 0, err
	}
	return with - base, nil
}
```
The tool's schema may contain keys other than `properties` and `required`, such as `additionalProperties`. Pass those through the SDK's extra-fields mechanism, so the model sees the same schema an MCP client would show it.

- [ ] **Step 4: Run the tests**

Run: `cd bench && go test -short ./harness/ -run TestRun && go vet ./...`
Expected: PASS. With `ANTHROPIC_API_KEY` set, also run `go test ./harness/ -run TestToolDefinitionBudget -v`. It should PASS and log the count.

- [ ] **Step 5: Commit**

```bash
git add bench/harness/agent.go bench/harness/anthropic.go bench/harness/agent_test.go bench/harness/anthropic_test.go bench/go.mod bench/go.sum
git commit -m "feat(bench): add the agent loop, the Anthropic model and tool-definition token counting"
```

---

### Task 5: Micro-benchmarks: cold schema on 1,000 tables, query latency

**Files:**
- Create: `bench/harness/micro.go`
- Test: `bench/harness/micro_test.go`

**Interfaces:**
- Consumes: `Server`, `StartQH`, `StartDBHub`, `FindTool`, `Conn` (Task 3); `StartPagila` (Task 1).
- Produces:
  - `harness.StartGenerated(ctx, tables int) (*Dataset, func(), error)`: Postgres 17 with `tables` tables `t_0000…`, each with 7 columns, a foreign key to the previous table, and a table comment.
  - `harness.ColdSchema(ctx, start func(context.Context) (Server, error), toolPrefix string, args json.RawMessage) (time.Duration, error)`.
  - `harness.QueryLatency(ctx, srv Server, tool string, argsFor func(sql string) json.RawMessage, queries []string, warm, n int) (p50, p95 time.Duration, err error)`.
  - `harness.Percentile(ds []time.Duration, p float64) time.Duration`.
  - `harness.SchemaCall(server, source string) (toolPrefix string, args json.RawMessage)`.
  - `harness.QueryCall(server, source string) (toolPrefix string, argsFor func(string) json.RawMessage)`.

- [ ] **Step 1: Write the failing tests**

`bench/harness/micro_test.go`:
```go
package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	var ds []time.Duration
	for i := 1; i <= 100; i++ {
		ds = append(ds, time.Duration(i)*time.Millisecond)
	}
	if Percentile(ds, 0.50) != 50*time.Millisecond || Percentile(ds, 0.95) != 95*time.Millisecond || Percentile(nil, 0.5) != 0 {
		t.Fatalf("%v %v", Percentile(ds, 0.50), Percentile(ds, 0.95))
	}
}

func TestMicroQH(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Docker")
	}
	ctx := context.Background()
	gen, stop, err := StartGenerated(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	bin, cleanup, err := BuildQH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	start := func(ctx context.Context) (Server, error) { return StartQH(ctx, bin, []Conn{ConnFor(gen)}) }
	prefix, args := SchemaCall("qh", gen.Name)
	d, err := ColdSchema(ctx, start, prefix, args)
	if err != nil || d <= 0 {
		t.Fatalf("cold schema: %v %v", d, err)
	}
	srv, err := start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if out, _ := srv.Call(ctx, "schema", args); !strings.Contains(out, "t_0049") {
		t.Fatalf("generated tables missing: %q", out[:min(len(out), 300)])
	}
	qp, argsFor := QueryCall("qh", gen.Name)
	p50, p95, err := QueryLatency(ctx, srv, FindTool(srv.Tools(), qp), argsFor, []string{"SELECT count(*) FROM t_0001"}, 1, 5)
	if err != nil || p50 <= 0 || p95 < p50 {
		t.Fatalf("latency: %v %v %v", p50, p95, err)
	}
	var probe map[string]any
	if json.Unmarshal(argsFor("SELECT 1"), &probe) != nil || probe["query"] != "SELECT 1" {
		t.Fatal("qh query args")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd bench && go test -short ./harness/ -run TestPercentile`
Expected: FAIL to build (`undefined: Percentile`).

- [ ] **Step 3: Implement**

`bench/harness/micro.go`:
```go
package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// StartGenerated starts Postgres 17 with n generated tables t_0000…, each
// with 7 columns, a foreign key to the previous table and a comment.
func StartGenerated(ctx context.Context, n int) (*Dataset, func(), error) {
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("gen"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("bench-pw"),
		tcpostgres.BasicWaitStrategies())
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return nil, nil, err
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return nil, nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		stop()
		return nil, nil, err
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		fk := ""
		if i > 0 {
			fk = fmt.Sprintf(" REFERENCES t_%04d(id)", i-1)
		}
		fmt.Fprintf(&b, "CREATE TABLE t_%04d (id bigint PRIMARY KEY, name text NOT NULL, status text, amount numeric(12,2), "+
			"created_at timestamptz DEFAULT now(), parent_id bigint%s, note text);\n", i, fk)
		fmt.Fprintf(&b, "COMMENT ON TABLE t_%04d IS 'generated table %d';\n", i, i)
	}
	if _, err := db.ExecContext(ctx, b.String()); err != nil {
		db.Close()
		stop()
		return nil, nil, fmt.Errorf("generate tables: %w", err)
	}
	if _, err := db.ExecContext(ctx, "ANALYZE"); err != nil {
		db.Close()
		stop()
		return nil, nil, err
	}
	return &Dataset{Name: "gen", Engine: "postgres", DSN: dsn, DB: db,
			Description: fmt.Sprintf("Generated schema with %d tables", n)},
		func() { db.Close(); stop() }, nil
}

// SchemaCall is the schema-discovery call measured for a server: qh's
// schema overview, dbhub's search_objects for tables at summary detail.
func SchemaCall(server, source string) (string, json.RawMessage) {
	if server == "qh" {
		return "schema", json.RawMessage(fmt.Sprintf(`{"source":%q}`, source))
	}
	return "search_objects", json.RawMessage(`{"object_type":"table","detail_level":"summary"}`)
}

// QueryCall is the query tool and argument shape for a server.
func QueryCall(server, source string) (string, func(string) json.RawMessage) {
	if server == "qh" {
		return "query", func(q string) json.RawMessage {
			b, _ := json.Marshal(map[string]string{"source": source, "query": q})
			return b
		}
	}
	return "execute_sql", func(q string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"sql": q})
		return b
	}
}

// ColdSchema starts a fresh server and times its first schema call.
func ColdSchema(ctx context.Context, start func(context.Context) (Server, error), toolPrefix string, args json.RawMessage) (time.Duration, error) {
	srv, err := start(ctx)
	if err != nil {
		return 0, err
	}
	defer srv.Close()
	tool := FindTool(srv.Tools(), toolPrefix)
	if tool == "" {
		return 0, fmt.Errorf("%s has no %s* tool", srv.Name(), toolPrefix)
	}
	t0 := time.Now()
	out, isErr := srv.Call(ctx, tool, args)
	d := time.Since(t0)
	if isErr {
		return 0, fmt.Errorf("%s %s: %s", srv.Name(), tool, out)
	}
	return d, nil
}

// QueryLatency runs each query warm times untimed, then n times timed, and
// returns the p50 and p95 of all timed calls.
func QueryLatency(ctx context.Context, srv Server, tool string, argsFor func(string) json.RawMessage, queries []string, warm, n int) (time.Duration, time.Duration, error) {
	if tool == "" {
		return 0, 0, fmt.Errorf("%s has no query tool", srv.Name())
	}
	var ds []time.Duration
	for _, q := range queries {
		for i := 0; i < warm+n; i++ {
			t0 := time.Now()
			out, isErr := srv.Call(ctx, tool, argsFor(q))
			d := time.Since(t0)
			if isErr {
				return 0, 0, fmt.Errorf("%s: %s", srv.Name(), out)
			}
			if i >= warm {
				ds = append(ds, d)
			}
		}
	}
	return Percentile(ds, 0.50), Percentile(ds, 0.95), nil
}

// Percentile returns the nearest-rank percentile p (0..1) of ds.
func Percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}
```
Check the dbhub `search_objects` arguments against the input schema of the pinned version: print `srv.Tools()`. Adjust `SchemaCall` to that version's parameter names for "tables, summary detail" and record the change.

- [ ] **Step 4: Run the tests**

Run: `cd bench && go test -short ./harness/ -run TestPercentile && go test ./harness/ -run TestMicroQH && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add bench/harness/micro.go bench/harness/micro_test.go
git commit -m "feat(bench): add cold-schema and query-latency micro-benchmarks"
```

---

### Task 6: Runner, report, README

**Files:**
- Create: `bench/harness/report.go`, `bench/harness/runner.go`, `bench/cmd/bench/main.go`, `bench/README.md`
- Modify: `Makefile` (root), adding a `bench` target
- Test: `bench/harness/report_test.go`, `bench/harness/runner_test.go`

**Interfaces:**
- Consumes everything from Tasks 1–5.
- Produces:
  - `type harness.Config struct { Model, DBHubVersion string; Datasets, Servers []string; Runs, Limit, Parallel, MaxTurns int; SkipAgent, SkipTokens, SkipMicro bool; APIKey string; Raw io.Writer; Log io.Writer }`.
  - `type harness.Deps struct { Models ModelFactory; StartServer func(ctx, server string, conns []Conn) (Server, error); StartDataset func(ctx, name string) (*Dataset, func(), error); CountTokens func(ctx context.Context, tools []ToolDef) (int64, error) }`.
  - `harness.RunBenchmark(ctx, cfg Config, deps Deps) (*Report, error)`.
  - `type harness.Report struct { … }`, `(*Report).Markdown() string` and `harness.Criteria(*Report) []Criterion`.
  - The `go run ./cmd/bench` runner.

- [ ] **Step 1: Write the failing tests**

`bench/harness/report_test.go`:
```go
package harness

import (
	"strings"
	"testing"
	"time"
)

func sampleReport() *Report {
	r := &Report{Date: "2026-10-11", Model: "claude-sonnet-5-5", Runs: 1, DBHubVersion: "1.5.0",
		ToolTokens: map[string][]int64{"qh": {400, 400, 400, 400, 400}, "dbhub": {500, 1000, 1500, 2000, 2500}},
		ColdSchema: map[string]time.Duration{"qh": 400 * time.Millisecond, "dbhub": 9 * time.Second},
		LatencyP50: map[string]time.Duration{"qh": 2 * time.Millisecond, "dbhub": 4 * time.Millisecond},
		LatencyP95: map[string]time.Duration{"qh": 5 * time.Millisecond, "dbhub": 9 * time.Millisecond}}
	for _, q := range []string{"chinook-01", "chinook-02"} {
		r.Results = append(r.Results,
			Result{Dataset: "chinook", Server: "qh", Question: q, Kind: "lookup", Run: 1, Correct: true, Submitted: true, InputTokens: 900, OutputTokens: 100, ToolCalls: 2, WallMS: 3000},
			Result{Dataset: "chinook", Server: "dbhub", Question: q, Kind: "lookup", Run: 1, Correct: q == "chinook-01", Submitted: true, InputTokens: 1900, OutputTokens: 100, ToolCalls: 3, WallMS: 4000})
	}
	return r
}

func TestSummaryAndCriteria(t *testing.T) {
	r := sampleReport()
	sums := Summarize(r.Results)
	var qh, db Summary
	for _, s := range sums {
		if s.Dataset == "all" && s.Server == "qh" {
			qh = s
		}
		if s.Dataset == "all" && s.Server == "dbhub" {
			db = s
		}
	}
	if qh.Accuracy != 1 || db.Accuracy != 0.5 || qh.AvgTotal != 1000 || db.AvgTotal != 2000 || qh.AvgToolCalls != 2 || db.Runs != 2 {
		t.Fatalf("qh %+v dbhub %+v", qh, db)
	}
	want := map[string]bool{"tokens": true, "accuracy": true, "tool calls": true, "tool definitions": true, "cold schema": true}
	for _, c := range Criteria(r) {
		if !c.Known || c.Pass != want[c.Name] {
			t.Errorf("criterion %+v", c)
		}
	}
	r.ColdSchema["qh"] = 2 * time.Second
	for _, c := range Criteria(r) {
		if c.Name == "cold schema" && c.Pass {
			t.Fatal("2 s cold schema passed")
		}
	}
	empty := &Report{}
	for _, c := range Criteria(empty) {
		if c.Known {
			t.Fatalf("criterion known without data: %+v", c)
		}
	}
}

func TestMarkdown(t *testing.T) {
	md := sampleReport().Markdown()
	for _, want := range []string{
		"# qh vs dbhub benchmark — 2026-10-11",
		"| chinook | qh | 100% |",
		"| Fewer total tokens per task | ≥ 40% fewer than dbhub | 50% fewer | ✅ |",
		"| Cold schema build, 1,000 tables | < 1 s | 0.40 s | ✅ |",
		"| 5 | 400 | 2500 |",
		"| chinook-01 | lookup | 1/1 | 1/1 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
}
```

`bench/harness/runner_test.go`:
```go
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// answerModel submits the right answer for chinook-01 on the first turn.
type answerModel struct{}

func (answerModel) Start(context.Context, string, string, []ToolDef) (*Reply, error) {
	return &Reply{Calls: []ToolCall{{ID: "s", Name: "submit_answer", Input: json.RawMessage(`{"rows":[[3503]]}`)}}, Usage: Usage{Input: 10, Output: 5}}, nil
}
func (answerModel) Continue(context.Context, []ToolResult) (*Reply, error) { return nil, errors.New("unused") }

func fakeDeps(failDBHub bool) Deps {
	return Deps{
		Models: func() Model { return answerModel{} },
		StartDataset: func(ctx context.Context, name string) (*Dataset, func(), error) {
			return OpenChinook(ctx)
		},
		StartServer: func(_ context.Context, server string, _ []Conn) (Server, error) {
			if server == "dbhub" && failDBHub {
				return nil, errors.New("start dbhub: npx is not installed")
			}
			return &namedServer{fakeServer: fakeServer{out: "ok"}, name: server}, nil
		},
		CountTokens: func(context.Context, []ToolDef) (int64, error) { return 123, nil },
	}
}

type namedServer struct {
	fakeServer
	name string
}

func (n *namedServer) Name() string { return n.name }

func TestRunnerServerStartFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads Chinook")
	}
	var raw bytes.Buffer
	cfg := Config{Model: "fake", Datasets: []string{"chinook"}, Servers: []string{"qh", "dbhub"}, Runs: 2, Limit: 1,
		Parallel: 2, MaxTurns: 5, SkipMicro: true, Raw: &raw}
	rep, err := RunBenchmark(context.Background(), cfg, fakeDeps(true))
	if err != nil {
		t.Fatal(err)
	}
	var ok, failed int
	for _, r := range rep.Results {
		switch {
		case r.Server == "qh" && r.Correct:
			ok++
		case r.Server == "dbhub" && strings.Contains(r.Error, "npx is not installed"):
			failed++
		}
	}
	if ok != 2 || failed != 2 {
		t.Fatalf("ok %d failed %d: %+v", ok, failed, rep.Results)
	}
	if rep.ToolTokens["qh"][4] != 123 || len(rep.ToolTokens["qh"]) != 5 {
		t.Fatalf("tool tokens: %v", rep.ToolTokens)
	}
}

func TestRunnerWritesRawIncrementally(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads Chinook")
	}
	var raw bytes.Buffer
	cfg := Config{Model: "fake", Datasets: []string{"chinook"}, Servers: []string{"qh"}, Runs: 3, Limit: 2,
		Parallel: 3, MaxTurns: 5, SkipMicro: true, SkipTokens: true, Raw: &raw}
	rep, err := RunBenchmark(context.Background(), cfg, fakeDeps(false))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(raw.String()), "\n")
	if len(lines) != 6 || len(rep.Results) != 6 {
		t.Fatalf("%d raw lines, %d results", len(lines), len(rep.Results))
	}
	for _, l := range lines {
		var r Result
		if err := json.Unmarshal([]byte(l), &r); err != nil || r.Server != "qh" {
			t.Fatalf("raw line %q: %v", l, err)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd bench && go test -short ./harness/ -run 'Summary|Markdown'`
Expected: FAIL to build (`undefined: Report`, `undefined: Summarize`).

- [ ] **Step 3: Implement**

`bench/harness/report.go`:
```go
package harness

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Report is everything one benchmark run measured.
type Report struct {
	Date, Model, DBHubVersion string
	Runs                      int
	Results                   []Result
	ToolTokens                map[string][]int64 // server → tool-definition tokens for 1..5 connections
	ColdSchema                map[string]time.Duration
	LatencyP50, LatencyP95    map[string]time.Duration
	Notes                     []string // e.g. a phase that could not run
}

// Summary aggregates the results of one dataset (or "all") and server.
type Summary struct {
	Dataset, Server                                  string
	Runs, Errors, Truncations                        int
	Accuracy                                         float64
	AvgTotal, AvgInput, AvgOutput, AvgToolCalls, AvgWallSec float64
}

// Summarize groups results by dataset and server, plus an "all" row per server.
func Summarize(results []Result) []Summary {
	type key struct{ ds, srv string }
	acc := map[key]*Summary{}
	add := func(k key, r Result) {
		s := acc[k]
		if s == nil {
			s = &Summary{Dataset: k.ds, Server: k.srv}
			acc[k] = s
		}
		s.Runs++
		if r.Correct {
			s.Accuracy++
		}
		if r.Error != "" {
			s.Errors++
		}
		s.Truncations += r.Truncations
		s.AvgInput += float64(r.InputTokens)
		s.AvgOutput += float64(r.OutputTokens)
		s.AvgToolCalls += float64(r.ToolCalls)
		s.AvgWallSec += float64(r.WallMS) / 1000
	}
	for _, r := range results {
		add(key{r.Dataset, r.Server}, r)
		add(key{"all", r.Server}, r)
	}
	var out []Summary
	for _, s := range acc {
		n := float64(s.Runs)
		s.Accuracy /= n
		s.AvgInput /= n
		s.AvgOutput /= n
		s.AvgToolCalls /= n
		s.AvgWallSec /= n
		s.AvgTotal = s.AvgInput + s.AvgOutput
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dataset != out[j].Dataset {
			return out[i].Dataset == "all" || (out[j].Dataset != "all" && out[i].Dataset < out[j].Dataset)
		}
		return out[i].Server > out[j].Server // qh before dbhub
	})
	return out
}

// Criterion is one success criterion of spec §1.
type Criterion struct {
	Name, Label, Target, Result string
	Known, Pass                 bool
}

func overall(results []Result, server string) (Summary, bool) {
	for _, s := range Summarize(results) {
		if s.Dataset == "all" && s.Server == server {
			return s, true
		}
	}
	return Summary{}, false
}

// Criteria evaluates spec §1 against the report. Unmeasured criteria are
// returned with Known false.
func Criteria(r *Report) []Criterion {
	qh, okQ := overall(r.Results, "qh")
	db, okD := overall(r.Results, "dbhub")
	both := okQ && okD && db.AvgTotal > 0
	var cs []Criterion
	c := Criterion{Name: "tokens", Label: "Fewer total tokens per task", Target: "≥ 40% fewer than dbhub", Result: "not measured"}
	if both {
		cut := 1 - qh.AvgTotal/db.AvgTotal
		c.Known, c.Pass, c.Result = true, cut >= 0.40, fmt.Sprintf("%.0f%% fewer", cut*100)
	}
	cs = append(cs, c)
	c = Criterion{Name: "accuracy", Label: "Accuracy", Target: "≥ dbhub", Result: "not measured"}
	if both {
		c.Known, c.Pass, c.Result = true, qh.Accuracy >= db.Accuracy, fmt.Sprintf("%.0f%% vs %.0f%%", qh.Accuracy*100, db.Accuracy*100)
	}
	cs = append(cs, c)
	c = Criterion{Name: "tool calls", Label: "Tool calls per task", Target: "fewer than dbhub", Result: "not measured"}
	if both {
		c.Known, c.Pass, c.Result = true, qh.AvgToolCalls < db.AvgToolCalls, fmt.Sprintf("%.1f vs %.1f", qh.AvgToolCalls, db.AvgToolCalls)
	}
	cs = append(cs, c)
	c = Criterion{Name: "tool definitions", Label: "qh tool definitions", Target: "≤ 800 tokens", Result: "not measured"}
	if t := r.ToolTokens["qh"]; len(t) > 0 {
		c.Known, c.Pass, c.Result = true, t[0] <= 800, fmt.Sprintf("%d tokens", t[0])
	}
	cs = append(cs, c)
	c = Criterion{Name: "cold schema", Label: "Cold schema build, 1,000 tables", Target: "< 1 s", Result: "not measured"}
	if d, ok := r.ColdSchema["qh"]; ok {
		c.Known, c.Pass, c.Result = true, d < time.Second, fmt.Sprintf("%.2f s", d.Seconds())
	}
	return append(cs, c)
}

func mark(c Criterion) string {
	switch {
	case !c.Known:
		return "—"
	case c.Pass:
		return "✅"
	}
	return "❌"
}

// Markdown renders the report.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# qh vs dbhub benchmark — %s\n\n", r.Date)
	fmt.Fprintf(&b, "Model `%s` · %d run(s) per question · dbhub %s · tool output capped at %d characters for both servers.\n\n", r.Model, r.Runs, r.DBHubVersion, OutputCap)
	b.WriteString("## Success criteria (spec §1)\n\n| Criterion | Target | Result | Pass |\n|---|---|---|---|\n")
	for _, c := range Criteria(r) {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", c.Label, c.Target, c.Result, mark(c))
	}
	b.WriteString("\n## Results by dataset\n\n| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Errors | Truncated outputs |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range Summarize(r.Results) {
		fmt.Fprintf(&b, "| %s | %s | %.0f%% | %.0f | %.0f | %.0f | %.1f | %.1f | %d | %d | %d |\n", s.Dataset, s.Server, s.Accuracy*100,
			s.AvgTotal, s.AvgInput, s.AvgOutput, s.AvgToolCalls, s.AvgWallSec, s.Runs, s.Errors, s.Truncations)
	}
	if len(r.ToolTokens) > 0 {
		b.WriteString("\n## Tool-definition tokens by number of connections\n\n| Connections | qh | dbhub |\n|---|---|---|\n")
		for i := 0; i < 5; i++ {
			fmt.Fprintf(&b, "| %d | %s | %s |\n", i+1, at(r.ToolTokens["qh"], i), at(r.ToolTokens["dbhub"], i))
		}
	}
	if len(r.ColdSchema)+len(r.LatencyP50) > 0 {
		b.WriteString("\n## Micro-benchmarks\n\n| | qh | dbhub |\n|---|---|---|\n")
		fmt.Fprintf(&b, "| Cold schema, 1,000 tables | %s | %s |\n", dur(r.ColdSchema, "qh"), dur(r.ColdSchema, "dbhub"))
		fmt.Fprintf(&b, "| Query latency p50 | %s | %s |\n", dur(r.LatencyP50, "qh"), dur(r.LatencyP50, "dbhub"))
		fmt.Fprintf(&b, "| Query latency p95 | %s | %s |\n", dur(r.LatencyP95, "qh"), dur(r.LatencyP95, "dbhub"))
	}
	b.WriteString("\n## Per question\n\n| Question | Kind | qh correct | dbhub correct | qh avg tokens | dbhub avg tokens |\n|---|---|---|---|---|---|\n")
	type agg struct {
		kind              string
		ok, n             map[string]int
		tokens            map[string]int64
	}
	byQ := map[string]*agg{}
	var ids []string
	for _, x := range r.Results {
		a := byQ[x.Question]
		if a == nil {
			a = &agg{kind: x.Kind, ok: map[string]int{}, n: map[string]int{}, tokens: map[string]int64{}}
			byQ[x.Question] = a
			ids = append(ids, x.Question)
		}
		a.n[x.Server]++
		a.tokens[x.Server] += x.InputTokens + x.OutputTokens
		if x.Correct {
			a.ok[x.Server]++
		}
	}
	sort.Strings(ids)
	avg := func(a *agg, s string) string {
		if a.n[s] == 0 {
			return "—"
		}
		return fmt.Sprintf("%d", a.tokens[s]/int64(a.n[s]))
	}
	for _, id := range ids {
		a := byQ[id]
		fmt.Fprintf(&b, "| %s | %s | %d/%d | %d/%d | %s | %s |\n", id, a.kind, a.ok["qh"], a.n["qh"], a.ok["dbhub"], a.n["dbhub"], avg(a, "qh"), avg(a, "dbhub"))
	}
	if len(r.Notes) > 0 {
		b.WriteString("\n## Notes\n\n")
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	b.WriteString("\nMethod: tool-definition tokens are `count_tokens(noop tool + server tools) − count_tokens(noop tool)`. " +
		"Total tokens are input (including cache reads and writes) plus output, summed over every model turn of a task.\n")
	return b.String()
}

func at(v []int64, i int) string {
	if i < len(v) {
		return fmt.Sprintf("%d", v[i])
	}
	return "—"
}

func dur(m map[string]time.Duration, k string) string {
	d, ok := m[k]
	if !ok {
		return "—"
	}
	if d >= time.Second {
		return fmt.Sprintf("%.2f s", d.Seconds())
	}
	return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000)
}
```

`bench/harness/runner.go`:
```go
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Config selects what a benchmark run does.
type Config struct {
	Model, DBHubVersion          string
	Datasets, Servers            []string
	Runs, Limit, Parallel, MaxTurns int
	SkipAgent, SkipTokens, SkipMicro bool
	APIKey                       string
	Raw                          io.Writer // one JSON Result per line, written as each run finishes
	Log                          io.Writer // progress; may be nil
}

// Deps are the parts tests replace.
type Deps struct {
	Models       ModelFactory
	StartServer  func(ctx context.Context, server string, conns []Conn) (Server, error)
	StartDataset func(ctx context.Context, name string) (*Dataset, func(), error)
	CountTokens  func(ctx context.Context, tools []ToolDef) (int64, error)
}

// DefaultDeps wires the real model, servers and datasets.
func DefaultDeps(cfg Config, qhBin string) Deps {
	return Deps{
		Models:       NewAnthropic(cfg.APIKey, cfg.Model),
		StartDataset: StartDataset,
		StartServer: func(ctx context.Context, server string, conns []Conn) (Server, error) {
			if server == "qh" {
				return StartQH(ctx, qhBin, conns)
			}
			return StartDBHub(ctx, cfg.DBHubVersion, conns)
		},
		CountTokens: func(ctx context.Context, tools []ToolDef) (int64, error) {
			return CountToolTokens(ctx, cfg.APIKey, cfg.Model, tools)
		},
	}
}

func (cfg Config) logf(format string, a ...any) {
	if cfg.Log != nil {
		fmt.Fprintf(cfg.Log, format+"\n", a...)
	}
}

// RunBenchmark runs the agent phase, the tool-token phase and the
// micro-benchmarks, as configured. Failures of one server or phase are
// recorded and the rest continues.
func RunBenchmark(ctx context.Context, cfg Config, deps Deps) (*Report, error) {
	rep := &Report{Date: time.Now().Format("2006-01-02"), Model: cfg.Model, Runs: cfg.Runs, DBHubVersion: cfg.DBHubVersion,
		ToolTokens: map[string][]int64{}, ColdSchema: map[string]time.Duration{},
		LatencyP50: map[string]time.Duration{}, LatencyP95: map[string]time.Duration{}}
	var mu sync.Mutex
	record := func(r Result) {
		mu.Lock()
		defer mu.Unlock()
		rep.Results = append(rep.Results, r)
		if cfg.Raw != nil {
			b, _ := json.Marshal(r)
			cfg.Raw.Write(append(b, '\n'))
		}
	}
	if !cfg.SkipAgent {
		for _, name := range cfg.Datasets {
			if err := runDataset(ctx, cfg, deps, name, record); err != nil {
				rep.Notes = append(rep.Notes, fmt.Sprintf("dataset %s: %v", name, err))
			}
		}
	}
	if !cfg.SkipTokens {
		if err := toolTokens(ctx, cfg, deps, rep); err != nil {
			rep.Notes = append(rep.Notes, "tool tokens: "+err.Error())
		}
	}
	if !cfg.SkipMicro {
		micro(ctx, cfg, deps, rep)
	}
	return rep, nil
}

func runDataset(ctx context.Context, cfg Config, deps Deps, name string, record func(Result)) error {
	ds, stop, err := deps.StartDataset(ctx, name)
	if err != nil {
		return err
	}
	defer stop()
	qs, err := LoadQuestions(name)
	if err != nil {
		return err
	}
	if cfg.Limit > 0 && cfg.Limit < len(qs) {
		qs = qs[:cfg.Limit]
	}
	truths := make([][][]any, len(qs))
	for i, q := range qs {
		if truths[i], err = Truth(ctx, ds.DB, q.SQL); err != nil {
			return fmt.Errorf("%s reference query: %w", q.ID, err)
		}
	}
	for _, server := range cfg.Servers {
		srv, err := deps.StartServer(ctx, server, []Conn{ConnFor(ds)})
		if err != nil {
			for _, q := range qs {
				for run := 1; run <= cfg.Runs; run++ {
					record(Result{Dataset: name, Server: server, Question: q.ID, Kind: q.Kind, Run: run, Error: err.Error()})
				}
			}
			continue
		}
		cfg.logf("%s/%s: %d questions × %d runs", name, server, len(qs), cfg.Runs)
		jobs := make(chan [2]int)
		var wg sync.WaitGroup
		for w := 0; w < max(1, cfg.Parallel); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range jobs {
					q, run := qs[j[0]], j[1]
					record(Run(ctx, deps.Models(), srv, name, q, truths[j[0]], run, cfg.MaxTurns))
				}
			}()
		}
		for i := range qs {
			for run := 1; run <= cfg.Runs; run++ {
				jobs <- [2]int{i, run}
			}
		}
		close(jobs)
		wg.Wait()
		srv.Close()
	}
	return nil
}

// toolTokens measures tool-definition tokens with Chinook registered 1..5 times.
func toolTokens(ctx context.Context, cfg Config, deps Deps, rep *Report) error {
	ds, stop, err := deps.StartDataset(ctx, "chinook")
	if err != nil {
		return err
	}
	defer stop()
	for _, server := range cfg.Servers {
		for k := 1; k <= 5; k++ {
			var conns []Conn
			for i := 1; i <= k; i++ {
				c := ConnFor(ds)
				c.Name = fmt.Sprintf("chinook%d", i)
				conns = append(conns, c)
			}
			srv, err := deps.StartServer(ctx, server, conns)
			if err != nil {
				rep.Notes = append(rep.Notes, fmt.Sprintf("tool tokens, %s with %d connections: %v", server, k, err))
				break
			}
			n, err := deps.CountTokens(ctx, ToolDefs(srv.Tools()))
			srv.Close()
			if err != nil {
				return err
			}
			rep.ToolTokens[server] = append(rep.ToolTokens[server], n)
		}
	}
	return nil
}

// micro runs the cold-schema and query-latency measurements.
func micro(ctx context.Context, cfg Config, deps Deps, rep *Report) {
	gen, stop, err := StartGenerated(ctx, 1000)
	if err != nil {
		rep.Notes = append(rep.Notes, "cold schema: "+err.Error())
	} else {
		for _, server := range cfg.Servers {
			prefix, args := SchemaCall(server, gen.Name)
			d, err := ColdSchema(ctx, func(ctx context.Context) (Server, error) {
				return deps.StartServer(ctx, server, []Conn{ConnFor(gen)})
			}, prefix, args)
			if err != nil {
				rep.Notes = append(rep.Notes, fmt.Sprintf("cold schema, %s: %v", server, err))
				continue
			}
			rep.ColdSchema[server] = d
		}
		stop()
	}
	ds, stopP, err := deps.StartDataset(ctx, "pagila")
	if err != nil {
		rep.Notes = append(rep.Notes, "query latency: "+err.Error())
		return
	}
	defer stopP()
	qs, _ := LoadQuestions("pagila")
	var queries []string
	for _, q := range qs {
		queries = append(queries, q.SQL)
	}
	for _, server := range cfg.Servers {
		srv, err := deps.StartServer(ctx, server, []Conn{ConnFor(ds)})
		if err != nil {
			rep.Notes = append(rep.Notes, fmt.Sprintf("query latency, %s: %v", server, err))
			continue
		}
		prefix, argsFor := QueryCall(server, ds.Name)
		p50, p95, err := QueryLatency(ctx, srv, FindTool(srv.Tools(), prefix), argsFor, queries, 2, 20)
		srv.Close()
		if err != nil {
			rep.Notes = append(rep.Notes, fmt.Sprintf("query latency, %s: %v", server, err))
			continue
		}
		rep.LatencyP50[server], rep.LatencyP95[server] = p50, p95
	}
}
```

`bench/cmd/bench/main.go`:
```go
// Command bench runs the qh vs dbhub benchmark (spec §9.2).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/anderson-0/qh/bench/harness"
)

func main() {
	var cfg harness.Config
	var datasets, servers, out string
	flag.StringVar(&cfg.Model, "model", harness.DefaultModel, "Claude model id")
	flag.StringVar(&cfg.DBHubVersion, "dbhub-version", harness.DefaultDBHubVersion, "dbhub npm version")
	flag.StringVar(&datasets, "datasets", "pagila,sakila,chinook", "datasets to run")
	flag.StringVar(&servers, "servers", "qh,dbhub", "servers to compare")
	flag.IntVar(&cfg.Runs, "runs", 3, "runs per question")
	flag.IntVar(&cfg.Limit, "limit", 0, "questions per dataset (0 = all)")
	flag.IntVar(&cfg.Parallel, "parallel", 4, "concurrent model conversations per server")
	flag.IntVar(&cfg.MaxTurns, "max-turns", 30, "model replies per question")
	flag.BoolVar(&cfg.SkipAgent, "skip-agent", false, "skip the model runs")
	flag.BoolVar(&cfg.SkipTokens, "skip-tokens", false, "skip tool-definition token counting")
	flag.BoolVar(&cfg.SkipMicro, "skip-micro", false, "skip the micro-benchmarks")
	flag.StringVar(&out, "out", "results", "results directory")
	flag.Parse()
	cfg.Datasets, cfg.Servers = strings.Split(datasets, ","), strings.Split(servers, ",")
	cfg.APIKey = os.Getenv("ANTHROPIC_API_KEY")
	if cfg.APIKey == "" && (!cfg.SkipAgent || !cfg.SkipTokens) {
		fmt.Fprintln(os.Stderr, "bench: ANTHROPIC_API_KEY is required for model runs and token counting (or pass -skip-agent -skip-tokens)")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	dir := filepath.Join(out, time.Now().Format("2006-01-02T150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
	}
	raw, err := os.Create(filepath.Join(dir, "raw.jsonl"))
	if err != nil {
		fail(err)
	}
	defer raw.Close()
	cfg.Raw, cfg.Log = raw, os.Stderr
	bin, cleanup, err := harness.BuildQH(ctx)
	if err != nil {
		fail(err)
	}
	defer cleanup()
	rep, err := harness.RunBenchmark(ctx, cfg, harness.DefaultDeps(cfg, bin))
	if err != nil {
		fail(err)
	}
	path := filepath.Join(dir, "report.md")
	if err := os.WriteFile(path, []byte(rep.Markdown()), 0o644); err != nil {
		fail(err)
	}
	fmt.Println(path)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "bench:", err)
	os.Exit(1)
}
```

Root `Makefile`: add `bench` to `.PHONY` and add this target:
```make
bench:
	cd bench && go run ./cmd/bench $(ARGS)
```

`bench/README.md`:
````markdown
# qh benchmark

Compares qh with [bytebase/dbhub](https://github.com/bytebase/dbhub) on Pagila (Postgres), Sakila (MySQL) and Chinook (SQLite).

Spec §9.2 defines the benchmark and §1 its success criteria.

## What it measures

- **Agent runs:** 30 questions per dataset, asked through a Claude model. Each server is the only tool source in its runs. The benchmark records:
  - tokens, tool calls and wall time per task;
  - accuracy against a reference SQL answer.
- **Tool-definition tokens:** measured with 1 to 5 connections registered, using the token-counting endpoint.
- **Micro-benchmarks:**
  - cold schema build on a generated 1,000-table Postgres database;
  - query latency p50/p95 on Pagila.

## Requirements

- Docker.
- Node 22.5 or later, with `npx`, for dbhub.
- `ANTHROPIC_API_KEY`, for agent runs and token counting.

## Running

```bash
make bench                                   # everything; writes bench/results/<timestamp>/report.md
make bench ARGS="-limit 3 -runs 1"           # a quick, cheap smoke run
make bench ARGS="-skip-agent -skip-tokens"   # micro-benchmarks only, no API key needed
```

A full run makes about 540 model conversations: 90 questions × 3 runs × 2 servers. Check your API budget before starting.

`raw.jsonl` gets one line per finished run, so an interrupted run keeps what it measured.

## Fairness

Both servers get the same model, prompts, turn limit (30) and tool-output cap (100,000 characters). Each runs read-only, with its own defaults:
- qh: 100-row cap.
- dbhub: no row cap.

Answers are submitted through a `submit_answer` tool and compared with the reference rows:
- numbers within a relative 1e-6;
- strings trimmed and compared case-insensitively;
- row order ignored unless the question asks for one.
````

- [ ] **Step 4: Run the tests**

Run: `cd bench && go test -short ./... && go test ./harness/ -run 'Runner' && go vet ./... && gofmt -l .`
Expected:
- all PASS;
- `gofmt -l .` prints nothing;
- the root module's `go test ./...` is unaffected.

- [ ] **Step 5: Commit**

```bash
git add bench Makefile
git commit -m "feat(bench): add the runner, the Markdown report and the README"
```
