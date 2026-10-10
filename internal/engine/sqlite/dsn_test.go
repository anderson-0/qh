package sqlite

import (
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
)

func TestBuildDSN(t *testing.T) {
	if got, err := (Engine{}).BuildDSN(engine.DSNFields{Database: "/data/shop.db"}); err != nil || got != "/data/shop.db" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := (Engine{}).BuildDSN(engine.DSNFields{}); err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: database (the file path) is required") {
		t.Fatalf("%v", err)
	}
	if _, err := (Engine{}).BuildDSN(engine.DSNFields{Host: "h", Database: "x.db"}); err == nil ||
		!strings.HasPrefix(err.Error(), "error[invalid]: sqlite takes only database, the path of the database file") {
		t.Fatalf("%v", err)
	}
}
