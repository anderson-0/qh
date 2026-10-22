// Package sqliteuri builds the file: URIs that qh hands to the SQLite driver.
//
// SQLite reads a URI as file://AUTHORITY/PATH?QUERY, where the authority must
// be empty or "localhost" (https://www.sqlite.org/uri.html). It decodes %HH
// escapes in the path, ends the path at the first ? and ignores everything
// after a #. A Unix path is used as it is. A Windows path needs a "/" in front
// and forward slashes: file:///C:/dir/x.db for a drive path and
// file:////server/share/x.db for a UNC path.
package sqliteuri

import (
	"net/url"
	"path/filepath"
	"strings"
)

// File returns the SQLite URI for the absolute path p with rawQuery, an
// encoded query without the leading ?. Characters that SQLite would read as
// syntax (%, ? and #) and every other unsafe byte are percent-encoded.
func File(p, rawQuery string) string {
	return build(p, rawQuery, filepath.Separator == '\\')
}

// build is File with the path rules chosen by windows, so the Windows rules
// can be tested on every OS.
func build(p, rawQuery string, windows bool) string {
	if windows {
		p = windowsPath(p)
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: rawQuery}
	return u.String()
}

// windowsPath turns an absolute Windows path into a URI path. The Windows VFS
// drops a "/" in front of a drive letter ("/C:/x") and in front of a \\?\
// prefix, then passes the name to GetFullPathNameW.
func windowsPath(p string) string {
	if strings.HasPrefix(p, `\\?\`) {
		// Windows does not normalize a \\?\ path, so it keeps its backslashes.
		return "/" + p
	}
	// C:\dir\x.db becomes /C:/dir/x.db, and \\server\share\x.db becomes
	// //server/share/x.db, which follows the empty authority.
	p = strings.ReplaceAll(p, `\`, "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
