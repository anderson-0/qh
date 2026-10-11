package cli

import (
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/anderson-0/qh/internal/core"
)

func TestServerOptions(t *testing.T) {
	root := t.TempDir()
	opts, err := serverOptions(core.Options{NoQueryLog: true}, nil, nil, []string{"env:QH_T_*", "file:/run/secrets/*"}, true, root)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Dial == nil || opts.CheckTarget == nil {
		t.Fatalf("Dial or CheckTarget unset: %+v", opts)
	}
	if opts.CheckTarget(netip.MustParseAddr("169.254.169.254")) {
		t.Error("CheckTarget allows the metadata address")
	}
	if !opts.CheckTarget(netip.MustParseAddr("8.8.8.8")) {
		t.Error("CheckTarget refuses a public address")
	}
	if len(opts.SecretRefs) != 2 || opts.SecretRefs[0] != "env:QH_T_*" || !opts.DenySSH || !opts.NoQueryLog {
		t.Errorf("options not carried: %+v", opts)
	}
	if opts.SQLiteDisabled || opts.SQLiteRoot != filepath.Clean(root) {
		t.Errorf("sqlite root: %+v", opts)
	}

	opts, err = serverOptions(core.Options{}, []string{"10.0.0.0/8"}, nil, nil, false, "")
	if err != nil || !opts.SQLiteDisabled || !opts.CheckTarget(netip.MustParseAddr("10.1.2.3")) {
		t.Errorf("allow-cidr / sqlite disabled: %v %+v", err, opts)
	}

	if _, err = serverOptions(core.Options{}, nil, nil, []string{"QH_*"}, false, ""); err == nil {
		t.Error("invalid pattern accepted")
	}
	if _, err = serverOptions(core.Options{}, []string{"not-a-cidr"}, nil, nil, false, ""); err == nil {
		t.Error("invalid CIDR accepted")
	}
}
