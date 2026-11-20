//go:build !windows && !(darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd)

package filesafe

import (
	"errors"
	"fmt"
)

// takeLease fails: package syscall has no Flock on this platform.
func takeLease(string, bool) (*Lease, error) {
	return nil, fmt.Errorf("filesafe: lease: %w", errors.ErrUnsupported)
}
