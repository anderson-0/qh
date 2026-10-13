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

// SetListenTiming shortens how long the LISTEN connection waits before it
// pings and how long the ping may take, for one test.
func SetListenTiming(t *testing.T, idle, ping time.Duration) {
	oldIdle, oldPing := listenIdle, listenPingTimeout
	listenIdle, listenPingTimeout = idle, ping
	t.Cleanup(func() { listenIdle, listenPingTimeout = oldIdle, oldPing })
}
