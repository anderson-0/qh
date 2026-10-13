// Package sshkey bounds the cost of parsing an SSH private key before
// x/crypto's parser sees it. In server mode the key comes from a tenant, and
// an encrypted OpenSSH key names its own bcrypt_pbkdf round count, so a
// crafted key could make every parse burn CPU.
package sshkey

import (
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// MaxSize is the largest PEM text accepted. A 16384-bit RSA key in OpenSSH
// format is about 13 KiB.
const MaxSize = 16 << 10

// ServerMaxKDFRounds is the bcrypt round cap in server mode. ssh-keygen
// writes 16 by default, and guides commonly recommend -a 100.
const ServerMaxKDFRounds = 128

const opensshMagic = "openssh-key-v1\x00"

var errMalformed = errors.New("the private key is not a valid OpenSSH private key")

// Check refuses a private key larger than MaxSize and, when maxRounds > 0,
// an OpenSSH-format key whose bcrypt KDF uses more than maxRounds rounds.
// Text that is not an OpenSSH private key is left to x/crypto. Errors never
// include key material.
func Check(pemText string, maxRounds int) error {
	if len(pemText) > MaxSize {
		return errors.New("the private key is larger than 16 KiB")
	}
	if maxRounds <= 0 {
		return nil
	}
	// x/crypto reads the first PEM block in the text; so does this.
	block, _ := pem.Decode([]byte(pemText))
	if block == nil || block.Type != "OPENSSH PRIVATE KEY" {
		return nil
	}
	kdf, rounds, err := kdfRounds(block.Bytes)
	if err != nil {
		return err
	}
	if kdf == "bcrypt" && uint64(rounds) > uint64(maxRounds) {
		return fmt.Errorf("the private key's KDF uses %d rounds; this server accepts at most %d (re-encrypt it with ssh-keygen -p -a 100)", rounds, maxRounds)
	}
	return nil
}

// kdfRounds decodes the header of an openssh-key-v1 key: the magic, then
// the strings ciphername, kdfname and kdfoptions, in SSH wire format. For
// bcrypt, kdfoptions holds the string salt and the uint32 rounds.
func kdfRounds(b []byte) (kdf string, rounds uint32, err error) {
	if !strings.HasPrefix(string(b), opensshMagic) {
		return "", 0, errMalformed
	}
	b = b[len(opensshMagic):]
	var name, opts []byte
	var ok bool
	if _, b, ok = readString(b); !ok { // ciphername
		return "", 0, errMalformed
	}
	if name, b, ok = readString(b); !ok {
		return "", 0, errMalformed
	}
	if opts, _, ok = readString(b); !ok {
		return "", 0, errMalformed
	}
	if string(name) != "bcrypt" {
		return string(name), 0, nil
	}
	if _, opts, ok = readString(opts); !ok { // salt
		return "", 0, errMalformed
	}
	if len(opts) < 4 {
		return "", 0, errMalformed
	}
	return "bcrypt", binary.BigEndian.Uint32(opts), nil
}

// readString reads a uint32 length and that many bytes.
func readString(b []byte) (s, rest []byte, ok bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(b)
	b = b[4:]
	if uint64(n) > uint64(len(b)) {
		return nil, nil, false
	}
	return b[:n], b[n:], true
}
