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
