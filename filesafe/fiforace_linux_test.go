//go:build linux && (amd64 || arm64)

package filesafe_test

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/cplieger/go/filesafe"
)

// exchange swaps a and b in dir atomically with renameat2(RENAME_EXCHANGE)
// and returns its errno, 0 on success.
func exchange(dir, a, b string) syscall.Errno {
	pa, err := syscall.BytePtrFromString(filepath.Join(dir, a))
	if err != nil {
		return syscall.EINVAL
	}
	pb, err := syscall.BytePtrFromString(filepath.Join(dir, b))
	if err != nil {
		return syscall.EINVAL
	}
	const renameExchange = 2
	cwd := int64(-100) // AT_FDCWD
	_, _, errno := syscall.Syscall6(sysRenameat2, uintptr(cwd), uintptr(unsafe.Pointer(pa)),
		uintptr(cwd), uintptr(unsafe.Pointer(pb)), renameExchange, 0)
	return errno
}

// TestOpenParentInRoot_FIFOSwapRaceNeverBlocks races a directory and a FIFO
// swapping places against OpenParentInRoot. Every open must return, refused or
// not: the component is opened as an intermediate path element, which os.Root
// opens with O_DIRECTORY, so a FIFO that wins the race is refused at
// once. A component opened through os.Root with a trailing separator instead
// is lstat-ed and then opened without O_DIRECTORY, and a FIFO swapped in
// between blocks the open for good.
func TestOpenParentInRoot_FIFOSwapRaceNeverBlocks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "p"), 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// One exchange up front, and back: a filesystem or sandbox without
	// RENAME_EXCHANGE would otherwise swap nothing and pass without racing.
	if errno := exchange(dir, "d", "p"); errno == syscall.ENOSYS || errno == syscall.EINVAL {
		t.Skipf("renameat2 RENAME_EXCHANGE unsupported here: %v", errno)
	} else if errno != 0 {
		t.Fatalf("renameat2 RENAME_EXCHANGE: %v", errno)
	}
	if errno := exchange(dir, "d", "p"); errno != 0 {
		t.Fatalf("renameat2 RENAME_EXCHANGE back: %v", errno)
	}
	stop := make(chan struct{})
	swapped := make(chan struct{})
	go func() {
		defer close(swapped)
		for {
			select {
			case <-stop:
				return
			default:
				_ = exchange(dir, "d", "p")
			}
		}
	}()
	// The swapper stops before a hang is released, so the FIFO stays at one
	// name while the release opens both.
	stopSwap := sync.OnceFunc(func() { close(stop); <-swapped })
	defer stopSwap()

	for i := range 20000 {
		done := make(chan struct{})
		go func() {
			defer close(done)
			if r, _, err := filesafe.OpenParentInRoot(root, "d/f"); err == nil {
				_ = r.Close()
			}
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			// Release the blocked open with a writer so the test can end.
			stopSwap()
			for _, n := range []string{"d", "p"} {
				if f, oErr := os.OpenFile(filepath.Join(dir, n), os.O_WRONLY|syscall.O_NONBLOCK, 0); oErr == nil {
					_ = f.Close()
				}
			}
			<-done
			t.Fatalf("OpenParentInRoot blocked on try %d on a FIFO swapped in for its component", i)
		}
	}
}
