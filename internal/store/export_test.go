package store

import (
	"testing"
	"time"
)

// SetPGOpenRetry shortens how long OpenPostgres retries, for one test.
func SetPGOpenRetry(t *testing.T, d time.Duration) {
	old := pgOpenRetry
	pgOpenRetry = d
	t.Cleanup(func() { pgOpenRetry = old })
}
