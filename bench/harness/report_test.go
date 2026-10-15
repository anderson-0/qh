package harness

import (
	"strings"
	"testing"
	"time"
)

func sampleReport() *Report {
	r := &Report{Date: "2026-10-11", Model: "claude-sonnet-5-5", Effort: "high", Runs: 1, RivalVersion: "1.5.0",
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

func TestAbortedRunsAreExcluded(t *testing.T) {
	r := sampleReport()
	// qh never started for two more runs; they must not count anywhere.
	r.Results = append(r.Results,
		Result{Dataset: "chinook", Server: "qh", Question: "chinook-03", Kind: "lookup", Run: 1, Error: "start qh: boom", Aborted: true},
		Result{Dataset: "chinook", Server: "qh", Question: "chinook-04", Kind: "lookup", Run: 1, Error: "model: 529", Aborted: true})
	for _, s := range Summarize(r.Results) {
		if s.Dataset == "all" && s.Server == "qh" && (s.Runs != 2 || s.Aborted != 2 || s.Accuracy != 1 || s.AvgTotal != 1000 || s.AvgToolCalls != 2 || s.Errors != 0) {
			t.Fatalf("aborted runs leaked into %+v", s)
		}
	}
	if !strings.Contains(r.Markdown(), "| Runs | Aborted |") {
		t.Fatal("markdown lacks the Aborted column")
	}

	// qh failed to start at all: nothing may pass on the strength of zero tokens.
	var failed []Result
	for _, x := range sampleReport().Results {
		if x.Server == "dbhub" {
			failed = append(failed, x)
		} else {
			failed = append(failed, Result{Dataset: x.Dataset, Server: "qh", Question: x.Question, Run: 1, Error: "start qh: boom", Aborted: true})
		}
	}
	r = sampleReport()
	r.Results = failed
	for _, c := range Criteria(r) {
		switch c.Name {
		case "tokens", "accuracy", "tool calls":
			if c.Known || c.Pass || c.Result != "not measured" {
				t.Errorf("criterion %q with no qh runs: %+v", c.Name, c)
			}
		}
	}
}

func TestMarkdown(t *testing.T) {
	md := sampleReport().Markdown()
	for _, want := range []string{
		"# qh vs dbhub benchmark — 2026-10-11",
		"Model `claude-sonnet-5-5` (effort high) · 1 run(s)",
		"tool output capped at 100,000 bytes for both servers",
		"| chinook | qh | 100% |",
		"| Fewer total tokens per task | ≥ 40% fewer than dbhub | 50.0% fewer (2 paired runs) | ✅ |",
		"| Cold schema build, 1,000 tables | < 1 s | 0.40 s | ✅ |",
		"| 5 | 400 | 2500 |",
		"| chinook-01 | lookup | 1/1 | 1/1 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
}

func TestMySQLMicroReport(t *testing.T) {
	r := sampleReport()
	r.MicroEngine = "mysql"
	md := r.Markdown()
	for _, want := range []string{"Cold schema build, 1,000 MySQL tables", "Cold schema, 1,000 MySQL tables (median of 3)", "Query latency p50 (MySQL)"} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q", want)
		}
	}
}

func TestCriteriaUseOnlyPairedRuns(t *testing.T) {
	r := sampleReport() // chinook: 2 questions, both servers, qh 1000 vs dbhub 2000 tokens
	// pagila: qh ran twice (expensive, 5000 tokens, 9 calls, wrong), dbhub was aborted both times.
	for _, q := range []string{"pagila-01", "pagila-02"} {
		r.Results = append(r.Results,
			Result{Dataset: "pagila", Server: "qh", Question: q, Kind: "lookup", Run: 1, Submitted: true, InputTokens: 4900, OutputTokens: 100, ToolCalls: 9, WallMS: 1000},
			Result{Dataset: "pagila", Server: "dbhub", Question: q, Kind: "lookup", Run: 1, Error: "model: 529", Aborted: true})
	}
	// and one chinook run only dbhub completed
	r.Results = append(r.Results,
		Result{Dataset: "chinook", Server: "dbhub", Question: "chinook-03", Kind: "lookup", Run: 1, Correct: true, Submitted: true, InputTokens: 100, OutputTokens: 0, ToolCalls: 1},
		Result{Dataset: "chinook", Server: "qh", Question: "chinook-03", Kind: "lookup", Run: 1, Error: "model: 529", Aborted: true})
	for _, c := range Criteria(r) {
		switch c.Name {
		case "tokens":
			if !c.Known || !c.Pass || c.Result != "50.0% fewer (2 paired runs)" {
				t.Errorf("tokens: %+v", c)
			}
		case "accuracy":
			if !c.Known || !c.Pass || c.Result != "100.0% vs 50.0% (2 paired runs)" {
				t.Errorf("accuracy: %+v", c)
			}
		case "tool calls":
			if !c.Known || !c.Pass || c.Result != "2.0 vs 3.0 (2 paired runs)" {
				t.Errorf("tool calls: %+v", c)
			}
		}
	}
	md := r.Markdown()
	want := "Criteria use the 2 runs both servers completed; 2 qh and 1 dbhub runs without a counterpart are excluded"
	if !strings.Contains(md, want) {
		t.Errorf("markdown lacks %q", want)
	}
	// per-question table: aborted rows do not count
	for _, row := range []string{
		"| pagila-01 | lookup | 0/1 | 0/0 | 5000 | — |",
		"| chinook-03 | lookup | 0/0 | 1/1 | — | 100 |",
	} {
		if !strings.Contains(md, row) {
			t.Errorf("per-question table lacks %q\n%s", row, md)
		}
	}
	// no counterpart anywhere: nothing is measured
	only := &Report{Results: []Result{
		{Dataset: "chinook", Server: "qh", Question: "chinook-01", Run: 1, Submitted: true, InputTokens: 1},
		{Dataset: "chinook", Server: "dbhub", Question: "chinook-02", Run: 1, Submitted: true, InputTokens: 1}}}
	for _, c := range Criteria(only) {
		if (c.Name == "tokens" || c.Name == "accuracy" || c.Name == "tool calls") && (c.Known || c.Result != "not measured") {
			t.Errorf("%+v", c)
		}
	}
	// a question whose every run aborted has no row
	r = &Report{Results: []Result{{Dataset: "chinook", Server: "qh", Question: "chinook-09", Run: 1, Aborted: true}}}
	if strings.Contains(r.Markdown(), "chinook-09") {
		t.Error("all-aborted question listed")
	}
}

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
