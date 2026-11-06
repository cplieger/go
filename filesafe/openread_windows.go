package filesafe

import (
	"io/fs"
	"os"
	"strings"
	"syscall"
)

// openRead opens clean read-only, following symlinks, with the share mode of
// openReadShared.
func openRead(clean string) (*os.File, error) {
	return openReadShared(clean, 0)
}

// openReadShared opens an existing absolute path read-only with
// FILE_SHARE_DELETE, which os.Open omits: without it the reader's handle
// makes a filesafe write of the same file fail its replace for the whole
// retry bound. FILE_FLAG_BACKUP_SEMANTICS lets a directory open, as os.Open
// does, so the caller's regular-file check refuses it by mode.
func openReadShared(clean string, flags uint32) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(extendedLengthPath(clean))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: clean, Err: err}
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|flags, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: clean, Err: err}
	}
	return os.NewFile(uintptr(h), clean), nil
}

// extendedLengthPath adds the \\?\ prefix past 248 bytes (MAX_PATH less 12),
// the length os.Open also extends at, since a plain path that long fails in
// CreateFile (https://learn.microsoft.com/windows/win32/fileio/maximum-file-path-limitation).
func extendedLengthPath(clean string) string {
	if len(clean) < 248 || strings.HasPrefix(clean, `\\?\`) || strings.HasPrefix(clean, `\\.\`) {
		return clean
	}
	if strings.HasPrefix(clean, `\\`) {
		return `\\?\UNC\` + clean[2:]
	}
	return `\\?\` + clean
}
