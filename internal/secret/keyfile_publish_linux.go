package secret

import (
	"errors"
	"golang.org/x/sys/unix"
)

func unsupportedKeyFileLink(err error) bool {
	// Linux also reports EPERM when the filesystem does not support hard links.
	return errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM)
}

func renameKeyFileNoReplace(temp, path string) error {
	return unix.Renameat2(unix.AT_FDCWD, temp, unix.AT_FDCWD, path, unix.RENAME_NOREPLACE)
}
