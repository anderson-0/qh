// Package harness runs the qh vs dbhub benchmark: datasets, questions,
// servers under test, the agent loop and the report.
package harness

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite"
)

// Dataset is a loaded sample database.
type Dataset struct {
	Name        string
	Engine      string // postgres, mysql, sqlite or mongodb
	DSN         string // what the servers under test connect with
	Description string // the same text is given to both servers
	DB          *sql.DB
}

// source is one pinned download.
type source struct{ file, url, sha256 string }

// Pinned in Task 1 Step 1. Pagila is pinned to the last commit before the
// July 2026 rewrite (uuidv7() columns and the pgvector extension), which needs
// PostgreSQL 18 plus pgvector; this one loads on plain postgres:17-alpine.
var sources = map[string]source{
	"pagila-schema": {"pagila-schema.sql", "https://raw.githubusercontent.com/devrimgunduz/pagila/5ba5a57aeb159f75f02aca2432d3c262186d13d3/pagila-schema.sql", "8ce358e4c8014087b85296694a0893887bd7a4190e3ce407f2721b86b98e5707"},
	"pagila-data":   {"pagila-data.sql", "https://raw.githubusercontent.com/devrimgunduz/pagila/5ba5a57aeb159f75f02aca2432d3c262186d13d3/pagila-data.sql", "880580fb2cd4daaa99f290ced264988cdd657b3158be63cd281466f796f6dbf2"},
	"sakila":        {"sakila-db.tar.gz", "https://downloads.mysql.com/docs/sakila-db.tar.gz", "683e957d456341e48cccd66135ddb0d5701a8436523ed0a6366279f8e8719a68"},
	"chinook":       {"chinook.sqlite", "https://raw.githubusercontent.com/lerocha/chinook-database/7f67772503d71ba90f19283c38e93923addb43fa/ChinookDatabase/DataSources/Chinook_Sqlite.sqlite", "7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15"},
}

// CacheDir is where downloads are kept: $QH_BENCH_CACHE, or bench/.cache.
func CacheDir() string {
	if d := os.Getenv("QH_BENCH_CACHE"); d != "" {
		return d
	}
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", ".cache")
}

// Fetch downloads url into cacheDir/name unless it is already there, and
// checks its SHA-256 either way.
func Fetch(ctx context.Context, cacheDir, name, url, sha256hex string) (string, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(cacheDir, name)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := download(ctx, url, path); err != nil {
			return "", err
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != sha256hex {
		os.Remove(path)
		return "", fmt.Errorf("%s: sha256 mismatch: got %s, want %s", name, got, sha256hex)
	}
	return path, nil
}

func download(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, res.StatusCode)
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fetchSource(ctx context.Context, key string) (string, error) {
	s := sources[key]
	return Fetch(ctx, CacheDir(), s.file, s.url, s.sha256)
}

// StartDataset starts or opens the named dataset.
func StartDataset(ctx context.Context, name string) (*Dataset, func(), error) {
	switch name {
	case "pagila":
		return StartPagila(ctx)
	case "sakila":
		return StartSakila(ctx)
	case "chinook":
		return OpenChinook(ctx)
	case "lendco":
		return StartLendco(ctx)
	case "pagila-docs":
		return StartPagilaDocs(ctx)
	case "lendco-docs":
		return StartLendcoDocs(ctx)
	}
	return nil, nil, fmt.Errorf("unknown dataset %q (want pagila, sakila, chinook, lendco, pagila-docs or lendco-docs)", name)
}

// StartPagila loads Pagila into a Postgres 17 container.
func StartPagila(ctx context.Context) (*Dataset, func(), error) {
	return startPagila(ctx, "pagila", "Pagila: a DVD rental store (films, actors, customers, rentals, payments) on PostgreSQL")
}

//go:embed lendco.sql
var lendcoSQL []byte

// StartLendco loads Pagila and rebuilds it under unfamiliar names
// (lendco.sql), so answers need schema discovery rather than memory.
func StartLendco(ctx context.Context) (*Dataset, func(), error) {
	dir, err := os.MkdirTemp("", "qh-bench-lendco-")
	if err != nil {
		return nil, nil, err
	}
	script := filepath.Join(dir, "lendco.sql")
	if err := os.WriteFile(script, lendcoSQL, 0o644); err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	ds, stop, err := startPagila(ctx, "lendco", "Lendco: a media lending service (catalog, performers, members, loans, charges) on PostgreSQL", script)
	os.RemoveAll(dir) // the container has its own copy
	return ds, stop, err
}

// startPagila loads Pagila, then the extra scripts, into a Postgres 17
// container.
func startPagila(ctx context.Context, name, description string, extra ...string) (*Dataset, func(), error) {
	schema, err := fetchSource(ctx, "pagila-schema")
	if err != nil {
		return nil, nil, err
	}
	data, err := fetchSource(ctx, "pagila-data")
	if err != nil {
		return nil, nil, err
	}
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("pagila"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("bench-pw"),
		tcpostgres.WithOrderedInitScripts(append([]string{schema, data}, extra...)...), tcpostgres.BasicWaitStrategies())
	stop := func() { _ = testcontainers.TerminateContainer(ctr) }
	if err != nil {
		stop()
		return nil, nil, err
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return nil, nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		stop()
		return nil, nil, err
	}
	return &Dataset{Name: name, Engine: "postgres", DSN: dsn, DB: db, Description: description},
		func() { db.Close(); stop() }, nil
}

// StartSakila loads Sakila into a MySQL 8.4 container.
func StartSakila(ctx context.Context) (*Dataset, func(), error) {
	archive, err := fetchSource(ctx, "sakila")
	if err != nil {
		return nil, nil, err
	}
	// Extract the verified archive afresh every run, so a stale or truncated
	// extraction is never reused.
	dir, err := os.MkdirTemp("", "qh-bench-sakila-")
	if err != nil {
		return nil, nil, err
	}
	if err := untar(archive, dir); err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	schema, data := filepath.Join(dir, "sakila-db", "sakila-schema.sql"), filepath.Join(dir, "sakila-db", "sakila-data.sql")
	ctr, err := tcmysql.Run(ctx, "mysql:8.4",
		tcmysql.WithDatabase("sakila"), tcmysql.WithUsername("bench"), tcmysql.WithPassword("bench-pw"),
		orderedMySQLScripts(schema, data))
	stop := func() { _ = testcontainers.TerminateContainer(ctr); os.RemoveAll(dir) }
	if err != nil {
		stop()
		return nil, nil, err
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		stop()
		return nil, nil, err
	}
	port, err := ctr.MappedPort(ctx, "3306/tcp")
	if err != nil {
		stop()
		return nil, nil, err
	}
	url := fmt.Sprintf("mysql://bench:bench-pw@%s:%s/sakila", host, port.Port())
	db, err := sql.Open("mysql", fmt.Sprintf("bench:bench-pw@tcp(%s:%s)/sakila", host, port.Port()))
	if err != nil {
		stop()
		return nil, nil, err
	}
	return &Dataset{Name: "sakila", Engine: "mysql", DSN: url, DB: db,
			Description: "Sakila: a DVD rental store (films, actors, customers, rentals, payments) on MySQL"},
		func() { db.Close(); stop() }, nil
}

// orderedMySQLScripts copies scripts into /docker-entrypoint-initdb.d with a
// numeric prefix: the image runs them in file-name order, and sakila-data.sql
// sorts before sakila-schema.sql (tcmysql.WithScripts keeps the plain names).
func orderedMySQLScripts(scripts ...string) testcontainers.CustomizeRequestOption {
	files := make([]testcontainers.ContainerFile, 0, len(scripts))
	for i, s := range scripts {
		files = append(files, testcontainers.ContainerFile{
			HostFilePath:      s,
			ContainerFilePath: fmt.Sprintf("/docker-entrypoint-initdb.d/%03d-%s", i, filepath.Base(s)),
			FileMode:          0o755,
		})
	}
	return testcontainers.WithFiles(files...)
}

// untar extracts the .sql files of a .tar.gz into dir.
func untar(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(h.Name, ".sql") || strings.Contains(h.Name, "..") {
			continue
		}
		out := filepath.Join(dir, filepath.Clean(h.Name))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		w, err := os.Create(out)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, tr); err != nil {
			w.Close()
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	}
}

// OpenChinook copies the cached Chinook file to a temp dir, so runs never
// change the cached copy.
func OpenChinook(ctx context.Context) (*Dataset, func(), error) {
	src, err := fetchSource(ctx, "chinook")
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "qh-bench-chinook-")
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "chinook.sqlite")
	b, err := os.ReadFile(src)
	if err == nil {
		err = os.WriteFile(path, b, 0o644)
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	return &Dataset{Name: "chinook", Engine: "sqlite", DSN: path, DB: db,
			Description: "Chinook: a digital music store (artists, albums, tracks, playlists, customers, invoices) on SQLite"},
		func() { db.Close(); os.RemoveAll(dir) }, nil
}
