package harness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchVerifiesAndCaches(t *testing.T) {
	body := "hello dataset\n"
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte(body))
	}))
	defer srv.Close()
	sum := sha256.Sum256([]byte(body))
	good := hex.EncodeToString(sum[:])
	dir := t.TempDir()
	path, err := Fetch(context.Background(), dir, "x.sql", srv.URL, good)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != body || filepath.Dir(path) != dir {
		t.Fatalf("content %q at %s", b, path)
	}
	if _, err := Fetch(context.Background(), dir, "x.sql", srv.URL, good); err != nil || hits != 1 {
		t.Fatalf("second fetch should hit the cache: hits=%d err=%v", hits, err)
	}
	_, err = Fetch(context.Background(), t.TempDir(), "y.sql", srv.URL, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestDatasets(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads datasets and needs Docker")
	}
	for name, want := range map[string]struct {
		query string
		n     int
	}{
		"pagila":  {"SELECT count(*) FROM film", 1000},
		"sakila":  {"SELECT count(*) FROM film", 1000},
		"chinook": {"SELECT count(*) FROM Track", 3503},
		"lendco":  {"SELECT count(*) FROM catalog_item", 1000},
	} {
		t.Run(name, func(t *testing.T) {
			ds, stop, err := StartDataset(context.Background(), name)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			var n int
			if err := ds.DB.QueryRow(want.query).Scan(&n); err != nil || n != want.n {
				t.Fatalf("%s: %d %v", want.query, n, err)
			}
			if ds.Name != name || ds.DSN == "" || ds.Description == "" {
				t.Fatalf("dataset fields: %+v", ds)
			}
		})
	}
}

func TestUntarExtractsSQLAndStaysInside(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"sakila-db/sakila-schema.sql": "schema",
		"sakila-db/readme.txt":        "not sql",
		"../evil.sql":                 "evil",
		"/abs-evil.sql":               "evil",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()

	parent := t.TempDir()
	archive := filepath.Join(parent, "a.tar.gz")
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "out")
	if err := untar(archive, dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "sakila-db", "sakila-schema.sql")); err != nil || string(b) != "schema" {
		t.Fatalf("schema: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sakila-db", "readme.txt")); err == nil {
		t.Fatal("non-sql entry extracted")
	}
	if _, err := os.Stat(filepath.Join(parent, "evil.sql")); err == nil {
		t.Fatal("../evil.sql escaped the destination")
	}
	// Nothing may land outside dir (an absolute name stays inside it).
	err := filepath.WalkDir(parent, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && p != archive && !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			t.Errorf("file outside destination: %s", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
