package secret

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func unsupportedKeyFileLinkError() error { return windows.ERROR_NOT_SUPPORTED }

func TestKeyFilePlatformLinkErrors(t *testing.T) {
	testKeyFileLinkErrors(t, []error{windows.ERROR_NOT_SUPPORTED, windows.ERROR_INVALID_FUNCTION, windows.ERROR_CALL_NOT_IMPLEMENTED}, []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION, windows.ERROR_PRIVILEGE_NOT_HELD, windows.ERROR_DISK_FULL, windows.ERROR_NOT_SAME_DEVICE})
}

// Copying to a new file can inherit different permissions; rename must retain
// the completed source's actual protected DACL, owner, and group.
func TestKeyFileFallbackPreservesSecurityDescriptor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	_, err := loadOrCreateKeyFile(path, func(temp, path string) error {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return err
		}
		sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
		if err != nil {
			return err
		}
		acl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		if err := windows.SetNamedSecurityInfo(temp, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			return err
		}
		flags := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION)
		before, err := windows.GetNamedSecurityInfo(temp, windows.SE_FILE_OBJECT, flags)
		if err != nil {
			return err
		}
		if err := forceKeyFileFallback(temp, path); err != nil {
			return err
		}
		after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags)
		if err != nil {
			return err
		}
		if before.String() != after.String() {
			t.Fatal("publication changed the security descriptor")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Deleting after publication also detects an accidentally leaked source handle.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// A completed key remains readable while the publisher still owns its DELETE
// handle. Ordinary os.ReadFile does not share DELETE access on Windows.
func TestKeyFileLoadWhileDeleteHandleOpen(t *testing.T) {
	for _, racing := range []bool{false, true} {
		t.Run(fmt.Sprintf("racing=%v", racing), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "master.key")
			var held windows.Handle = windows.InvalidHandle
			defer func() {
				if held != windows.InvalidHandle {
					windows.CloseHandle(held)
				}
			}()
			install := func() error {
				if err := os.WriteFile(path, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"), 0600); err != nil {
					return err
				}
				name, err := windows.UTF16PtrFromString(path)
				if err != nil {
					return err
				}
				held, err = windows.CreateFile(name, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
				return err
			}
			var key []byte
			var err error
			if racing {
				key, err = loadOrCreateKeyFile(path, func(temp, path string) error {
					if err := install(); err != nil {
						return err
					}
					return forceKeyFileFallback(temp, path)
				})
			} else {
				if err := install(); err != nil {
					t.Fatal(err)
				}
				key, err = LoadOrCreateKeyFile(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(key, make([]byte, 32)) {
				t.Fatal("did not load winner with DELETE handle open")
			}
		})
	}
}

// Raw CreateFile otherwise loses os.ReadFile's long-path compatibility on
// Windows systems where the process cannot use unprefixed long paths.
func TestKeyFileWindowsPath(t *testing.T) {
	long := strings.Repeat("segment\\", 40) + "master.key"
	for _, tc := range []struct{ name, input, want string }{
		{"short", `C:\keys\master.key`, `C:\keys\master.key`},
		{"drive", `C:\` + long, `\\?\C:\` + long},
		{"unc", `\\server\share\` + long, `\\?\UNC\server\share\` + long},
		{"extended", `\\?\C:\` + long, `\\?\C:\` + long},
		{"nt", `\??\C:\` + long, `\??\C:\` + long},
		{"device", `\\.\C:\` + long, `\\.\C:\` + long},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := keyFileWindowsPath(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("converted path=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestKeyFileWindowsLongPath(t *testing.T) {
	root := t.TempDir()
	relative := filepath.Join(strings.Repeat("a", 80), strings.Repeat("b", 80), strings.Repeat("c", 80), strings.Repeat("d", 80))
	if err := os.MkdirAll(filepath.Join(root, relative), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, path := range []string{filepath.Join(root, relative, "absolute.key"), filepath.Join(relative, "relative.key")} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			key, err := loadOrCreateKeyFile(path, forceKeyFileFallback)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadOrCreateKeyFile(path)
			if err != nil || !bytes.Equal(key, loaded) {
				t.Fatalf("long-path reload failed: %v", err)
			}
		})
	}
}
