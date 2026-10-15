package secret

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func unsupportedKeyFileLink(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_CALL_NOT_IMPLEMENTED)
}

// keyFileRenameInfo matches FILE_RENAME_INFO. The first DWORD is zero, so
// ReplaceIfExists is FALSE. RootDirectory is NULL for an absolute destination.
type keyFileRenameInfo struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func renameKeyFileNoReplace(temp, path string) error {
	absolute, err := keyFileWindowsPath(path)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16FromString(absolute)
	if err != nil {
		return err
	}
	sourcePath, err := keyFileWindowsPath(temp)
	if err != nil {
		return err
	}
	source, err := windows.UTF16PtrFromString(sourcePath)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(source, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	nameBytes := (len(destination) - 1) * 2
	var header keyFileRenameInfo
	offset := int(unsafe.Offsetof(header.FileName))
	buffer := make([]byte, offset+nameBytes)
	info := (*keyFileRenameInfo)(unsafe.Pointer(&buffer[0]))
	info.FileNameLength = uint32(nameBytes)
	copy(unsafe.Slice(&info.FileName[0], len(destination)-1), destination[:len(destination)-1])
	// Renaming the completed file preserves its security descriptor. Do not fall
	// back to MoveFileEx, an overwriting rename, or copying to the destination.
	return windows.SetFileInformationByHandle(handle, windows.FileRenameInfo, &buffer[0], uint32(len(buffer)))
}

// qh readers share DELETE access with the briefly open publication handle.
// Other Windows readers may still encounter their normal sharing errors.
func readKeyFile(path string) ([]byte, error) {
	nativePath, err := keyFileWindowsPath(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	name, err := windows.UTF16PtrFromString(nativePath)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return io.ReadAll(file)
}

// Keep os.ReadFile's extended-path compatibility when using CreateFile directly,
// including systems where unprefixed long paths are unavailable. Prefixing only
// long paths preserves ordinary Windows normalization for short file names.
func keyFileWindowsPath(path string) (string, error) {
	path = strings.ReplaceAll(path, "/", `\`)
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) || strings.HasPrefix(path, `\\.\`) {
		return path, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if len(absolute) < 248 {
		return absolute, nil
	}
	if strings.HasPrefix(absolute, `\\`) {
		return `\\?\UNC\` + absolute[2:], nil
	}
	return `\\?\` + absolute, nil
}
