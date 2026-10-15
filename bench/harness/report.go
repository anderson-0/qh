package harness

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Report is everything one benchmark run measured.
type Report struct {
	Date, Model, Effort    string
	Rival, RivalVersion    string // the server qh is compared with; empty Rival means dbhub
	MicroEngine            string
	Runs                   int
	Results                []Result
	ToolTokens             map[string][]int64 // server → tool-definition tokens for 1..5 connections
	ColdSchema             map[string]time.Duration
	LatencyP50, LatencyP95 map[string]time.Duration
	Notes                  []string // e.g. a phase that could not run
}

// rival returns the rival's name, dbhub when the report does not say.
func (r *Report) rival() string {
	if r.Rival == "" {
		return "dbhub"
	}
	return r.Rival
}

// Summary aggregates the results of one dataset (or "all") and server.
type Summary struct {
	Dataset, Server                                         string
	Runs, Aborted, Errors, Truncations                      int
	Accuracy                                                float64
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
		if r.Aborted {
			s.Aborted++
			return
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
		if s.Runs == 0 {
			out = append(out, *s)
			continue
		}
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
		if (out[i].Server == "qh") != (out[j].Server == "qh") {
			return out[i].Server == "qh" // qh before its rival
		}
		return out[i].Server < out[j].Server
	})
	return out
}

// Criterion is one success criterion of spec §1.
type Criterion struct {
	Name, Label, Target, Result string
	Known, Pass                 bool
}

// pairedRuns returns the (dataset, question, run) triples that have a
// non-aborted result for both qh and rival, as parallel slices, and how many
// non-aborted runs each server has in all.
func pairedRuns(results []Result, rival string) (qh, db []Result, qhAll, dbAll int) {
	type key struct {
		ds, q string
		run   int
	}
	byQH, byDB := map[key]Result{}, map[key]Result{}
	var order []key
	for _, r := range results {
		if r.Aborted {
			continue
		}
		k := key{r.Dataset, r.Question, r.Run}
		switch r.Server {
		case "qh":
			qhAll++
			if _, dup := byQH[k]; !dup {
				order = append(order, k)
			}
			byQH[k] = r
		case rival:
			dbAll++
			byDB[k] = r
		}
	}
	for _, k := range order {
		if d, ok := byDB[k]; ok {
			qh, db = append(qh, byQH[k]), append(db, d)
		}
	}
	return qh, db, qhAll, dbAll
}

// pairAvg is the mean of the per-run figures of rs.
type pairAvg struct{ total, calls, accuracy float64 }

func averageOf(rs []Result) pairAvg {
	var a pairAvg
	for _, r := range rs {
		a.total += float64(r.InputTokens + r.OutputTokens)
		a.calls += float64(r.ToolCalls)
		if r.Correct {
			a.accuracy++
		}
	}
	n := float64(len(rs))
	return pairAvg{a.total / n, a.calls / n, a.accuracy / n}
}

// Criteria evaluates spec §1 against the report. The token, accuracy and
// tool-call criteria compare only the runs both servers completed, so a run
// one server lost never tilts the comparison. Unmeasured criteria are
// returned with Known false.
func Criteria(r *Report) []Criterion {
	rival := r.rival()
	pq, pd, _, _ := pairedRuns(r.Results, rival)
	var qh, db pairAvg
	both := len(pq) > 0
	if both {
		qh, db = averageOf(pq), averageOf(pd)
		both = db.total > 0
	}
	paired := fmt.Sprintf(" (%d paired runs)", len(pq))
	var cs []Criterion
	c := Criterion{Name: "tokens", Label: "Fewer total tokens per task", Target: "≥ 40% fewer than " + rival, Result: "not measured"}
	if both {
		cut := 1 - qh.total/db.total
		c.Known, c.Pass, c.Result = true, cut >= 0.40, fmt.Sprintf("%.1f%% fewer", cut*100)+paired
	}
	cs = append(cs, c)
	c = Criterion{Name: "accuracy", Label: "Accuracy", Target: "≥ " + rival, Result: "not measured"}
	if both {
		c.Known, c.Pass, c.Result = true, qh.accuracy >= db.accuracy, fmt.Sprintf("%.1f%% vs %.1f%%", qh.accuracy*100, db.accuracy*100)+paired
	}
	cs = append(cs, c)
	c = Criterion{Name: "tool calls", Label: "Tool calls per task", Target: "fewer than " + rival, Result: "not measured"}
	if both {
		c.Known, c.Pass, c.Result = true, qh.calls < db.calls, fmt.Sprintf("%.1f vs %.1f", qh.calls, db.calls)+paired
	}
	cs = append(cs, c)
	c = Criterion{Name: "tool definitions", Label: "qh tool definitions", Target: "≤ 800 tokens", Result: "not measured"}
	if t := r.ToolTokens["qh"]; len(t) > 0 {
		c.Known, c.Pass, c.Result = true, t[0] <= 800, fmt.Sprintf("%d tokens", t[0])
	}
	cs = append(cs, c)
	c = Criterion{Name: "cold schema", Label: "Cold schema build, 1,000 tables", Target: "< 1 s", Result: "not measured"}
	if r.MicroEngine == "mysql" {
		c.Label = "Cold schema build, 1,000 MySQL tables"
	}
	if r.MicroEngine == "sqlite" {
		c.Label = "Cold schema build, 1,000 SQLite tables"
	}
	if r.MicroEngine == "postgres" {
		c.Label = "Cold schema build, 1,000 Postgres tables"
	}
	limit := time.Second
	if rival == "mongodb-mcp" {
		c.Label, c.Target, limit = "Cold schema build, 1,000 collections", "< 5 s", 5*time.Second
	}
	if d, ok := r.ColdSchema["qh"]; ok {
		c.Known, c.Pass, c.Result = true, d < limit, fmt.Sprintf("%.2f s", d.Seconds())
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
	rival := r.rival()
	fmt.Fprintf(&b, "# qh vs %s benchmark — %s\n\n", rival, r.Date)
	model := "`" + r.Model + "`"
	if r.Effort != "" {
		model += " (effort " + r.Effort + ")"
	}
	fmt.Fprintf(&b, "Model %s · %d run(s) per question · %s %s · tool output capped at %d,%03d bytes for both servers.\n\n", model, r.Runs, rival, r.RivalVersion, OutputCap/1000, OutputCap%1000)
	b.WriteString("## Success criteria (spec §1)\n\n| Criterion | Target | Result | Pass |\n|---|---|---|---|\n")
	for _, c := range Criteria(r) {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", c.Label, c.Target, c.Result, mark(c))
	}
	if pq, _, qhAll, dbAll := pairedRuns(r.Results, rival); len(pq) < qhAll || len(pq) < dbAll {
		fmt.Fprintf(&b, "\nCriteria use the %d runs both servers completed; %d qh and %d %s runs without a counterpart are excluded.\n", len(pq), qhAll-len(pq), dbAll-len(pq), rival)
	}
	b.WriteString("\n## Results by dataset\n\n| Dataset | Server | Accuracy | Avg total tokens | Avg input | Avg output | Avg tool calls | Avg wall (s) | Runs | Aborted | Errors | Truncated outputs |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range Summarize(r.Results) {
		fmt.Fprintf(&b, "| %s | %s | %.0f%% | %.0f | %.0f | %.0f | %.1f | %.1f | %d | %d | %d | %d |\n", s.Dataset, s.Server, s.Accuracy*100,
			s.AvgTotal, s.AvgInput, s.AvgOutput, s.AvgToolCalls, s.AvgWallSec, s.Runs, s.Aborted, s.Errors, s.Truncations)
	}
	if len(r.ToolTokens) > 0 {
		fmt.Fprintf(&b, "\n## Tool-definition tokens by number of connections\n\n| Connections | qh | %s |\n|---|---|---|\n", rival)
		for i := 0; i < 5; i++ {
			fmt.Fprintf(&b, "| %d | %s | %s |\n", i+1, at(r.ToolTokens["qh"], i), at(r.ToolTokens[rival], i))
		}
	}
	if len(r.ColdSchema)+len(r.LatencyP50) > 0 {
		fmt.Fprintf(&b, "\n## Micro-benchmarks\n\n| | qh | %s |\n|---|---|---|\n", rival)
		unit := "tables"
		latencySuffix := ""
		if r.MicroEngine == "mysql" {
			unit, latencySuffix = "MySQL tables", " (MySQL)"
		}
		if r.MicroEngine == "sqlite" {
			unit, latencySuffix = "SQLite tables", " (SQLite)"
		}
		if r.MicroEngine == "postgres" {
			unit, latencySuffix = "Postgres tables", " (Postgres)"
		}
		if rival == "mongodb-mcp" {
			unit = "collections"
		}
		fmt.Fprintf(&b, "| Cold schema, 1,000 %s (median of 3) | %s | %s |\n", unit, dur(r.ColdSchema, "qh"), dur(r.ColdSchema, rival))
		fmt.Fprintf(&b, "| Query latency p50%s | %s | %s |\n", latencySuffix, dur(r.LatencyP50, "qh"), dur(r.LatencyP50, rival))
		fmt.Fprintf(&b, "| Query latency p95%s | %s | %s |\n", latencySuffix, dur(r.LatencyP95, "qh"), dur(r.LatencyP95, rival))
	}
	fmt.Fprintf(&b, "\n## Per question\n\n| Question | Kind | qh correct | %[1]s correct | qh avg tokens | %[1]s avg tokens |\n|---|---|---|---|---|---|\n", rival)
	type agg struct {
		kind   string
		ok, n  map[string]int
		tokens map[string]int64
	}
	byQ := map[string]*agg{}
	var ids []string
	for _, x := range r.Results {
		if x.Aborted {
			continue
		}
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
		fmt.Fprintf(&b, "| %s | %s | %d/%d | %d/%d | %s | %s |\n", id, a.kind, a.ok["qh"], a.n["qh"], a.ok[rival], a.n[rival], avg(a, "qh"), avg(a, rival))
	}
	if len(r.Notes) > 0 {
		b.WriteString("\n## Notes\n\n")
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	b.WriteString("\nMethod: tool-definition tokens are `count_tokens(noop tool + server tools, server instructions as the system prompt) − count_tokens(noop tool)`, so they include the instructions the server sends at initialization. " +
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
