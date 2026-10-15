package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Config selects what a benchmark run does.
type Config struct {
	Model, Effort                    string
	Rival, RivalVersion              string // "dbhub" (default), "mongodb-mcp", "toolbox" or "toolbox-data"
	MicroEngine                      string // postgres (SQL default), mysql or mongodb
	Datasets, Servers                []string
	Runs, Limit, Parallel, MaxTurns  int
	TokenBudget                      int64 // input+output tokens one run may spend; 0 = no ceiling
	SkipAgent, SkipTokens, SkipMicro bool
	APIKey                           string
	RunTimeout                       time.Duration // wall-time limit of one conversation; 0 means DefaultRunTimeout
	Raw                              io.Writer     // one JSON Result per line, written as each run finishes
	Log                              io.Writer     // progress; may be nil
}

// rival returns cfg.Rival, defaulting to dbhub.
func (cfg Config) rival() string {
	if cfg.Rival == "" {
		return "dbhub"
	}
	return cfg.Rival
}

// Deps are the parts tests replace.
type Deps struct {
	Models         ModelFactory
	StartServer    func(ctx context.Context, server string, conns []Conn) (Server, error)
	StartDataset   func(ctx context.Context, name string) (*Dataset, func(), error)
	StartGenerated func(ctx context.Context, engine string, n int) (*Dataset, func(), error)
	CountTokens    func(ctx context.Context, tools []ToolDef, instructions string) (int64, error)
	// Preflight checks the API key, the model id and the request shape once,
	// before the agent phase; nil skips it.
	Preflight func(ctx context.Context) error
}

// DefaultRunTimeout is how long one conversation may take. A run that exceeds
// it is a failed run, not an aborted one.
const DefaultRunTimeout = 10 * time.Minute

// requestTimeout bounds each attempt of one API request.
const requestTimeout = 5 * time.Minute

// runTimeoutText renders d as "10m" rather than "10m0s".
func runTimeoutText(d time.Duration) string {
	t := d.String()
	if strings.HasSuffix(t, "m0s") {
		t = strings.TrimSuffix(t, "0s")
	}
	if strings.HasSuffix(t, "h0m") {
		t = strings.TrimSuffix(t, "0m")
	}
	return t
}

// errTap remembers the last error its model returned.
type errTap struct {
	Model
	err error
}

func (t *errTap) Start(ctx context.Context, system, user string, tools []ToolDef) (*Reply, error) {
	r, err := t.Model.Start(ctx, system, user, tools)
	t.err = err
	return r, err
}

func (t *errTap) Continue(ctx context.Context, results []ToolResult) (*Reply, error) {
	r, err := t.Model.Continue(ctx, results)
	t.err = err
	return r, err
}

// failureNotes counts, per server, the failed runs that hit the token budget
// or the run timeout; there is no note when there are none.
func failureNotes(results []Result, rival string) []string {
	var notes []string
	for _, kind := range []string{"token budget exceeded", "run timed out"} {
		var qh, db int
		for _, r := range results {
			if r.Aborted || !strings.Contains(r.Error, kind) {
				continue
			}
			if r.Server == "qh" {
				qh++
			} else if r.Server == rival {
				db++
			}
		}
		if qh+db > 0 {
			notes = append(notes, fmt.Sprintf("%d qh and %d %s runs failed with %q (they count against their server)", qh, db, rival, kind))
		}
	}
	return notes
}

// runWithTimeout is Run under the per-run timeout.
func runWithTimeout(ctx context.Context, cfg Config, m Model, srv Server, ds string, q Question, truth [][]any, run int) Result {
	d := cfg.RunTimeout
	if d <= 0 {
		d = DefaultRunTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	tap := &errTap{Model: m}
	r := Run(runCtx, tap, srv, ds, q, truth, run, RunOpts{MaxTurns: cfg.MaxTurns, TokenBudget: cfg.TokenBudget})
	// A run that ended on a transient API error (say a 429 whose backoff the
	// deadline cut short) stays aborted; only a run that ended on the bare
	// deadline, or on something else, is a timeout.
	cutByDeadline := !r.Aborted || errors.Is(tap.err, context.DeadlineExceeded)
	if runCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil && !r.Submitted && cutByDeadline {
		r.Aborted, r.Error = false, "run timed out after "+runTimeoutText(d)
	}
	return r
}

// DefaultDeps wires the real model, servers and datasets.
func DefaultDeps(cfg Config, qhBin string) Deps {
	return Deps{
		Models:       NewAnthropic(cfg.APIKey, cfg.Model, cfg.Effort),
		StartDataset: StartDataset,
		StartGenerated: func(ctx context.Context, engine string, n int) (*Dataset, func(), error) {
			switch engine {
			case "mysql":
				return StartGeneratedMySQL(ctx, n)
			case "mongodb":
				return StartGeneratedMongo(ctx, n)
			default:
				return StartGenerated(ctx, n)
			}
		},
		StartServer: func(ctx context.Context, server string, conns []Conn) (Server, error) {
			switch server {
			case "qh":
				return StartQH(ctx, qhBin, conns)
			case "dbhub":
				return StartDBHub(ctx, cfg.RivalVersion, conns)
			case "mongodb-mcp":
				return StartMongoMCP(ctx, cfg.RivalVersion, conns)
			case "toolbox":
				return StartToolbox(ctx, cfg.RivalVersion, "", conns)
			case "toolbox-data":
				return StartToolbox(ctx, cfg.RivalVersion, "data", conns)
			}
			return nil, fmt.Errorf("unknown server %q", server)
		},
		CountTokens: func(ctx context.Context, tools []ToolDef, instructions string) (int64, error) {
			return CountToolTokens(ctx, cfg.APIKey, cfg.Model, tools, instructions)
		},
		Preflight: func(ctx context.Context) error { return Preflight(ctx, cfg.APIKey, cfg.Model, cfg.Effort) },
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
	if cfg.MicroEngine == "" {
		cfg.MicroEngine = "postgres"
		if cfg.rival() == "mongodb-mcp" {
			cfg.MicroEngine = "mongodb"
		}
	}
	if (cfg.MicroEngine != "postgres" && cfg.MicroEngine != "mysql" && cfg.MicroEngine != "mongodb") ||
		(cfg.MicroEngine == "mongodb") != (cfg.rival() == "mongodb-mcp") {
		return nil, fmt.Errorf("micro-engine %q is incompatible with rival %q (SQL: postgres or mysql; mongodb-mcp: mongodb)", cfg.MicroEngine, cfg.rival())
	}
	rep := &Report{Date: time.Now().Format("2006-01-02"), Model: cfg.Model, Effort: cfg.Effort, Runs: cfg.Runs, Rival: cfg.rival(), RivalVersion: cfg.RivalVersion,
		MicroEngine: cfg.MicroEngine,
		ToolTokens:  map[string][]int64{}, ColdSchema: map[string]time.Duration{},
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
	if !cfg.SkipAgent && deps.Preflight != nil {
		if err := deps.Preflight(ctx); err != nil {
			rep.Notes = append(rep.Notes, fmt.Sprintf("preflight failed, nothing was run: %v", err))
			return rep, nil
		}
	}
	if !cfg.SkipAgent {
		for _, name := range cfg.Datasets {
			if ctx.Err() != nil {
				break
			}
			if err := runDataset(ctx, cfg, deps, name, record); err != nil && ctx.Err() == nil {
				rep.Notes = append(rep.Notes, fmt.Sprintf("dataset %s: %v", name, err))
			}
		}
	}
	if !cfg.SkipTokens && ctx.Err() == nil {
		if err := toolTokens(ctx, cfg, deps, rep); err != nil {
			rep.Notes = append(rep.Notes, "tool tokens: "+err.Error())
		}
	}
	if !cfg.SkipMicro && ctx.Err() == nil {
		micro(ctx, cfg, deps, rep)
	}
	aborted := map[string]int{}
	for _, r := range rep.Results {
		if r.Aborted {
			aborted[r.Server]++
		}
	}
	if len(aborted) > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d qh and %d %s runs were aborted (server start or model API failures) and are excluded from every figure",
			aborted["qh"], aborted[cfg.rival()], cfg.rival()))
	}
	rep.Notes = append(rep.Notes, failureNotes(rep.Results, cfg.rival())...)
	if ctx.Err() != nil {
		rep.Notes = append(rep.Notes, "interrupted; results are partial")
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
		if ctx.Err() != nil {
			return nil
		}
		srv, err := deps.StartServer(ctx, server, []Conn{ConnFor(ds)})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			for _, q := range qs {
				for run := 1; run <= cfg.Runs; run++ {
					record(Result{Dataset: name, Server: server, Question: q.ID, Kind: q.Kind, Run: run, Error: err.Error(), Aborted: true})
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
					if ctx.Err() != nil {
						continue // drain: nothing new starts after an interrupt
					}
					q, run := qs[j[0]], j[1]
					res := runWithTimeout(ctx, cfg, deps.Models(), srv, name, q, truths[j[0]], run)
					if ctx.Err() != nil {
						continue // possibly cut short by the interrupt: not a measurement
					}
					record(res)
				}
			}()
		}
	feed:
		for i := range qs {
			for run := 1; run <= cfg.Runs; run++ {
				select {
				case jobs <- [2]int{i, run}:
				case <-ctx.Done():
					break feed
				}
			}
		}
		close(jobs)
		wg.Wait()
		srv.Close()
	}
	return nil
}

// toolTokens measures tool-definition tokens with Chinook (Pagila as
// documents for mongodb-mcp, Pagila for toolbox) registered 1..5 times.
// mongodb-mcp-server and Toolbox's prebuilt configurations serve one
// connection per process, so they are measured with one. Toolbox is measured
// on Postgres because its tool set depends on the engine, and Postgres is the
// engine of two of the four datasets.
func toolTokens(ctx context.Context, cfg Config, deps Deps, rep *Report) error {
	dsName, maxConns := "chinook", 5
	switch cfg.rival() {
	case "mongodb-mcp":
		dsName, maxConns = "pagila-docs", 1
		rep.Notes = append(rep.Notes, "mongodb-mcp-server serves one connection per process; its tool tokens are measured with one")
	case "toolbox", "toolbox-data":
		dsName, maxConns = "pagila", 1
		rep.Notes = append(rep.Notes, "Toolbox's prebuilt configuration serves one database per process and its tools depend on the engine; tool tokens are measured with one Postgres connection (Pagila). Its MySQL and SQLite tool sets are smaller.")
	}
	ds, stop, err := deps.StartDataset(ctx, dsName)
	if err != nil {
		return err
	}
	defer stop()
	for _, server := range cfg.Servers {
		kmax := maxConns
		if server == "qh" {
			kmax = 5
		}
		for k := 1; k <= kmax && ctx.Err() == nil; k++ {
			var conns []Conn
			for i := 1; i <= k; i++ {
				c := ConnFor(ds)
				c.Name = fmt.Sprintf("%s%d", strings.ReplaceAll(dsName, "-", "_"), i)
				conns = append(conns, c)
			}
			srv, err := deps.StartServer(ctx, server, conns)
			if err != nil {
				rep.Notes = append(rep.Notes, fmt.Sprintf("tool tokens, %s with %d connections: %v", server, k, err))
				break
			}
			n, err := deps.CountTokens(ctx, ToolDefs(srv.Tools()), srv.Instructions())
			srv.Close()
			if err != nil {
				return err
			}
			rep.ToolTokens[server] = append(rep.ToolTokens[server], n)
		}
	}
	return nil
}

// coldRuns is how many fresh-process cold-schema runs are taken per server;
// the median is reported.
const coldRuns = 3

// micro runs the cold-schema and query-latency measurements.
func micro(ctx context.Context, cfg Config, deps Deps, rep *Report) {
	docs := cfg.rival() == "mongodb-mcp"
	latencyDS := "pagila"
	if cfg.MicroEngine == "mysql" {
		latencyDS = "sakila"
	}
	if docs {
		latencyDS = "pagila-docs"
	}
	gen, stop, err := deps.StartGenerated(ctx, cfg.MicroEngine, 1000)
	if err != nil {
		rep.Notes = append(rep.Notes, "cold schema: "+err.Error())
	} else {
		for _, server := range cfg.Servers {
			prefix, args := SchemaCall(server, gen)
			var runs []time.Duration
			for i := 0; i < coldRuns; i++ {
				d, err := ColdSchema(ctx, func(ctx context.Context) (Server, error) {
					return deps.StartServer(ctx, server, []Conn{ConnFor(gen)})
				}, prefix, args)
				if err != nil {
					rep.Notes = append(rep.Notes, fmt.Sprintf("cold schema, %s: %v", server, err))
					runs = nil
					break
				}
				runs = append(runs, d)
				cfg.logf("cold schema (%s), %s run %d/%d: %s", cfg.MicroEngine, server, i+1, coldRuns, d)
			}
			if runs != nil {
				rep.ColdSchema[server] = Percentile(runs, 0.5)
			}
		}
		stop()
		if len(rep.ColdSchema) > 0 {
			note := "Cold schema: qh's overview lists the first 200 of 1,000 tables with a pattern hint (it builds the full catalogue); dbhub's search_objects (summary, limit 1000) lists all 1,000."
			if strings.HasPrefix(cfg.rival(), "toolbox") {
				note = "Cold schema: qh's overview lists the first 200 of 1,000 tables with a pattern hint (it builds the full catalogue); Toolbox's list_tables (output_format simple) lists all 1,000 names. Toolbox connects when it starts, so its figure excludes connecting; qh's includes it."
			}
			if docs {
				note = "Cold schema: qh samples 100 documents from each of 1,000 collections and infers fields and references; mongodb-mcp-server's list-collections returns names only. mongodb-mcp-server connects when it starts, so its figure excludes connecting; qh's includes it."
			}
			rep.Notes = append(rep.Notes, note,
				"Cold schema is the first schema call after process start; it includes any database connection the server makes lazily (qh connects on first use).",
				"The three fresh processes use the same database instance; database caches and statistics can warm between runs. The reported median does not measure a database restart.")
		}
	}
	ds, stopP, err := deps.StartDataset(ctx, latencyDS)
	if err != nil {
		rep.Notes = append(rep.Notes, "query latency: "+err.Error())
		return
	}
	defer stopP()
	qs, _ := LoadQuestions(latencyDS)
	var queries []string
	for _, q := range qs {
		if docs {
			queries = append(queries, string(q.Mongo))
		} else {
			queries = append(queries, q.SQL)
		}
	}
	for _, server := range cfg.Servers {
		srv, err := deps.StartServer(ctx, server, []Conn{ConnFor(ds)})
		if err != nil {
			rep.Notes = append(rep.Notes, fmt.Sprintf("query latency, %s: %v", server, err))
			continue
		}
		prefix, argsFor := QueryCall(server, ds)
		p50, p95, err := QueryLatency(ctx, srv, FindTool(srv.Tools(), prefix), argsFor, queries, 2, 20)
		srv.Close()
		if err != nil {
			rep.Notes = append(rep.Notes, fmt.Sprintf("query latency, %s: %v", server, err))
			continue
		}
		rep.LatencyP50[server], rep.LatencyP95[server] = p50, p95
	}
}
