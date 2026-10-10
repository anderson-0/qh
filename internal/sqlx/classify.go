package sqlx

import (
	"strings"

	"github.com/anderson-0/qh/internal/qerr"
)

// Access is what a statement needs from the database.
type Access int

const (
	Read  Access = iota + 1 // reads only
	Write                   // changes data
	DDL                     // changes the schema; invalidates schema caches
)

func (a Access) String() string {
	switch a {
	case Read:
		return "read"
	case Write:
		return "write"
	case DDL:
		return "ddl"
	}
	return "unknown"
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var (
	sessionControl = set("begin", "start", "commit", "rollback", "end", "abort", "savepoint",
		"release", "set", "reset", "discard", "use", "prepare", "execute", "deallocate",
		"attach", "detach", "listen", "unlisten")
	ddlWords    = set("create", "alter", "drop", "comment", "rename")
	readPragmas = set("table_info", "table_xinfo", "index_list", "index_info", "index_xinfo",
		"foreign_key_list", "database_list", "table_list", "compile_options", "collation_list",
		"function_list", "pragma_list", "module_list")
	// reserved words that end a table reference (they are never aliases).
	reserved = set("select", "where", "join", "inner", "left", "right", "full", "outer", "cross",
		"on", "group", "order", "limit", "union", "natural", "using", "having", "window", "offset",
		"fetch", "for", "lateral", "set", "values", "returning", "as", "and", "or", "not",
		"intersect", "except")
)

func statements(toks []token) [][]token {
	var out [][]token
	start := 0
	for i, t := range toks {
		if t.kind == kSemi {
			if i > start {
				out = append(out, toks[start:i])
			}
			start = i + 1
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

// parse tokenizes sql and returns its single statement.
func parse(sql string, d Dialect) ([]token, error) {
	toks, err := tokenize(sql, d)
	if err != nil {
		return nil, err
	}
	stmts := statements(toks)
	switch len(stmts) {
	case 0:
		return nil, qerr.New(qerr.Invalid, "query is empty")
	case 1:
		return stmts[0], nil
	}
	return nil, qerr.New(qerr.Invalid, "only one statement per call is supported").
		WithHint("Send each statement in its own query call")
}

func firstWord(t []token) string {
	for _, tk := range t {
		if tk.kind == kLParen {
			continue
		}
		if tk.kind == kWord {
			return tk.text
		}
		return ""
	}
	return ""
}

func anyOf(words map[string]bool, keys ...string) bool {
	for _, k := range keys {
		if words[k] {
			return true
		}
	}
	return false
}

// Classify returns the access sql needs. It rejects empty input, more than
// one statement, and session or transaction control statements.
func Classify(sql string, d Dialect) (Access, error) {
	t, err := parse(sql, d)
	if err != nil {
		return 0, err
	}
	words := map[string]bool{}
	for _, tk := range t {
		if tk.kind == kWord {
			words[tk.text] = true
		}
	}
	first := firstWord(t)
	switch {
	case first == "":
		return 0, qerr.New(qerr.Invalid, "query does not start with a statement keyword")
	case sessionControl[first]:
		return 0, qerr.New(qerr.Invalid, strings.ToUpper(first)+" statements are not supported").
			WithHint("Each query call runs in its own transaction")
	case ddlWords[first]:
		return DDL, nil
	case first == "select":
		if words["into"] { // SELECT ... INTO creates a table (Postgres) or writes a file (MySQL)
			return Write, nil
		}
		return Read, nil
	case first == "with":
		if anyOf(words, "insert", "update", "delete", "merge", "into") {
			return Write, nil
		}
		return Read, nil
	case first == "explain":
		// SQLite applies pragmas at prepare time, so EXPLAIN PRAGMA x = y still runs.
		if inner := explainInner(t); len(inner) > 0 && inner[0].kind == kWord && inner[0].text == "pragma" {
			if d == SQLite && readPragma(inner) {
				return Read, nil
			}
			return 0, pragmaErr()
		}
		if words["analyze"] && anyOf(words, "insert", "update", "delete", "merge", "create", "drop", "alter", "truncate", "into") {
			return Write, nil
		}
		return Read, nil
	case first == "values" || first == "table" || first == "show" || first == "describe" || first == "desc":
		return Read, nil
	case first == "pragma":
		if d == SQLite && readPragma(t) {
			return Read, nil
		}
		return 0, pragmaErr()
	}
	return Write, nil // unknown statements fail closed
}

// pragmaErr rejects every pragma that is not a known read pragma, in any
// mode: pragmas change connection state that outlives the statement.
func pragmaErr() error {
	return qerr.New(qerr.Invalid, "PRAGMA assignments are not supported").
		WithHint("Only read pragmas such as table_info or index_list can be queried")
}

// explainInner returns the tokens of the statement an EXPLAIN describes: t
// after the EXPLAIN keyword, its QUERY, PLAN, ANALYZE and VERBOSE modifiers and
// one parenthesized option list.
func explainInner(t []token) []token {
	i := 0
	for i < len(t) && !(t[i].kind == kWord && t[i].text == "explain") {
		i++
	}
	i++
	parens := false
	for i < len(t) {
		switch {
		case t[i].kind == kWord && (t[i].text == "query" || t[i].text == "plan" || t[i].text == "analyze" || t[i].text == "verbose"):
			i++
		case t[i].kind == kLParen && !parens:
			parens = true
			for depth := 0; i < len(t); i++ {
				if t[i].kind == kLParen {
					depth++
				} else if t[i].kind == kRParen {
					if depth--; depth == 0 {
						i++
						break
					}
				}
			}
		default:
			return t[i:]
		}
	}
	return nil
}

// readPragma reports whether t is PRAGMA [schema.]name[(arg)] for a known
// read-only pragma, with nothing assigned.
func readPragma(t []token) bool {
	for _, tk := range t {
		if tk.kind == kEquals {
			return false
		}
	}
	rest := t[1:]
	name := ""
	switch {
	case len(rest) >= 3 && rest[1].kind == kDot:
		name = rest[2].text
	case len(rest) >= 1:
		name = rest[0].text
	}
	return readPragmas[name]
}

// FirstKeyword returns the leading keyword of sql, lowercased, or "" when sql
// is not exactly one parseable statement.
func FirstKeyword(sql string, d Dialect) string {
	t, err := parse(sql, d)
	if err != nil {
		return ""
	}
	return firstWord(t)
}

// ReferencedTables returns the table names that follow FROM, JOIN, UPDATE and
// INTO, as written ("name" or "schema.name"), without duplicates.
func ReferencedTables(sql string, d Dialect) []string {
	toks, err := tokenize(sql, d)
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for i, t := range toks {
		if t.kind != kWord {
			continue
		}
		switch t.text {
		case "from", "join", "update", "into":
		default:
			continue
		}
		j := i + 1
		for {
			name, next := readName(toks, j)
			if name == "" {
				break
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
			j = skipAlias(toks, next)
			if t.text == "from" && j < len(toks) && toks[j].kind == kComma {
				j++
				continue
			}
			break
		}
	}
	return out
}

func isName(t token) bool {
	return t.kind == kQuoted || t.kind == kWord && !reserved[t.text]
}

func readName(toks []token, j int) (string, int) {
	if j >= len(toks) || !isName(toks[j]) {
		return "", j
	}
	name := toks[j].text
	j++
	if j+1 < len(toks) && toks[j].kind == kDot && (toks[j+1].kind == kWord || toks[j+1].kind == kQuoted) {
		name += "." + toks[j+1].text
		j += 2
	}
	return name, j
}

func skipAlias(toks []token, j int) int {
	if j < len(toks) && toks[j].kind == kWord && toks[j].text == "as" {
		return j + 2
	}
	if j < len(toks) && isName(toks[j]) {
		return j + 1
	}
	return j
}
