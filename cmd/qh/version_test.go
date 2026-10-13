package main

import (
	"runtime/debug"
	"testing"
)

func TestBuildVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: v}}, true }
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }
	for _, tc := range []struct {
		stamped string
		info    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"1.0.0", info("v9.9.9"), "1.0.0"}, // release builds keep their stamp
		{"dev", info("v1.0.0"), "v1.0.0"},  // go install …@v1.0.0
		{"dev", info("(devel)"), "dev"},    // go build in a checkout
		{"dev", info(""), "dev"},
		{"dev", none, "dev"},
	} {
		if got := buildVersion(tc.stamped, tc.info); got != tc.want {
			t.Errorf("buildVersion(%q) = %q, want %q", tc.stamped, got, tc.want)
		}
	}
}
