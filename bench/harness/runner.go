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
	Model, DBHubVersion              string
	Datasets, Servers                []string
	Runs, Limit, Parallel, MaxTurns  int
	SkipAgent, SkipTokens, SkipMicro bool
	APIKey                           string
	Raw                              io.Writer // one JSON Result per line, written as each run finishes
	Log                              io.Writer // progress; may be nil
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
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d qh and %d dbhub runs were aborted (server start or model API failures) and are excluded from every figure",
			aborted["qh"], aborted["dbhub"]))
	}
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
					res := Run(ctx, deps.Models(), srv, name, q, truths[j[0]], run, cfg.MaxTurns)
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

// toolTokens measures tool-definition tokens with Chinook registered 1..5 times.
func toolTokens(ctx context.Context, cfg Config, deps Deps, rep *Report) error {
	ds, stop, err := deps.StartDataset(ctx, "chinook")
	if err != nil {
		return err
	}
	defer stop()
	for _, server := range cfg.Servers {
		for k := 1; k <= 5 && ctx.Err() == nil; k++ {
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

// coldRuns is how many fresh-process cold-schema runs are taken per server;
// the median is reported.
const coldRuns = 3

// micro runs the cold-schema and query-latency measurements.
func micro(ctx context.Context, cfg Config, deps Deps, rep *Report) {
	gen, stop, err := StartGenerated(ctx, 1000)
	if err != nil {
		rep.Notes = append(rep.Notes, "cold schema: "+err.Error())
	} else {
		for _, server := range cfg.Servers {
			prefix, args := SchemaCall(server, gen.Name)
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
			}
			if runs != nil {
				rep.ColdSchema[server] = Percentile(runs, 0.5)
			}
		}
		stop()
		if len(rep.ColdSchema) > 0 {
			rep.Notes = append(rep.Notes,
				"Cold schema: qh's overview lists the first 200 of 1,000 tables with a pattern hint (it builds the full catalogue); dbhub's search_objects (summary, limit 1000) lists all 1,000.",
				"Cold schema is the first schema call after process start; it includes any database connection the server makes lazily (qh connects on first use).")
		}
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
