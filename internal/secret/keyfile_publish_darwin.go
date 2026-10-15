package secret

import (
	"errors"
	"golang.org/x/sys/unix"
)

func unsupportedKeyFileLink(err error) bool {
	return errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS)
}

func renameKeyFileNoReplace(temp, path string) error {
	return unix.RenamexNp(temp, path, unix.RENAME_EXCL)
}
