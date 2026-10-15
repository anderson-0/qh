package secret

import (
	"golang.org/x/sys/unix"
	"testing"
)

func unsupportedKeyFileLinkError() error { return unix.ENOTSUP }

func TestKeyFilePlatformLinkErrors(t *testing.T) {
	testKeyFileLinkErrors(t, []error{unix.ENOTSUP, unix.EOPNOTSUPP, unix.ENOSYS}, []error{unix.EPERM, unix.EACCES, unix.EIO, unix.ENOSPC, unix.EXDEV})
}
