package filesafe

import (
	"errors"
	"fmt"
)

func takeLease(string, bool) (*Lease, error) {
	return nil, fmt.Errorf("%w: lease: %w", ErrModeUnsupported, errors.ErrUnsupported)
}
