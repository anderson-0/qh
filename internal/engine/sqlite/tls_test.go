package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/testutil"
)

func TestTLSRefused(t *testing.T) {
	spec := engine.ConnSpec{ID: "x", DSN: testutil.SQLiteFixture(t), Mode: engine.ModeRO, PoolSize: 1, TLS: &engine.TLS{Mode: engine.TLSRequire}}
	_, err := Engine{}.Open(context.Background(), spec)
	if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: tls applies only to network databases") {
		t.Fatalf("%v", err)
	}
}
