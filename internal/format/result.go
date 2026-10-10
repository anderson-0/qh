// Package format renders query results and schemas as compact text for agents.
package format

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anderson-0/qh/internal/engine"
)

// MaxCellRunes is the longest value shown before it is cut.
const MaxCellRunes = 200

var escaper = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)

// Result renders r as TSV with a header row and a one-line footer.
func Result(r *engine.Result) string {
	var b strings.Builder
	if len(r.Columns) > 0 {
		for i, c := range r.Columns {
			if i > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(cell(c.Name))
		}
		b.WriteByte('\n')
		for _, row := range r.Rows {
			for i, v := range row {
				if i > 0 {
					b.WriteByte('\t')
				}
				b.WriteString(value(v))
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString(footer(r))
	return b.String()
}

func value(v engine.Value) string {
	switch {
	case v.Null:
		return "NULL"
	case v.Binary:
		return "<bytes " + humanBytes(v.Size) + ">"
	}
	return cell(v.Text)
}

func cell(s string) string {
	s = truncate(s)
	if !strings.ContainsAny(s, "\\\t\n\r") {
		return s
	}
	return escaper.Replace(s)
}

func truncate(s string) string {
	if len(s) <= MaxCellRunes {
		return s
	}
	n := utf8.RuneCountInString(s)
	if n <= MaxCellRunes {
		return s
	}
	i := 0
	for k := 0; k < MaxCellRunes; k++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i] + fmt.Sprintf("…[+%d chars]", n-MaxCellRunes)
}

func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	case n < 1<<30:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
}

func plural(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func duration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < 10*time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func footer(r *engine.Result) string {
	d := duration(r.Duration)
	if len(r.Columns) == 0 {
		if r.RowsAffected >= 0 {
			return fmt.Sprintf("-- %s affected · %s\n", plural(r.RowsAffected, "row"), d)
		}
		return "-- ok · " + d + "\n"
	}
	n := int64(len(r.Rows))
	if r.Truncated {
		return fmt.Sprintf("-- %s shown, more exist (truncated) · %s\n", plural(n, "row"), d)
	}
	return fmt.Sprintf("-- %s · %s\n", plural(n, "row"), d)
}
