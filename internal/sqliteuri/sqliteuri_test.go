package sqliteuri

import (
	"net/url"
	"testing"
)

const query = "mode=ro&_busy_timeout=5000"

// Unix paths keep the URI qh built before Windows paths were handled.
func TestUnixUnchanged(t *testing.T) {
	for _, p := range []string{
		"/tmp/a.db",
		"/tmp/with space/shop.db",
		"/data/Zoë 🚀/a%41%b.db",
		"/data/odd?#;&=+,$'!~@:x.db",
		`/data/back\slash/C:\x.db`,
		"/C:/x.db",
		"//server/share/x.db",
	} {
		for _, q := range []string{query, ""} {
			old := (&url.URL{Scheme: "file", Path: p, RawQuery: q}).String()
			if got := build(p, q, false); got != old {
				t.Errorf("build(%q, %q) = %q, was %q", p, q, got, old)
			}
		}
	}
	for p, want := range map[string]string{
		"/tmp/with space/shop.db": "file:///tmp/with%20space/shop.db?" + query,
		"/data/a%41?#b.db":        "file:///data/a%2541%3F%23b.db?" + query,
	} {
		if got := build(p, query, false); got != want {
			t.Errorf("build(%q) = %q, want %q", p, got, want)
		}
	}
}

// Windows paths follow SQLite's URI rules: an empty authority, a "/" in front
// of the drive letter and forward slashes. A UNC path keeps its two leading
// slashes after the authority, and a \\?\ path keeps its backslashes.
func TestWindows(t *testing.T) {
	for p, want := range map[string]string{
		`C:\data\shop.db`:                  "file:///C:/data/shop.db",
		`c:\shop.db`:                       "file:///c:/shop.db",
		`D:\with space\Zoë 🚀\shop.db`:      "file:///D:/with%20space/Zo%C3%AB%20%F0%9F%9A%80/shop.db",
		`C:\data\50% %41 #1;&=+,$'!~@.db`:  "file:///C:/data/50%25%20%2541%20%231;&=+,$%27%21~@.db",
		`\\server\share\dir\shop.db`:       "file:////server/share/dir/shop.db",
		`\\server\share with space\Zoë.db`: "file:////server/share%20with%20space/Zo%C3%AB.db",
		`\\localhost\C$\data\shop.db`:      "file:////localhost/C$/data/shop.db",
		`\\?\C:\data\shop.db`:              "file:///%5C%5C%3F%5CC:%5Cdata%5Cshop.db",
		`\\?\UNC\server\share\shop.db`:     "file:///%5C%5C%3F%5CUNC%5Cserver%5Cshare%5Cshop.db",
		`C:/already/forward/shop.db`:       "file:///C:/already/forward/shop.db",
		`\\.\C:\data\shop.db`:              "file:////./C:/data/shop.db",
	} {
		if got := build(p, "", true); got != want {
			t.Errorf("build(%q) = %q, want %q", p, got, want)
		}
		if got := build(p, query, true); got != want+"?"+query {
			t.Errorf("build(%q, query) = %q, want %q", p, got, want+"?"+query)
		}
	}
}
