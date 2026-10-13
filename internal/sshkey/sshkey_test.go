package sshkey

import (
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/tunnel/sshtest"
)

func TestCheckSize(t *testing.T) {
	key, _ := sshtest.Key(t, "")
	if err := Check(key, 0); err != nil {
		t.Fatalf("real key: %v", err)
	}
	big := key + strings.Repeat("x", MaxSize)
	for _, rounds := range []int{0, ServerMaxKDFRounds} {
		err := Check(big, rounds)
		if err == nil || !strings.Contains(err.Error(), "larger than 16 KiB") {
			t.Fatalf("rounds cap %d: oversize key: %v", rounds, err)
		}
		if strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), "xxxx") {
			t.Fatalf("key material in error: %v", err)
		}
	}
	if err := Check(strings.Repeat("x", MaxSize), 0); err != nil {
		t.Fatalf("exactly 16 KiB: %v", err)
	}
}

func TestCheckRounds(t *testing.T) {
	huge := sshtest.KeyWithRounds(1_000_000)
	start := time.Now()
	err := Check(huge, ServerMaxKDFRounds)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("took %v", d)
	}
	want := "the private key's KDF uses 1000000 rounds; this server accepts at most 128 (re-encrypt it with ssh-keygen -p -a 100)"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	if err := Check(huge, 0); err != nil {
		t.Fatalf("no cap: %v", err)
	}
	for _, r := range []uint32{1, 16, 100, 128} {
		if err := Check(sshtest.KeyWithRounds(r), ServerMaxKDFRounds); err != nil {
			t.Fatalf("%d rounds: %v", r, err)
		}
	}
	if err := Check(sshtest.KeyWithRounds(129), ServerMaxKDFRounds); err == nil {
		t.Fatal("129 rounds accepted")
	}
	// Real keys: unencrypted (kdf none) and encrypted by x/crypto (16 rounds).
	for _, pp := range []string{"", "pp"} {
		key, _ := sshtest.Key(t, pp)
		if err := Check(key, ServerMaxKDFRounds); err != nil {
			t.Fatalf("real key (passphrase %q): %v", pp, err)
		}
	}
}

// Keys in other PEM formats, and text that is not PEM at all, are left to
// x/crypto's parser.
func TestCheckIgnoresOtherFormats(t *testing.T) {
	for _, s := range []string{"", "not a key", "-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n"} {
		if err := Check(s, ServerMaxKDFRounds); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func str(b string) string {
	return string(binary.BigEndian.AppendUint32(nil, uint32(len(b)))) + b
}

func u32(n uint32) string { return string(binary.BigEndian.AppendUint32(nil, n)) }

func openssh(body string) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: []byte(body)}))
}

// A malformed header is an error, never a panic.
func TestCheckMalformedHeader(t *testing.T) {
	const magic = "openssh-key-v1\x00"
	opts := str("0123456789abcdef") + u32(16)
	good := magic + str("aes256-ctr") + str("bcrypt") + str(opts)
	if err := Check(openssh(good), ServerMaxKDFRounds); err != nil {
		t.Fatalf("good header: %v", err)
	}
	bad := map[string]string{
		"empty":               "",
		"wrong magic":         "openssh-key-v2\x00" + good[len(magic):],
		"magic only":          magic,
		"huge cipher length":  magic + u32(0xFFFFFFFF) + "aes",
		"cipher past the end": magic + u32(100) + "aes256-ctr",
		"no kdfname":          magic + str("aes256-ctr"),
		"huge kdf length":     magic + str("aes256-ctr") + u32(0x80000000),
		"no kdfoptions":       magic + str("aes256-ctr") + str("bcrypt"),
		"huge kdfoptions":     magic + str("aes256-ctr") + str("bcrypt") + u32(0xFFFFFFF0) + opts,
		"empty kdfoptions":    magic + str("aes256-ctr") + str("bcrypt") + str(""),
		"huge salt":           magic + str("aes256-ctr") + str("bcrypt") + str(u32(0xFFFFFFFF)+"salt"+u32(16)),
		"no rounds":           magic + str("aes256-ctr") + str("bcrypt") + str(str("0123456789abcdef")),
		"short rounds":        magic + str("aes256-ctr") + str("bcrypt") + str(str("0123456789abcdef")+"\x00\x10"),
	}
	// Every truncation of a good header is malformed too.
	for i := range len(good) {
		bad[fmt.Sprintf("truncated at %d", i)] = good[:i]
	}
	for name, body := range bad {
		err := Check(openssh(body), ServerMaxKDFRounds)
		if err == nil || err.Error() != "the private key is not a valid OpenSSH private key" {
			t.Errorf("%q: %v", name, err)
		}
	}
	// A kdf other than bcrypt carries no round count to check.
	if err := Check(openssh(magic+str("none")+str("none")+str("")), ServerMaxKDFRounds); err != nil {
		t.Errorf("kdf none: %v", err)
	}
}

func FuzzCheck(f *testing.F) {
	f.Add([]byte("openssh-key-v1\x00" + str("aes256-ctr") + str("bcrypt") + str(str("salt")+u32(16))))
	f.Add([]byte("openssh-key-v1\x00" + u32(0xFFFFFFFF)))
	f.Fuzz(func(t *testing.T, body []byte) {
		Check(openssh(string(body)), ServerMaxKDFRounds)
	})
}

// Signing cost grows steeply with an RSA modulus, and the PKCS#1 and PKCS#8
// parsers do not cap it (x/crypto's OpenSSH parser stops at 8192 bits).
func TestCheckParsed(t *testing.T) {
	rsaBits := func(bits uint) *rsa.PrivateKey {
		n := new(big.Int).Lsh(big.NewInt(1), bits-1)
		return &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: n, E: 65537}}
	}
	if err := CheckParsed(rsaBits(8192), ServerMaxRSABits); err != nil {
		t.Fatalf("8192 bits: %v", err)
	}
	want := "the private key is an 8193-bit RSA key; this server accepts at most 8192 bits"
	if err := CheckParsed(rsaBits(8193), ServerMaxRSABits); err == nil || err.Error() != want {
		t.Fatalf("8193 bits: %v", err)
	}
	if err := CheckParsed(rsaBits(16384), 0); err != nil {
		t.Fatalf("no cap: %v", err)
	}
	_, ed, _ := ed25519.GenerateKey(nil)
	for _, k := range []any{ed, &ed, nil} {
		if err := CheckParsed(k, ServerMaxRSABits); err != nil {
			t.Fatalf("%T: %v", k, err)
		}
	}
}
