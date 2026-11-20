//go:build !windows

package filesafe

import (
	"errors"
	"io/fs"
	"syscall"
	"testing"
)

// TestNotDirectoryErr_ASocketOpenIsENOTDIR pins openDirInRoot's ENOTDIR
// contract where the kernel tests the socket type before O_DIRECTORY and
// answers EOPNOTSUPP (OpenBSD, NetBSD and DragonFly vfs_vnops.c; Go's
// os.rootOpenDir translates the same errno), while every other error keeps
// its errno and the PathError keeps its op and path.
func TestNotDirectoryErr_ASocketOpenIsENOTDIR(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		in, want syscall.Errno
	}{
		"EOPNOTSUPP": {syscall.EOPNOTSUPP, syscall.ENOTDIR},
		"ENOTDIR":    {syscall.ENOTDIR, syscall.ENOTDIR},
		"EACCES":     {syscall.EACCES, syscall.EACCES},
		"ENOENT":     {syscall.ENOENT, syscall.ENOENT},
	} {
		got := notDirectoryErr(&fs.PathError{Op: "openat", Path: "d", Err: tc.in})
		pe, ok := errors.AsType[*fs.PathError](got)
		if !ok || pe.Op != "openat" || pe.Path != "d" || !errors.Is(got, tc.want) {
			t.Errorf("%s: notDirectoryErr = %v, want openat d: %v", name, got, tc.want)
		}
	}
}
