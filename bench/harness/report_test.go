package harness

import (
	"strings"
	"testing"
	"time"
)

func sampleReport() *Report {
	r := &Report{Date: "2026-10-11", Model: "claude-sonnet-5-5", Effort: "high", Runs: 1, DBHubVersion: "1.5.0",
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
		"| chinook | qh | 100% |",
		"| Fewer total tokens per task | ≥ 40% fewer than dbhub | 50.0% fewer | ✅ |",
		"| Cold schema build, 1,000 tables | < 1 s | 0.40 s | ✅ |",
		"| 5 | 400 | 2500 |",
		"| chinook-01 | lookup | 1/1 | 1/1 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
}
