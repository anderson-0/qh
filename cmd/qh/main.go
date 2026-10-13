package main

import (
	"os"
	"runtime/debug"

	"github.com/anderson-0/qh/internal/cli"
)

// version is set by release builds (-X main.version=...).
var version = "dev"

func main() {
	os.Exit(cli.Execute(buildVersion(version, debug.ReadBuildInfo), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// buildVersion returns v, or for an unstamped build the module version that
// `go install github.com/anderson-0/qh/cmd/qh@v1.2.3` records.
func buildVersion(v string, info func() (*debug.BuildInfo, bool)) string {
	if v != "dev" {
		return v
	}
	if bi, ok := info(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return v
}
