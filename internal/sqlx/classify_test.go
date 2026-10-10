package sqlx

import (
	"reflect"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		sql     string
		d       Dialect
		want    Access
		wantErr string
	}{
		{"SELECT 1", Postgres, Read, ""},
		{"  select * from users;", Postgres, Read, ""},
		{"SELECT 1; -- trailing note", Postgres, Read, ""},
		{"SELECT 1 -- note without newline", Postgres, Read, ""},
		{"(SELECT 1) UNION (SELECT 2)", Postgres, Read, ""},
		{"WITH x AS (SELECT 1) SELECT * FROM x", Postgres, Read, ""},
		{"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", Postgres, Write, ""},
		{"SELECT * INTO new_t FROM t", Postgres, Write, ""},
		{"SELECT 'insert into x' AS s", Postgres, Read, ""},
		{`SELECT "update" FROM t`, Postgres, Read, ""},
		{"SELECT $$ DELETE FROM t $$", Postgres, Read, ""},
		{"SELECT $fn$ ; DROP TABLE t $fn$", Postgres, Read, ""},
		{"SELECT E'it\\'s; DROP TABLE t'", Postgres, Read, ""},
		{"SELECT 1 /* ; DROP TABLE t */", Postgres, Read, ""},
		{"SELECT 1 /* outer /* inner */ ; still comment */", Postgres, Read, ""},
		{"SELECT $1", Postgres, Read, ""},
		{"INSERT INTO t VALUES (1)", Postgres, Write, ""},
		{"update t set x = 1", Postgres, Write, ""},
		{"MERGE INTO t USING s ON true WHEN MATCHED THEN DELETE", Postgres, Write, ""},
		{"CREATE TABLE t (x int)", Postgres, DDL, ""},
		{"ALTER TABLE t ADD COLUMN y int", Postgres, DDL, ""},
		{"DROP TABLE t", Postgres, DDL, ""},
		{"COMMENT ON TABLE t IS 'x'", Postgres, DDL, ""},
		{"TRUNCATE t", Postgres, Write, ""},
		{"EXPLAIN SELECT 1", Postgres, Read, ""},
		{"EXPLAIN ANALYZE DELETE FROM t", Postgres, Write, ""},
		{"EXPLAIN (FORMAT TEXT) SELECT 1", Postgres, Read, ""},
		{"EXPLAIN PRAGMA query_only = 0", SQLite, 0, "PRAGMA assignments are not supported"},
		{"EXPLAIN QUERY PLAN PRAGMA table_info(users)", SQLite, Read, ""},
		{"EXPLAIN QUERY PLAN PRAGMA hard_heap_limit = 1", SQLite, 0, "PRAGMA assignments are not supported"},
		{"EXPLAIN PRAGMA journal_mode", SQLite, 0, "PRAGMA assignments are not supported"},
		{"SHOW search_path", Postgres, Read, ""},
		{"TABLE users", Postgres, Read, ""},
		{"VALUES (1), (2)", Postgres, Read, ""},
		{"SELECT nextval('s')", Postgres, Read, ""},
		{"frobnicate the thing", Postgres, Write, ""},
		{"VACUUM", SQLite, Write, ""},
		{"VACUUM INTO '/tmp/x.db'", SQLite, Write, ""},
		{"PRAGMA table_info(users)", SQLite, Read, ""},
		{"PRAGMA main.table_info(users)", SQLite, Read, ""},
		{"PRAGMA query_only = 0", SQLite, 0, "PRAGMA assignments are not supported"},
		{"PRAGMA journal_mode", SQLite, 0, "PRAGMA assignments are not supported"},
		{"PRAGMA main.query_only(0)", SQLite, 0, "PRAGMA assignments are not supported"},
		{"PRAGMA x = 1", Postgres, 0, "PRAGMA assignments are not supported"},
		{"SELECT `update` FROM t", SQLite, Read, ""},
		{"SELECT [delete] FROM t", SQLite, Read, ""},
		{"SELECT 1 # comment ; DROP TABLE t", MySQL, Read, ""},
		{`SELECT "a;b"`, MySQL, Read, ""},
		{"SELECT 'a\\';DROP'", MySQL, Read, ""},
		{"DESCRIBE users", MySQL, Read, ""},
		// Postgres line comments also end at a carriage return.
		{"SELECT 1 --x\r; DROP TABLE t", Postgres, 0, "only one statement per call"},
		{"SELECT 1 --x\nSELECT 2", Postgres, Read, ""},
		{"SELECT 1 --x\r; DROP TABLE t", SQLite, Read, ""},
		// MySQL "--" is a comment only when followed by whitespace, a control
		// character or end of input.
		{"SELECT 1--1; DROP TABLE t", MySQL, 0, "only one statement per call"},
		{"SELECT 1--1", MySQL, Read, ""},
		{"SELECT 1 -- x; DROP TABLE t", MySQL, Read, ""},
		{"SELECT 1 --\n; DROP TABLE t", MySQL, 0, "only one statement per call"},
		// A MySQL number ends at the first character that cannot continue it,
		// so a keyword may follow directly.
		{"SELECT 1.5INTO OUTFILE 'x'", MySQL, Write, ""},
		{"SELECT 1e0INTO OUTFILE 'x'", MySQL, Write, ""},
		{"SELECT 1.0INTO DUMPFILE 'x'", MySQL, Write, ""},
		{"SELECT id, 1.0INTO DUMPFILE 'x' FROM users LIMIT 1", MySQL, Write, ""},
		{"SELECT 1INTO OUTFILE 'x'", MySQL, Write, ""},
		{"SELECT 1e10 AS n", MySQL, Read, ""},
		{"SELECT 1.5e-3, 2E+4, 7. FROM t", MySQL, Read, ""},
		{"SELECT 1.5INTO x FROM t", Postgres, Write, ""},
		// MySQL executable comments run server-side, so they are rejected.
		{"SELECT 1 /*! INTO OUTFILE '/tmp/x' */", MySQL, 0, "executable comments"},
		{"SELECT 1 /*M! INTO OUTFILE '/tmp/x' */", MySQL, 0, "executable comments"},
		{"SELECT 1 /*! x */", Postgres, Read, ""},
		// A semicolon inside quotes or quoted identifiers is not a separator.
		{`SELECT "a;b"`, Postgres, Read, ""},
		{"SELECT `a;b`", SQLite, Read, ""},
		{"SELECT [a;b]", SQLite, Read, ""},
		{"SELECT 'a;b'", Postgres, Read, ""},
		{"SELECT 'a;b'", MySQL, Read, ""},
		{"SELECT 'a;b'", SQLite, Read, ""},
		{"SELECT 1; SELECT 2", Postgres, 0, "only one statement per call"},
		{"COMMIT; DELETE FROM t", Postgres, 0, "only one statement per call"},
		{"BEGIN", Postgres, 0, "BEGIN statements are not supported"},
		{"SET TRANSACTION READ WRITE", Postgres, 0, "SET statements are not supported"},
		{"ATTACH DATABASE 'x.db' AS x", SQLite, 0, "ATTACH statements are not supported"},
		{"USE other_db", MySQL, 0, "USE statements are not supported"},
		{"", Postgres, 0, "query is empty"},
		{"  ;  ", Postgres, 0, "query is empty"},
		{"-- just a comment", Postgres, 0, "query is empty"},
		{"42", Postgres, 0, "does not start with a statement keyword"},
		{"SELECT 'unterminated", Postgres, 0, "unterminated string"},
		{"SELECT 1 /* open", Postgres, 0, "unterminated comment"},
		{"SELECT $$ open", Postgres, 0, "unterminated dollar-quoted string"},
		{`SELECT "open`, Postgres, 0, "unterminated quoted identifier"},
	}
	for _, c := range cases {
		got, err := Classify(c.sql, c.d)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Classify(%q) err = %v, want containing %q", c.sql, err, c.wantErr)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "error[invalid]") {
				t.Errorf("Classify(%q) err category: %v", c.sql, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("Classify(%q) = %v, %v; want %v", c.sql, got, err, c.want)
		}
	}
}

func TestFirstKeyword(t *testing.T) {
	cases := map[string]string{
		"(SELECT 1)":                      "select",
		"  WITH x AS (SELECT 1) SELECT 1": "with",
		"/* c */ values (1)":              "values",
		"SELECT 1; SELECT 2":              "",
		"":                                "",
	}
	for sql, want := range cases {
		if got := FirstKeyword(sql, Postgres); got != want {
			t.Errorf("FirstKeyword(%q) = %q, want %q", sql, got, want)
		}
	}
}

func TestReferencedTables(t *testing.T) {
	cases := []struct {
		sql  string
		want []string
	}{
		{"SELECT * FROM users u JOIN orders o ON o.user_id = u.id", []string{"users", "orders"}},
		{"SELECT * FROM public.users, orders AS o WHERE 1=1", []string{"public.users", "orders"}},
		{`SELECT * FROM "UserAccounts"`, []string{"UserAccounts"}},
		{"UPDATE users SET x = 1", []string{"users"}},
		{"INSERT INTO audit (a) SELECT a FROM src", []string{"audit", "src"}},
		{"SELECT * FROM (SELECT 1) AS s", nil},
		{"SELECT 1", nil},
		{"SELECT 'unterminated", nil},
	}
	for _, c := range cases {
		if got := ReferencedTables(c.sql, Postgres); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ReferencedTables(%q) = %#v, want %#v", c.sql, got, c.want)
		}
	}
}

func FuzzClassify(f *testing.F) {
	for _, s := range []string{"SELECT 1", "WITH x AS (SELECT 1) SELECT 1", "SELECT $$a$$", "SELECT 'a''b'", "PRAGMA x.y(1)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, d := range []Dialect{Postgres, MySQL, SQLite} {
			_, _ = Classify(s, d)
			_ = ReferencedTables(s, d)
			_ = FirstKeyword(s, d)
		}
	})
}
