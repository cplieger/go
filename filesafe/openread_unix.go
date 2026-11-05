//go:build !windows

package filesafe

import "os"

// openRead opens clean read-only, following symlinks.
func openRead(clean string) (*os.File, error) {
	return os.Open(clean)
}
