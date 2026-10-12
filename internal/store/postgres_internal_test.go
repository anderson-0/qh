package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPermanentPGError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "28P01"}, true},  // invalid_password
		{&pgconn.PgError{Code: "28000"}, true},  // invalid_authorization_specification
		{&pgconn.PgError{Code: "3D000"}, true},  // invalid_catalog_name
		{&pgconn.PgError{Code: "42501"}, true},  // insufficient_privilege
		{&pgconn.PgError{Code: "57P04"}, true},  // database_dropped
		{&pgconn.PgError{Code: "57P03"}, false}, // cannot_connect_now: starting up
		{&pgconn.PgError{Code: "53300"}, false}, // too_many_connections
		{&pgconn.PgError{Code: "57P01"}, false}, // admin_shutdown
		{&pgconn.PgError{Code: "57P02"}, false}, // crash_shutdown
		{errors.New("dial tcp: connection refused"), false},
		{nil, false},
	} {
		for _, err := range []error{tc.err, fmt.Errorf("wrapped: %w", tc.err)} {
			if tc.err == nil && err != nil {
				continue
			}
			if got := permanent(err); got != tc.want {
				t.Errorf("permanent(%v) = %v, want %v", err, got, tc.want)
			}
		}
	}
}
