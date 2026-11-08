//go:build linux || (darwin && !ios)

package measure

import (
	"syscall"
	"testing"
)

// fstatOpenFDs counts the descriptors below 4096 that fstat accepts, the
// ground truth readOpenFDs must match; a test binary holds far fewer.
func fstatOpenFDs() int {
	var st syscall.Stat_t
	n := 0
	for fd := range 4096 {
		if syscall.Fstat(fd, &st) == nil {
			n++
		}
	}
	return n
}

// TestReadOpenFDs_matchesFstat checks that the count leaves out exactly the
// handles the listing holds (fdListingHandles): it equals the descriptors open
// before and after the listing.
func TestReadOpenFDs_matchesFstat(t *testing.T) {
	for range 3 {
		before := fstatOpenFDs()
		got := readOpenFDs(fdDir, fdListingHandles)
		if after := fstatOpenFDs(); before != after {
			continue // a descriptor opened or closed meanwhile; measure again
		}
		if got != before {
			t.Fatalf("readOpenFDs(%s, %d) = %d, want %d (fstat ground truth)", fdDir, fdListingHandles, got, before)
		}
		return
	}
	t.Skip("the descriptor set kept changing")
}
