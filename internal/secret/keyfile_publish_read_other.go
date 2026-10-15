//go:build !windows

package secret

import "os"

func readKeyFile(path string) ([]byte, error) { return os.ReadFile(path) }
