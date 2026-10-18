package redact_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anderson-0/qh/internal/redact"
)

// benchPEM returns a synthetic PEM-like value of about size bytes.
func benchPEM(size int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	b.WriteString("-----BEGIN TEST KEY-----\n")
	for i := 0; b.Len() < size-26; i++ {
		b.WriteByte(alphabet[(i*7+i/64)%len(alphabet)])
		if i%64 == 63 {
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n-----END TEST KEY-----")
	return b.String()
}

// benchMessage returns driver-like text of size bytes. With secret set, the
// secret appears at the start, the middle and the end.
func benchMessage(size int, secret string) string {
	const filler = "pq: connection to server at db-01.internal (10.0.0.7), port 5432 failed; retrying -- "
	msg := []byte(strings.Repeat(filler, size/len(filler)+1)[:size])
	if secret != "" {
		for _, at := range []int{0, size/2 - len(secret)/2, size - len(secret)} {
			copy(msg[at:], secret)
		}
	}
	return string(msg)
}

// BenchmarkText measures Text on short and long messages with one short
// secret, a mixed set of eight and a set holding a 64 KiB key. There is no
// threshold; the numbers are recorded for review.
func BenchmarkText(b *testing.B) {
	mixed := []string{"q7", "hunter2", "p@ss w/rd'\\", "pässwörd", "s3cr3t-token-0123456789abcdef",
		"AKIAEXAMPLEKEY000000", "Zm9vYmFyYmF6cXV4", "correct horse battery staple"}
	sets := []struct {
		name   string
		values []string
	}{
		{"one2", []string{"q7"}},
		{"mixed8", mixed},
		{"pem8", append(append([]string{}, mixed[:7]...), benchPEM(64<<10))},
	}
	sizes := []struct {
		name string
		n    int
	}{{"200B", 200}, {"4KiB", 4 << 10}, {"64KiB", 64 << 10}}
	for _, set := range sets {
		s := redact.New(set.values...)
		for _, size := range sizes {
			for _, match := range []bool{false, true} {
				secret := ""
				if match {
					secret = set.values[0]
				}
				msg := benchMessage(size.n, secret)
				if changed := s.Text(msg) != msg; changed != match {
					b.Fatalf("set=%s size=%s: redaction %v, want %v", set.name, size.name, changed, match)
				}
				b.Run(fmt.Sprintf("set=%s/size=%s/match=%v", set.name, size.name, match), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(msg)))
					for b.Loop() {
						s.Text(msg)
					}
				})
			}
		}
	}
}
