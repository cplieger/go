package filesafe

import "fmt"

// ensurePrivateDir refuses: ownership by the effective uid and a 0700 mode
// have no Windows equivalent (access is an ACL), so no verdict could keep
// EnsurePrivateDir's guarantee.
func ensurePrivateDir(clean string, _ *cfg) (PrivateDir, error) {
	return PrivateDir{}, fmt.Errorf("%w: EnsurePrivateDir %s", ErrModeUnsupported, clean)
}
