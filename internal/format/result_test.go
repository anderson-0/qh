package format

import (
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
)

func txt(s string) engine.Value { return engine.Value{Text: s} }

func TestResultTable(t *testing.T) {
	r := &engine.Result{
		Columns: []engine.Column{{Name: "id"}, {Name: "email"}, {Name: "bio"}, {Name: "avatar"}},
		Rows: [][]engine.Value{
			{txt("1"), txt("a@x.com"), txt("line1\nline2\tx\\y"), {Binary: true, Size: 2150}},
			{txt("2"), txt("b@x.com"), {Null: true}, {Binary: true, Size: 16}},
		},
		RowsAffected: -1,
		Duration:     38 * time.Millisecond,
	}
	want := "id\temail\tbio\tavatar\n" +
		"1\ta@x.com\tline1\\nline2\\tx\\\\y\t<bytes 2.1KB>\n" +
		"2\tb@x.com\tNULL\t<bytes 16B>\n" +
		"-- 2 rows · 38ms\n"
	if got := Result(r); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestResultFooters(t *testing.T) {
	cols := []engine.Column{{Name: "n"}}
	cases := []struct {
		r    *engine.Result
		want string
	}{
		{&engine.Result{Columns: cols, Rows: [][]engine.Value{{txt("1")}}, RowsAffected: -1, Duration: 8 * time.Millisecond}, "n\n1\n-- 1 row · 8ms\n"},
		{&engine.Result{Columns: cols, RowsAffected: -1, Duration: 500 * time.Microsecond}, "n\n-- 0 rows · <1ms\n"},
		{&engine.Result{Columns: cols, Rows: [][]engine.Value{{txt("1")}, {txt("2")}}, Truncated: true, RowsAffected: -1, Duration: 12 * time.Second}, "n\n1\n2\n-- 2 rows shown, more exist (truncated) · 12.0s\n"},
		{&engine.Result{RowsAffected: 3, Duration: 12 * time.Millisecond}, "-- 3 rows affected · 12ms\n"},
		{&engine.Result{RowsAffected: 1, Duration: 12 * time.Millisecond}, "-- 1 row affected · 12ms\n"},
		{&engine.Result{RowsAffected: -1, Duration: 2 * time.Millisecond}, "-- ok · 2ms\n"},
	}
	for _, c := range cases {
		if got := Result(c.r); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestTruncateIsRuneAware(t *testing.T) {
	exact := strings.Repeat("é", MaxCellRunes)
	if got := cell(exact); got != exact {
		t.Fatalf("200 runes should be untouched")
	}
	long := strings.Repeat("é", MaxCellRunes+5)
	want := strings.Repeat("é", MaxCellRunes) + "…[+5 chars]"
	if got := cell(long); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestHugeValueIsBounded(t *testing.T) {
	r := &engine.Result{
		Columns:      []engine.Column{{Name: "doc"}},
		Rows:         [][]engine.Value{{txt(strings.Repeat(`{"k":"v"}`, 120_000))}},
		RowsAffected: -1,
	}
	if got := Result(r); len(got) > 300 {
		t.Fatalf("output is %d bytes", len(got))
	}
}
