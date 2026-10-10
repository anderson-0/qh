package mysql

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

func TestInspect(t *testing.T) {
	cases := []struct {
		dsn                  string
		host, db, user, pass string
		port                 int
		redacted             string
	}{
		{"mysql://bob:hunter22@db.example.com:3307/shop?tls=true", "db.example.com", "shop", "bob", "hunter22", 3307,
			"mysql://bob:***@db.example.com:3307/shop?tls=true"},
		{"mariadb://bob@db/shop", "db", "shop", "bob", "", 3306, "mariadb://bob@db/shop"},
		{"mysql://bob:pw@[::1]/shop", "::1", "shop", "bob", "pw", 3306, "mysql://bob:***@[::1]/shop"},
	}
	for _, c := range cases {
		info, err := Engine{}.Inspect(c.dsn)
		if err != nil {
			t.Fatalf("%s: %v", c.dsn, err)
		}
		if info.Host != c.host || info.Port != c.port || info.Database != c.db || info.User != c.user || info.Password != c.pass || info.Redacted != c.redacted {
			t.Errorf("%s: %+v", c.dsn, info)
		}
	}

	info, err := Engine{}.Inspect("bob:hunter22@tcp(db:3307)/shop?tls=true")
	if err != nil {
		t.Fatal(err)
	}
	if info.Host != "db" || info.Port != 3307 || info.Database != "shop" || info.Password != "hunter22" {
		t.Fatalf("native form: %+v", info)
	}
	if !strings.Contains(info.Redacted, "bob:***@tcp(db:3307)/shop") || strings.Contains(info.Redacted, "hunter22") || !strings.Contains(info.Redacted, "tls=true") {
		t.Fatalf("native redaction: %q", info.Redacted)
	}

	info, err = Engine{}.Inspect("bob:pw@unix(/var/run/mysqld/mysqld.sock)/shop")
	if err != nil || info.Host != "/var/run/mysqld/mysqld.sock" || info.Port != 0 {
		t.Fatalf("unix socket: %+v %v", info, err)
	}

	_, err = Engine{}.Inspect("mysql://bob:hunter22@db/shop?timeout=notaduration")
	if err == nil || !strings.HasPrefix(err.Error(), "error[invalid]: invalid mysql connection string") || strings.Contains(err.Error(), "hunter22") {
		t.Fatalf("bad dsn: %v", err)
	}
}

func TestParseConfigEnforcesSafety(t *testing.T) {
	cfg, err := parseConfig("mysql://u:p@h/db?multiStatements=true&allowAllFiles=true&parseTime=true&interpolateParams=true")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MultiStatements || cfg.AllowAllFiles || cfg.ParseTime || cfg.InterpolateParams || cfg.Loc != time.UTC || cfg.Timeout != 10*time.Second {
		t.Fatalf("unsafe options kept: %+v", cfg)
	}
	cfg, _ = parseConfig("mysql://u:p@h/db?timeout=3s")
	if cfg.Timeout != 3*time.Second {
		t.Fatalf("user timeout not kept: %v", cfg.Timeout)
	}
}

func TestEngineNames(t *testing.T) {
	if (Engine{}).Name() != "mysql" || (Engine{name: "mariadb"}).Name() != "mariadb" {
		t.Fatal("names")
	}
}

func TestToValue(t *testing.T) {
	cases := []struct {
		kind colKind
		in   []byte
		want engine.Value
	}{
		{kindText, nil, engine.Value{Null: true}},
		{kindText, []byte("Zoë 🚀"), engine.Value{Text: "Zoë 🚀"}},
		{kindBinary, []byte{0xde, 0xad, 0xbe, 0xef}, engine.Value{Binary: true, Size: 4}},
		{kindDatetime, []byte("2025-03-14 10:22:31"), engine.Value{Text: "2025-03-14T10:22:31"}},
		{kindTimestamp, []byte("2025-03-14 10:22:31.5"), engine.Value{Text: "2025-03-14T10:22:31.5Z"}},
		{kindJSON, []byte(`{"a": 1, "b": [1, 2]}`), engine.Value{Text: `{"a":1,"b":[1,2]}`}},
		{kindJSON, []byte(`not json`), engine.Value{Text: "not json"}},
		{kindBit, []byte{0x01}, engine.Value{Text: "1"}},
		{kindBit, []byte{0x01, 0x00}, engine.Value{Text: "256"}},
	}
	for _, c := range cases {
		if got := toValue(c.kind, c.in); got != c.want {
			t.Errorf("toValue(%v, %q) = %+v, want %+v", c.kind, c.in, got, c.want)
		}
	}
	kinds := map[string]colKind{"BLOB": kindBinary, "VARBINARY": kindBinary, "LONGBLOB": kindBinary, "TEXT": kindText,
		"DATETIME": kindDatetime, "TIMESTAMP": kindTimestamp, "JSON": kindJSON, "BIT": kindBit, "UNSIGNED BIGINT": kindText}
	for in, want := range kinds {
		if got := kindOf(in); got != want {
			t.Errorf("kindOf(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPlanResult(t *testing.T) {
	tree := &engine.Result{Columns: []engine.Column{{Name: "EXPLAIN"}}, RowsAffected: -1,
		Rows: [][]engine.Value{{{Text: "-> Rows fetched before execution  (cost=0..0 rows=1)\n    -> Index lookup\n"}}}}
	got := planResult(tree)
	if len(got.Columns) != 1 || got.Columns[0].Name != "plan" || len(got.Rows) != 2 || got.Rows[1][0].Text != "    -> Index lookup" {
		t.Fatalf("tree: %+v", got)
	}
	tabular := &engine.Result{Columns: []engine.Column{{Name: "id"}, {Name: "table"}, {Name: "key"}, {Name: "rows"}}, RowsAffected: -1,
		Rows: [][]engine.Value{{{Text: "1"}, {Text: "orders"}, {Null: true}, {Text: "250"}}}}
	got = planResult(tabular)
	if len(got.Rows) != 1 || got.Rows[0][0].Text != "id=1 table=orders rows=250" {
		t.Fatalf("tabular: %+v", got)
	}
}

func TestParseEnum(t *testing.T) {
	cases := map[string][]string{
		"enum('pending','paid','shipped')": {"pending", "paid", "shipped"},
		"enum('it''s','a,b')":              {"it's", "a,b"},
		"set('x')":                         {"x"},
		"varchar(10)":                      nil,
		"text":                             nil,
	}
	for in, want := range cases {
		if got := parseEnum(in); !reflect.DeepEqual(got, want) {
			t.Errorf("parseEnum(%q) = %#v, want %#v", in, got, want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in      string
		flavor  flavor
		display string
	}{
		{"8.4.11", flavorMySQL, "MySQL 8.4.11"},
		{"8.0.40-0ubuntu0.22.04.1", flavorMySQL, "MySQL 8.0.40"},
		{"11.4.13-MariaDB-ubu2404", flavorMariaDB, "MariaDB 11.4.13"},
		{"10.11.9-MariaDB", flavorMariaDB, "MariaDB 10.11.9"},
	}
	for _, c := range cases {
		f, d := parseVersion(c.in)
		if f != c.flavor || d != c.display {
			t.Errorf("parseVersion(%q) = %v %q", c.in, f, d)
		}
	}
}

func TestClassify(t *testing.T) {
	ctx := context.Background()
	cat := func(err error) qerr.Category { return qerr.From(err).Category }
	e := qerr.From(classify(ctx, &gomysql.MySQLError{Number: 1054, Message: "Unknown column 'u.emial' in 'field list'"}))
	if e.Category != qerr.QueryError || e.UnknownColumn != "u.emial" {
		t.Errorf("1054: %#v", e)
	}
	e = qerr.From(classify(ctx, &gomysql.MySQLError{Number: 1146, Message: "Table 'qh.userz' doesn't exist"}))
	if e.Category != qerr.QueryError || e.UnknownTable != "userz" {
		t.Errorf("1146: %#v", e)
	}
	for _, n := range []uint16{3024, 1969, 1317} {
		if c := cat(classify(ctx, &gomysql.MySQLError{Number: n, Message: "interrupted"})); c != qerr.Timeout {
			t.Errorf("%d: %v", n, c)
		}
	}
	if c := cat(classify(ctx, &gomysql.MySQLError{Number: 1792, Message: "Cannot execute statement in a READ ONLY transaction."})); c != qerr.ReadOnly {
		t.Errorf("1792: %v", c)
	}
	if got := classify(ctx, &gomysql.MySQLError{Number: 1064, Message: "You have an error in your SQL syntax"}).Error(); got != "error[query_error]: You have an error in your SQL syntax" {
		t.Errorf("1064: %q", got)
	}
	if c := cat(classify(ctx, context.DeadlineExceeded)); c != qerr.Timeout {
		t.Errorf("deadline: %v", c)
	}
	if got := connectErr(&gomysql.MySQLError{Number: 1045, Message: "Access denied for user 'u'@'h' (using password: YES)"}).Error(); !strings.HasPrefix(got, "error[connection_failed]: authentication failed: Access denied") {
		t.Errorf("1045: %q", got)
	}
	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	if got := connectErr(netErr).Error(); !strings.HasPrefix(got, "error[connection_failed]: cannot reach the database: dial tcp") || !strings.Contains(got, "connection refused") {
		t.Errorf("net: %q", got)
	}
	if c := cat(connectErr(context.DeadlineExceeded)); c != qerr.ConnectionFailed {
		t.Errorf("connect deadline: %v", c)
	}
}

func TestParseConfigTLSServerNameFromURLHost(t *testing.T) {
	cfg, err := parseConfig("mysql://u:p@db.example.com:3307/x?tls=true")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLS == nil || cfg.TLS.ServerName != "db.example.com" || cfg.Addr != "db.example.com:3307" {
		t.Fatalf("tls host: addr=%q tls=%+v", cfg.Addr, cfg.TLS)
	}
	cfg, err = parseConfig("mysql://u:p@[::1]/x")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "[::1]:3306" {
		t.Fatalf("ipv6 addr: %q", cfg.Addr)
	}
}

func TestParseConfigDropsSessionParams(t *testing.T) {
	cfg, err := parseConfig("mysql://u:p@h/db?sql_select_limit=5&Max_Execution_Time=0&transaction_read_only=0&sql_mode=ANSI")
	if err != nil {
		t.Fatal(err)
	}
	for k := range cfg.Params {
		switch strings.ToLower(k) {
		case "sql_select_limit", "max_execution_time", "max_statement_time", "time_zone", "transaction_read_only", "tx_read_only", "autocommit":
			t.Fatalf("session param %q kept: %+v", k, cfg.Params)
		}
	}
	if cfg.Params["sql_mode"] != "ANSI" {
		t.Fatalf("sql_mode dropped: %+v", cfg.Params)
	}
}

// TestReadOnlyRejectsFileWrites needs no database: Query refuses before it
// touches the pool, so a nil pool proves the refusal came from qh itself.
func TestReadOnlyRejectsFileWrites(t *testing.T) {
	c := &conn{readOnly: true}
	for _, q := range []string{
		"SELECT 1.5INTO OUTFILE '/tmp/qh-evil-a.txt'",
		"SELECT id, 1.0INTO DUMPFILE '/tmp/qh-evil-b.txt' FROM users LIMIT 1",
		"SELECT 1e0INTO OUTFILE '/tmp/qh-evil-c.txt'",
		`SELECT 1 AS "a\" INTO OUTFILE '/tmp/qh-evil-d.txt' -- "`,
		`SELECT 'a\' INTO OUTFILE '/tmp/qh-evil-e.txt' -- '`,
		`SELECT 'a\' INTO DuMpFiLe '/tmp/qh-evil-f.txt' -- '`,
	} {
		_, err := c.Query(context.Background(), q, engine.QueryOpts{MaxRows: 10, ReadOnly: true})
		if e := qerr.From(err); err == nil || e.Category != qerr.ReadOnly {
			t.Errorf("%q: %v", q, err)
		}
	}
	// The per-call flag applies on a read-write connection too.
	rw := &conn{}
	_, err := rw.Query(context.Background(), `SELECT 'a\' INTO OUTFILE '/tmp/qh-evil-g.txt' -- '`, engine.QueryOpts{ReadOnly: true})
	if e := qerr.From(err); err == nil || e.Category != qerr.ReadOnly {
		t.Errorf("per-call read-only: %v", err)
	}
}

func TestClassify1290OnlyReadOnlyWhenServerIsReadOnly(t *testing.T) {
	ctx := context.Background()
	for _, msg := range []string{
		"The MySQL server is running with the --read-only option so it cannot execute this statement",
		"The MariaDB server is running with the --read-only option so it cannot execute this statement",
		"The MySQL server is running with the --super-read-only option so it cannot execute this statement",
		"The server is in read only mode",
	} {
		if e := qerr.From(classify(ctx, &gomysql.MySQLError{Number: 1290, Message: msg})); e.Category != qerr.ReadOnly {
			t.Errorf("%q: %v", msg, e.Category)
		}
	}
	msg := "The MySQL server is running with the --secure-file-priv option so it cannot execute this statement"
	e := qerr.From(classify(ctx, &gomysql.MySQLError{Number: 1290, Message: msg}))
	if e.Category != qerr.QueryError || !strings.Contains(e.Error(), "--secure-file-priv") {
		t.Errorf("secure-file-priv: %v", e)
	}
}

func TestClassifyStatementRaised1044And1049AreQueryErrors(t *testing.T) {
	ctx := context.Background()
	for _, me := range []*gomysql.MySQLError{
		{Number: 1044, Message: "Access denied for user 'u'@'%' to database 'other'"},
		{Number: 1049, Message: "Unknown database 'nope'"},
	} {
		e := qerr.From(classify(ctx, me))
		if e.Category != qerr.QueryError || !strings.Contains(e.Error(), me.Message) {
			t.Errorf("%d: %v", me.Number, e)
		}
		if c := qerr.From(connectErr(me)).Category; c != qerr.ConnectionFailed {
			t.Errorf("connectErr %d: %v", me.Number, c)
		}
	}
}

func TestParseRawQueryValuesWithSlashes(t *testing.T) {
	for _, dsn := range []string{
		"mysql://u:p@h/db?time_zone=Europe%2FParis",
		"mysql://u:p@h/db?time_zone=Europe/Paris",
	} {
		cfg, err := parseRaw(dsn)
		if err != nil {
			t.Errorf("%s: %v", dsn, err)
			continue
		}
		if cfg.Params["time_zone"] != "Europe/Paris" || cfg.DBName != "db" {
			t.Errorf("%s: params=%v db=%q", dsn, cfg.Params, cfg.DBName)
		}
		// qh owns the session time zone, so parseConfig drops it.
		if cfg, err := parseConfig(dsn); err != nil || cfg.Params["time_zone"] != "" {
			t.Errorf("%s: parseConfig params=%v err=%v", dsn, cfg.Params, err)
		}
	}
	// A comma-separated charset list must reach the driver unescaped.
	cfg, err := parseRaw("mysql://u:p@h/db?charset=utf8mb4,utf8")
	if err != nil || !strings.Contains(cfg.FormatDSN(), "charset=utf8mb4,utf8") {
		t.Errorf("charset list: %v %v", cfg, err)
	}
	_, err = parseRaw("mysql://u:p%zz@h/db")
	if err == nil || !strings.Contains(err.Error(), "percent-encoded") {
		t.Errorf("hint: %v", err)
	}
}

func TestAssembleStringDefaultsAreQuotedOnMySQL(t *testing.T) {
	col := func(name, dataType, dflt, extra string) columnRow {
		return columnRow{schema: "s", table: "t", name: name, colType: dataType, dataType: dataType, nullable: "NO", extra: extra,
			dflt: sql.NullString{String: dflt, Valid: true}}
	}
	columns := []columnRow{
		col("a", "varchar", "pending", ""),
		col("b", "text", "it's", ""),
		col("c", "int", "0", ""),
		col("d", "datetime", "CURRENT_TIMESTAMP", "DEFAULT_GENERATED"),
		col("e", "varchar", "concat(_utf8mb4\\'a\\',_utf8mb4\\'b\\')", "DEFAULT_GENERATED"),
		col("f", "varbinary", "0x78", ""), // MySQL reports binary literal defaults as hex
		{schema: "s", table: "t", name: "g", colType: "varchar(9)", dataType: "varchar", nullable: "YES"},
	}
	tables := []tableRow{{schema: "s", name: "t", kind: "BASE TABLE"}}
	cat := (&conn{flavor: flavorMySQL}).assemble(tables, columns, nil, nil)
	want := []string{"'pending'", "'it''s'", "0", "CURRENT_TIMESTAMP", "concat(_utf8mb4\\'a\\',_utf8mb4\\'b\\')", "0x78", ""}
	for i, f := range cat.Namespaces[0].Objects[0].Fields {
		if f.Default != want[i] {
			t.Errorf("%s: default %q, want %q", f.Name, f.Default, want[i])
		}
	}
	// MariaDB already reports quoted string defaults and the string NULL.
	maria := []columnRow{col("a", "varchar", "'pending'", ""), col("b", "varchar", "NULL", "")}
	cat = (&conn{flavor: flavorMariaDB}).assemble(tables, maria, nil, nil)
	fs := cat.Namespaces[0].Objects[0].Fields
	if fs[0].Default != "'pending'" || fs[1].Default != "" {
		t.Errorf("mariadb: %+v", fs)
	}
}

func TestAssembleSortsIndexesAndForeignKeys(t *testing.T) {
	tables := []tableRow{{schema: "s", name: "t", kind: "BASE TABLE"}, {schema: "s", name: "a", kind: "BASE TABLE"}, {schema: "s", name: "b", kind: "BASE TABLE"}}
	indexes := []indexRow{
		{"s", "t", "idx_b", 1, sql.NullString{String: "x", Valid: true}},
		{"s", "t", "Idx_a", 1, sql.NullString{String: "y", Valid: true}},
		{"s", "t", "idx_A", 0, sql.NullString{String: "z", Valid: true}},
	}
	keys := []keyRow{
		{"s", "t", "fk1", "p", "s", "b", "id"},
		{"s", "t", "fk2", "q", "s", "a", "id"},
		{"s", "t", "fk3", "o", "s", "a", "id"},
	}
	cat := (&conn{flavor: flavorMySQL}).assemble(tables, nil, keys, indexes)
	var o *engine.Object
	for _, x := range cat.Namespaces[0].Objects {
		if x.Name == "t" {
			o = x
		}
	}
	var ix, fk []string
	for _, x := range o.Indexes {
		ix = append(ix, x.Name)
	}
	for _, x := range o.ForeignKeys {
		fk = append(fk, x.RefObject+"."+x.Columns[0])
	}
	if !reflect.DeepEqual(ix, []string{"Idx_a", "idx_A", "idx_b"}) || !reflect.DeepEqual(fk, []string{"a.o", "a.q", "b.p"}) {
		t.Errorf("indexes %v foreign keys %v", ix, fk)
	}
}
