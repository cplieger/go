//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package wiregen_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/go/wiregen/v2"
)

// Opening a FIFO for reading blocks until a writer appears (fifo(7)), so a
// FIFO at the edges file is refused without being opened.
func TestDeadsetEdges_RefusesAFIFO(t *testing.T) {
	root, pkg := edgesProject(t, `{"name": "app"}`)
	fifo := filepath.Join(root, "deadset-edges.json")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	before := treeOf(t, root)
	errc := make(chan error, 1)
	go func() {
		errc <- wireRegistry(pkg, "User").Generate(t.Context(),
			wiregen.WithTSDir(filepath.Join(root, "web", "src", "wire")), wiregen.WithDeadsetEdges(root))
	}()
	var err error
	select {
	case err = <-errc:
	case <-time.After(30 * time.Second):
		// A writer unblocks the open, so the goroutine ends with the test.
		if w, openErr := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); openErr == nil {
			w.Close()
		}
		<-errc
		t.Fatal("Generate blocked opening the FIFO")
	}
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Errorf("Generate = %v, want the FIFO refused as not a regular file", err)
	}
	if after := treeOf(t, root); !slices.Equal(after, before) {
		t.Errorf("the tree changed:\n%v\nwant:\n%v", after, before)
	}
}
