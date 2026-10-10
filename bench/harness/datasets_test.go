package harness

import (
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
