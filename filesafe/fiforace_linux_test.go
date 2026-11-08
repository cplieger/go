//go:build linux && (amd64 || arm64)

package filesafe_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/cplieger/go/filesafe"
)

// exchange swaps a and b in dir atomically with renameat2(RENAME_EXCHANGE).
func exchange(t *testing.T, dir, a, b string) {
	pa, err := syscall.BytePtrFromString(filepath.Join(dir, a))
	if err != nil {
		t.Error(err)
		return
	}
	pb, err := syscall.BytePtrFromString(filepath.Join(dir, b))
	if err != nil {
		t.Error(err)
		return
	}
	const renameExchange = 2
	cwd := int64(-100) // AT_FDCWD
	_, _, _ = syscall.Syscall6(sysRenameat2, uintptr(cwd), uintptr(unsafe.Pointer(pa)),
		uintptr(cwd), uintptr(unsafe.Pointer(pb)), renameExchange, 0)
}

// TestOpenParentInRoot_FIFOSwapRaceNeverBlocks races a directory and a FIFO
// swapping places against OpenParentInRoot. Every open must return, refused or
// not: the component is opened as an intermediate path element, which the
// kernel opens with O_DIRECTORY, so a FIFO that wins the race is refused at
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
	stop := make(chan struct{})
	swapped := make(chan struct{})
	go func() {
		defer close(swapped)
		for {
			select {
			case <-stop:
				return
			default:
				exchange(t, dir, "d", "p")
			}
		}
	}()
	defer func() { close(stop); <-swapped }()

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
