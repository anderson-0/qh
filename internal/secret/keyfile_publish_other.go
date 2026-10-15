//go:build !linux && !darwin && !windows

package secret

import "errors"

// No verified exclusive rename is available on this platform. Keep the original
// hard-link error rather than attempting a replacement or a partial copy.
func unsupportedKeyFileLink(error) bool { return false }

func renameKeyFileNoReplace(string, string) error {
	return errors.New("secret: exclusive key file rename is unsupported on this platform")
}
