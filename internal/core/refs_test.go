package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
)

const fp = "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"

func TestRefsAndSSHAreStoredAndShown(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := ConnInput{Name: "pg", Engine: "postgres", DSNRef: "env:QH_PG_DSN", PasswordRef: "file:/run/secrets/pw",
		SSH: &engine.SSH{Host: "bastion", User: "u", HostKey: fp, Password: "ssh-pw"}}
	info, _, err := h.svc.CreateConnection(ctx, ws, in, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.DSNRef != "env:QH_PG_DSN" || info.PasswordRef != "file:/run/secrets/pw" || info.SSH == nil ||
		info.SSH.Host != "bastion" || info.SSH.Port != 22 || info.SSH.Auth != "password" || info.DSN != "(from env:QH_PG_DSN)" {
		t.Fatalf("%+v %+v", info, info.SSH)
	}
	got, err := h.svc.GetConnection(ctx, ws, "pg")
	if err != nil || got.DSNRef != "env:QH_PG_DSN" || got.SSH == nil || got.SSH.HostKey != fp {
		t.Fatalf("%+v %v", got, err)
	}
	// The SSH password is sealed, never in the summary.
	rec, _ := h.st.GetConnection(ctx, ws, "pg")
	if strings.Contains(rec.SSH, "ssh-pw") || strings.Contains(rec.Refs, "ssh-pw") {
		t.Fatal("ssh password stored in plain columns")
	}
	in2, err := h.svc.load(rec)
	if err != nil || in2.SSH == nil || in2.SSH.Password != "ssh-pw" || in2.DSNRef != "env:QH_PG_DSN" {
		t.Fatalf("load: %+v %v", in2, err)
	}
}

func TestSwitchingBetweenDSNAndRef(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, _, err := h.svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u@db/app"}, false); err != nil {
		t.Fatal(err)
	}
	ref := "env:QH_PG_DSN"
	info, _, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{DSNRef: &ref}, false)
	if err != nil || info.DSNRef != ref || info.DSN != "(from env:QH_PG_DSN)" {
		t.Fatalf("to ref: %+v %v", info, err)
	}
	dsn := "postgres://u@db2/app"
	info, _, err = h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{DSN: &dsn}, false)
	if err != nil || info.DSNRef != "" || info.Host != "db2" {
		t.Fatalf("back to dsn: %+v %v", info, err)
	}
	if _, _, err := h.svc.UpdateConnection(ctx, ws, "pg", ConnPatch{DSN: &dsn, DSNRef: &ref}, false); err == nil {
		t.Fatal("dsn and dsn_ref together accepted")
	}
}

func TestRefAndSSHValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for want, in := range map[string]ConnInput{
		"give dsn_ref or dsn":       {Name: "a", Engine: "postgres", DSN: "postgres://x@y/z", DSNRef: "env:X"},
		"invalid secret reference":  {Name: "a", Engine: "postgres", DSNRef: "env:"},
		"password_ref applies only": {Name: "a", Engine: "sqlite", DSN: "/tmp/x.db", PasswordRef: "env:X"},
		"ssh applies only":          {Name: "a", Engine: "sqlite", DSN: "/tmp/x.db", SSH: &engine.SSH{Host: "b", User: "u", HostKey: fp, Password: "p"}},
		"ssh.user is required":      {Name: "a", Engine: "postgres", DSN: "postgres://x@y/z", SSH: &engine.SSH{Host: "b", HostKey: fp, Password: "p"}},
		"ssh.host_key is required":  {Name: "a", Engine: "postgres", DSN: "postgres://x@y/z", SSH: &engine.SSH{Host: "b", User: "u", Password: "p"}},
	} {
		_, _, err := h.svc.CreateConnection(ctx, ws, in, false)
		if qerr.From(err).Category != qerr.Invalid || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", want, err)
		}
	}
}

func TestServerModeAllowList(t *testing.T) {
	st := openStore(t)
	svc := newServiceWith(t, st, Options{Dial: dialNowhere, SecretRefs: []string{"env:QH_TENANT_*"}, DenySSH: true})
	ctx := context.Background()
	_, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "a", Engine: "postgres", DSNRef: "env:HOME"}, false)
	if qerr.From(err).Category != qerr.Forbidden || !strings.Contains(err.Error(), "env:HOME is not allowed on this server") {
		t.Fatalf("env:HOME: %v", err)
	}
	if _, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "b", Engine: "postgres", DSNRef: "env:QH_TENANT_DB"}, false); err != nil {
		t.Fatalf("allowed ref: %v", err)
	}
	_, _, err = svc.CreateConnection(ctx, ws, ConnInput{Name: "c", Engine: "postgres", DSN: "postgres://x@10.0.0.1/z",
		SSH: &engine.SSH{Host: "b", User: "u", HostKey: fp, Password: "p"}}, false)
	if qerr.From(err).Category != qerr.Forbidden || !strings.Contains(err.Error(), "SSH tunnels are disabled on this server") {
		t.Fatalf("deny ssh: %v", err)
	}
	_, _, err = svc.CreateConnection(ctx, ws, ConnInput{Name: "d", Engine: "postgres", DSN: "postgres://x@10.0.0.1/z",
		SSH: &engine.SSH{Host: "b", User: "u", HostKey: fp, PasswordRef: "env:HOME"}}, false)
	if qerr.From(err).Category != qerr.Forbidden {
		t.Fatalf("ssh ref checked before deny-ssh or not at all: %v", err)
	}
}

// Update and the connection test are refusal points too (spec 5.2): a
// reference outside the allow-list, or a tunnel under --deny-ssh, is refused
// there as well as on create.
func TestServerModeRefusalsOnUpdateAndTest(t *testing.T) {
	svc := newServiceWith(t, openStore(t), Options{Dial: dialNowhere, CheckTarget: func(netip.Addr) bool { return true },
		SecretRefs: []string{"env:QH_TENANT_*"}, DenySSH: true})
	ctx := context.Background()
	if _, _, err := svc.CreateConnection(ctx, ws, ConnInput{Name: "pg", Engine: "postgres", DSN: "postgres://u@10.0.0.1/app"}, false); err != nil {
		t.Fatal(err)
	}
	notAllowed := "env:HOME"
	_, _, err := svc.UpdateConnection(ctx, ws, "pg", ConnPatch{DSNRef: &notAllowed}, false)
	if qerr.From(err).Category != qerr.Forbidden || !strings.Contains(err.Error(), "env:HOME is not allowed on this server") {
		t.Errorf("update with an unlisted dsn_ref: %v", err)
	}
	_, _, err = svc.UpdateConnection(ctx, ws, "pg", ConnPatch{PasswordRef: &notAllowed}, false)
	if qerr.From(err).Category != qerr.Forbidden {
		t.Errorf("update with an unlisted password_ref: %v", err)
	}
	_, err = svc.TestInput(ctx, ws, ConnInput{Engine: "postgres", DSNRef: "env:HOME"})
	if qerr.From(err).Category != qerr.Forbidden || !strings.Contains(err.Error(), "env:HOME is not allowed on this server") {
		t.Errorf("test with an unlisted dsn_ref: %v", err)
	}
	tunnel := &engine.SSH{Host: "b", User: "u", HostKey: fp, Password: "p"}
	_, _, err = svc.UpdateConnection(ctx, ws, "pg", ConnPatch{SSH: tunnel}, false)
	if qerr.From(err).Category != qerr.Forbidden || !strings.Contains(err.Error(), "SSH tunnels are disabled on this server") {
		t.Errorf("update adding ssh under deny-ssh: %v", err)
	}
	_, err = svc.TestInput(ctx, ws, ConnInput{Engine: "postgres", DSN: "postgres://u@10.0.0.1/app", SSH: tunnel})
	if qerr.From(err).Category != qerr.Forbidden || !strings.Contains(err.Error(), "SSH tunnels are disabled on this server") {
		t.Errorf("test with ssh under deny-ssh: %v", err)
	}
	// The refused updates changed nothing.
	got, err := svc.GetConnection(ctx, ws, "pg")
	if err != nil || got.DSNRef != "" || got.SSH != nil {
		t.Errorf("a refused update was applied: %+v %v", got, err)
	}
}

func dialNowhere(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("no network in tests")
}

func TestDecodeSecretAcceptsPlan4Envelope(t *testing.T) {
	e, err := decodeSecret([]byte(`{"v":2,"dsn":"x","tls":null}`))
	if err != nil || e.DSN != "x" || e.TLS != nil || e.SSH != nil || e.DSNRef != "" || e.PasswordRef != "" {
		t.Fatalf("%+v %v", e, err)
	}
	e, err = decodeSecret([]byte("postgres://raw"))
	if err != nil || e.DSN != "postgres://raw" {
		t.Fatalf("raw: %+v %v", e, err)
	}
}
