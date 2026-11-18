package chartdata

import (
	"time"

	"github.com/cplieger/go/measure/v2"
)

// Count is an API row's figure for a count read elsewhere.
type Count struct {
	Value  *float64          `json:"value"`
	State  measure.ReadState `json:"state"`
	AsOf   *time.Time        `json:"as_of"`
	Reason string            `json:"reason,omitempty"`
}

// CountOf builds the Count of last under r.
func CountOf(last *float64, r measure.Read) (Count, error) {
	return Count{}, nil
}

// FrameRead is how the read behind a frame went.
type FrameRead struct {
	State  measure.ReadState `json:"state"`
	AsOf   *time.Time        `json:"as_of"`
	Reason string            `json:"reason,omitempty"`
}

// WithRead marks the frame with r.
func WithRead(r measure.Read) Option {
	return func(*options) {}
}
