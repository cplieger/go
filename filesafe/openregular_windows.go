package filesafe

import (
	"fmt"
	"os"
	"syscall"
)

// openNoFollow opens clean read-only, refusing a final-component symlink with
// ErrSymlinkTarget. FILE_FLAG_OPEN_REPARSE_POINT opens the link itself, so
// the handle's own mode decides; a junction reads as ModeIrregular for the
// caller's regular-file check (https://learn.microsoft.com/windows/win32/api/fileapi/nf-fileapi-createfilew).
func openNoFollow(clean string) (*os.File, error) {
	f, err := openReadShared(clean, syscall.FILE_FLAG_OPEN_REPARSE_POINT)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s", ErrSymlinkTarget, clean)
	}
	return f, nil
}
