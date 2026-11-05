package filesafe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

// MkfifoForTest reports errors.ErrUnsupported: Windows filesystems hold no
// FIFOs (named pipes live in the \\.\pipe\ namespace), so a FIFO case skips.
func MkfifoForTest(string) error {
	return fmt.Errorf("mkfifo on Windows: %w", errors.ErrUnsupported)
}

// errPrivilegeNotHeld is ERROR_PRIVILEGE_NOT_HELD, which CreateSymbolicLink
// returns to a process without SeCreateSymbolicLinkPrivilege outside
// Developer Mode (https://learn.microsoft.com/windows/win32/api/winbase/nf-winbase-createsymboliclinkw).
const errPrivilegeNotHeld syscall.Errno = 1314

// fileSupportsPOSIXUnlinkRename is FILE_SUPPORTS_POSIX_UNLINK_RENAME, the
// volume flag for POSIX rename and delete semantics ([MS-FSCC] 2.5.1).
const fileSupportsPOSIXUnlinkRename = 0x400

// symlinkOrSkip creates newname pointing at oldname, skipping t when the
// process may not create symlinks.
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	err := os.Symlink(oldname, newname)
	if errors.Is(err, errPrivilegeNotHeld) {
		t.Skipf("symlink creation needs SeCreateSymbolicLinkPrivilege or Developer Mode: %v", err)
	}
	if err != nil {
		t.Fatalf("Symlink(%q, %q) = %v", oldname, newname, err)
	}
}

// volumeInfo returns the filesystem name and flags of the volume holding dir,
// from GetVolumeInformationByHandleW
// (https://learn.microsoft.com/windows/win32/api/fileapi/nf-fileapi-getvolumeinformationbyhandlew).
func volumeInfo(t *testing.T, dir string) (fsName string, flags uint32) {
	t.Helper()
	d, err := os.Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) = %v", dir, err)
	}
	defer d.Close()
	var buf [syscall.MAX_PATH + 1]uint16
	ok, _, callErr := procGetVolumeInformationByHandleW.Call(d.Fd(), 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&flags)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ok == 0 {
		t.Fatalf("GetVolumeInformationByHandleW(%q) = %v", dir, callErr)
	}
	return syscall.UTF16ToString(buf[:]), flags
}

// renameSpy records the error of every replace attempt the write path makes;
// after, when set, runs after each attempt with its 1-based number.
type renameSpy struct {
	after func(attempt int, err error)
	errs  []error
}

// spyRenames swaps rootRename for a recording wrapper until t ends, so a test
// using it must not run in parallel.
func spyRenames(t *testing.T) *renameSpy {
	t.Helper()
	s := &renameSpy{}
	orig := rootRename
	rootRename = func(r *os.Root, oldname, newname string) error {
		err := orig(r, oldname, newname)
		s.errs = append(s.errs, err)
		if s.after != nil {
			s.after(len(s.errs), err)
		}
		return err
	}
	t.Cleanup(func() { rootRename = orig })
	return s
}

// holdWithoutShareDelete opens path the way os.Open does on Windows, sharing
// read and write but not delete, and closes it when t ends unless the caller
// closed it first.
func holdWithoutShareDelete(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open(%q) = %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// readHandle reads f from offset 0 to its end.
func readHandle(t *testing.T, f *os.File) string {
	t.Helper()
	b, err := io.ReadAll(io.NewSectionReader(f, 0, 1<<20))
	if err != nil {
		t.Fatalf("read %q through its open handle = %v", f.Name(), err)
	}
	return string(b)
}

// writeForm is one atomic-write entry point, writing data to name inside dir;
// root is dir opened as an *os.Root.
type writeForm struct {
	write func(ctx context.Context, root *os.Root, dir, name string, data []byte, opts ...Option) (Result, error)
	name  string
}

// writeForms returns the six atomic-write entry points.
func writeForms() []writeForm {
	return []writeForm{
		{name: "WriteFile", write: func(ctx context.Context, _ *os.Root, dir, name string, data []byte, opts ...Option) (Result, error) {
			return WriteFile(ctx, filepath.Join(dir, name), data, opts...)
		}},
		{name: "WriteReader", write: func(ctx context.Context, _ *os.Root, dir, name string, data []byte, opts ...Option) (Result, error) {
			return WriteReader(ctx, filepath.Join(dir, name), bytes.NewReader(data), opts...)
		}},
		{name: "PendingFile", write: func(ctx context.Context, _ *os.Root, dir, name string, data []byte, opts ...Option) (Result, error) {
			pf, err := NewPendingFile(ctx, filepath.Join(dir, name), opts...)
			if err != nil {
				return Result{}, err
			}
			return commitPending(ctx, pf, data)
		}},
		{name: "WriteFileInRoot", write: func(ctx context.Context, root *os.Root, _, name string, data []byte, opts ...Option) (Result, error) {
			return WriteFileInRoot(ctx, root, name, data, opts...)
		}},
		{name: "WriteReaderInRoot", write: func(ctx context.Context, root *os.Root, _, name string, data []byte, opts ...Option) (Result, error) {
			return WriteReaderInRoot(ctx, root, name, bytes.NewReader(data), opts...)
		}},
		{name: "PendingFileInRoot", write: func(ctx context.Context, root *os.Root, _, name string, data []byte, opts ...Option) (Result, error) {
			pf, err := NewPendingFileInRoot(ctx, root, name, opts...)
			if err != nil {
				return Result{}, err
			}
			return commitPending(ctx, pf, data)
		}},
	}
}

// commitPending writes data to pf and commits it, cleaning up on a failed write.
func commitPending(ctx context.Context, pf *PendingFile, data []byte) (Result, error) {
	if _, err := pf.Write(data); err != nil {
		_ = pf.Cleanup()
		return Result{}, err
	}
	return pf.Commit(ctx)
}
