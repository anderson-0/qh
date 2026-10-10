package mysql

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/anderson-0/qh/internal/engine"
)

type colKind int

const (
	kindText colKind = iota
	kindBinary
	kindDatetime
	kindTimestamp
	kindJSON
	kindBit
)

// kindOf maps go-sql-driver's DatabaseTypeName to how values are rendered.
// The driver reports binary-charset blobs and strings as BLOB/BINARY/VARBINARY
// and text ones as TEXT/CHAR/VARCHAR.
func kindOf(dbType string) colKind {
	switch strings.ToUpper(dbType) {
	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "GEOMETRY":
		return kindBinary
	case "DATETIME":
		return kindDatetime
	case "TIMESTAMP":
		return kindTimestamp
	case "JSON":
		return kindJSON
	case "BIT":
		return kindBit
	}
	return kindText
}

// toValue converts a text-protocol value to its canonical form. TIMESTAMP is
// rendered in UTC with Z because every session runs with time_zone '+00:00'.
func toValue(k colKind, b []byte) engine.Value {
	if b == nil {
		return engine.Value{Null: true}
	}
	switch k {
	case kindBinary:
		return engine.Value{Binary: true, Size: len(b)}
	case kindDatetime:
		return engine.Value{Text: strings.Replace(string(b), " ", "T", 1)}
	case kindTimestamp:
		return engine.Value{Text: strings.Replace(string(b), " ", "T", 1) + "Z"}
	case kindJSON:
		var buf bytes.Buffer
		if json.Compact(&buf, b) == nil {
			return engine.Value{Text: buf.String()}
		}
	case kindBit:
		var n uint64
		for _, x := range b {
			n = n<<8 | uint64(x)
		}
		return engine.Value{Text: strconv.FormatUint(n, 10)}
	}
	return engine.Value{Text: string(b)}
}

// planResult shapes EXPLAIN output as one "plan" column. MySQL's FORMAT=TREE
// returns one multi-line value (split into lines); MariaDB's tabular EXPLAIN
// becomes one "column=value …" line per row, skipping NULLs.
func planResult(r *engine.Result) *engine.Result {
	out := &engine.Result{Columns: []engine.Column{{Name: "plan", Type: "text"}}, RowsAffected: -1, Duration: r.Duration, Truncated: r.Truncated}
	if len(r.Columns) == 1 {
		for _, row := range r.Rows {
			for _, line := range strings.Split(row[0].Text, "\n") {
				if strings.TrimSpace(line) != "" {
					out.Rows = append(out.Rows, []engine.Value{{Text: line}})
				}
			}
		}
		return out
	}
	for _, row := range r.Rows {
		var parts []string
		for i, v := range row {
			if !v.Null && v.Text != "" {
				parts = append(parts, r.Columns[i].Name+"="+v.Text)
			}
		}
		out.Rows = append(out.Rows, []engine.Value{{Text: strings.Join(parts, " ")}})
	}
	return out
}

// parseEnum returns the values of an enum(...) or set(...) COLUMN_TYPE, or
// nil for any other type. Quotes inside values are doubled, as in SQL.
func parseEnum(colType string) []string {
	open := strings.IndexByte(colType, '(')
	if open < 0 || !strings.HasSuffix(colType, ")") {
		return nil
	}
	s := colType[open+1 : len(colType)-1]
	var out []string
	var b strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case !in && ch == '\'':
			in = true
			b.Reset()
		case in && ch == '\'' && i+1 < len(s) && s[i+1] == '\'':
			b.WriteByte('\'')
			i++
		case in && ch == '\'':
			in = false
			out = append(out, b.String())
		case in:
			b.WriteByte(ch)
		}
	}
	return out
}

type flavor int

const (
	flavorMySQL flavor = iota
	flavorMariaDB
)

// parseVersion reads VERSION() output, e.g. "8.4.11" or "11.4.13-MariaDB-ubu2404".
func parseVersion(v string) (flavor, string) {
	f, name := flavorMySQL, "MySQL"
	if strings.Contains(strings.ToLower(v), "mariadb") {
		f, name = flavorMariaDB, "MariaDB"
	}
	num, _, _ := strings.Cut(v, "-")
	return f, name + " " + num
}
