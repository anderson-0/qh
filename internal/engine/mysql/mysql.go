// Package mysql is the MySQL and MariaDB engine.
package mysql

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	"github.com/anderson-0/qh/internal/sqlx"
)

// Engine is the MySQL/MariaDB engine. The zero value is named "mysql";
// Engine{name: "mariadb"} is an alias with identical behaviour, because the
// flavour is detected at connect time.
type Engine struct{ name string }

func (e Engine) Name() string {
	if e.name == "" {
		return "mysql"
	}
	return e.name
}

func (Engine) Dialect() sqlx.Dialect { return sqlx.MySQL }

func invalidDSN() error {
	return qerr.New(qerr.Invalid, "invalid mysql connection string").
		WithHint("Use mysql://user:password@host:3306/database or user:password@tcp(host:3306)/database")
}

func isURL(u *url.URL) bool { return u.Scheme == "mysql" || u.Scheme == "mariadb" }

// parseRaw parses a mysql:// or mariadb:// URL, or a go-sql-driver DSN,
// without changing any option. Errors never include the input.
func parseRaw(dsn string) (*gomysql.Config, error) {
	dsn = strings.TrimSpace(dsn)
	if u, err := url.Parse(dsn); err == nil && isURL(u) {
		params := "/"
		if u.RawQuery != "" {
			params = "/?" + u.RawQuery
		}
		cfg, err := gomysql.ParseDSN(params)
		if err != nil {
			return nil, invalidDSN()
		}
		if u.User != nil {
			cfg.User = u.User.Username()
			cfg.Passwd, _ = u.User.Password()
		}
		host := u.Hostname()
		if host == "" {
			host = "127.0.0.1"
		}
		port := u.Port()
		if port == "" {
			port = "3306"
		}
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(host, port)
		cfg.DBName = strings.TrimPrefix(u.Path, "/")
		return cfg, nil
	}
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return nil, invalidDSN()
	}
	return cfg, nil
}

// parseConfig is parseRaw plus the options qh always enforces: one statement
// per query, no LOAD DATA LOCAL access to this machine's files, results as
// text, UTC.
func parseConfig(dsn string) (*gomysql.Config, error) {
	cfg, err := parseRaw(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MultiStatements = false
	cfg.AllowAllFiles = false
	cfg.ParseTime = false
	cfg.InterpolateParams = false
	cfg.Loc = time.UTC
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return cfg, nil
}

func (Engine) Inspect(dsn string) (engine.DSNInfo, error) {
	cfg, err := parseRaw(dsn)
	if err != nil {
		return engine.DSNInfo{}, err
	}
	info := engine.DSNInfo{Database: cfg.DBName, User: cfg.User, Password: cfg.Passwd, Redacted: redact(dsn, cfg)}
	if cfg.Net == "unix" {
		info.Host = cfg.Addr
		return info, nil
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		host = cfg.Addr
	}
	info.Host = host
	info.Port, _ = strconv.Atoi(port)
	return info, nil
}

// placeholder stands in for the password while a DSN is re-encoded.
const placeholder = "QHREDACTEDPASSWORD"

func redact(dsn string, cfg *gomysql.Config) string {
	dsn = strings.TrimSpace(dsn)
	if u, err := url.Parse(dsn); err == nil && isURL(u) {
		if u.User != nil {
			if _, ok := u.User.Password(); ok {
				u.User = url.UserPassword(u.User.Username(), placeholder)
			}
		}
		return strings.ReplaceAll(u.String(), placeholder, "***")
	}
	if cfg.Passwd == "" {
		return dsn
	}
	c := cfg.Clone()
	c.Passwd = placeholder
	return strings.ReplaceAll(c.FormatDSN(), placeholder, "***")
}
