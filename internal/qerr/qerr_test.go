package qerr

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormat(t *testing.T) {
	cases := []struct {
		err  *Error
		want string
	}{
		{New(Timeout, "query exceeded 30s").WithHint("Add WHERE/LIMIT or narrow the range"),
			"error[timeout]: query exceeded 30s. Add WHERE/LIMIT or narrow the range"},
		{New(Invalid, "query is empty."), "error[invalid]: query is empty"},
		{New(QueryError, "  boom  "), "error[query_error]: boom"},
		{New(ConnectionFailed, "dial a: refused\n\tdial b: refused\r\n").WithHint("try\nagain"),
			"error[connection_failed]: dial a: refused dial b: refused. try again"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestFrom(t *testing.T) {
	base := New(ReadOnly, "connection is read-only")
	wrapped := fmt.Errorf("context: %w", base)
	if got := From(wrapped); got != base {
		t.Fatalf("From did not unwrap: %v", got)
	}
	plain := errors.New("disk full")
	got := From(plain)
	if got.Category != QueryError || got.Message != "disk full" || !errors.Is(got, plain) {
		t.Fatalf("From(plain) = %#v", got)
	}
}
