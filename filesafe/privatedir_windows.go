package filesafe

import (
	"fmt"
	"os"
)

// ensurePrivateDir refuses: ownership by the effective uid and a 0700 mode
// have no Windows equivalent (access is an ACL), so no verdict could keep
// EnsurePrivateDir's guarantee. The two checks refuse for the same reason.
func ensurePrivateDir(clean string, _ *cfg) (PrivateDirResult, error) {
	return PrivateDirResult{}, fmt.Errorf("%w: EnsurePrivateDir %s", ErrModeUnsupported, clean)
}

func checkPrivateDir(clean string) error {
	return fmt.Errorf("%w: CheckPrivateDir %s", ErrModeUnsupported, clean)
}

func checkPrivateDirInRoot(_ *os.Root, clean string) error {
	return fmt.Errorf("%w: CheckPrivateDirInRoot %s", ErrModeUnsupported, clean)
}
