package filesafe

import (
	"errors"
	"fmt"
	"testing"
)

func TestWritePhase_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		want  string
		phase WritePhase
	}{
		{"create", "create temp file", PhaseTempCreate},
		{"write", "write temp file", PhaseTempWrite},
		{"chmod", "chmod temp file", PhaseTempChmod},
		{"sync", "sync temp file", PhaseTempSync},
		{"close", "close temp file", PhaseTempClose},
		{"rename", "rename to final path", PhaseRename},
		{"zero_is_unknown", "unknown phase", WritePhase(0)},
		{"out_of_range_is_unknown", "unknown phase", WritePhase(99)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.phase.String(); got != tt.want {
				t.Errorf("WritePhase(%d).String() = %q, want %q", int(tt.phase), got, tt.want)
			}
		})
	}
}

func TestWriteError_Unwrap_PreservesChain(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("underlying io failure")
	we := &WriteError{Phase: PhaseTempWrite, Err: sentinel}
	if !errors.Is(we, sentinel) {
		t.Errorf("errors.Is(WriteError, sentinel) = false, want true")
	}
	if got := we.Unwrap(); got != sentinel {
		t.Errorf("WriteError.Unwrap() = %v, want %v", got, sentinel)
	}
}

func TestPlatformSentinels_Messages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		name string
		want string
	}{
		{ErrFileBusy, "file_busy", "filesafe: target in use or access denied"},
		{ErrModeUnsupported, "mode_unsupported", "filesafe: file modes are not supported on this platform"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrFileBusy_MatchesThroughWriteError(t *testing.T) {
	t.Parallel()
	cause := errors.New("sharing violation")
	err := error(&WriteError{
		Phase: PhaseRename,
		Err:   fmt.Errorf("%w: %d attempts: %w", ErrFileBusy, 7, cause),
	})
	t.Run("sentinel", func(t *testing.T) {
		t.Parallel()
		if !errors.Is(err, ErrFileBusy) {
			t.Errorf("errors.Is(%v, ErrFileBusy) = false, want true", err)
		}
	})
	t.Run("cause", func(t *testing.T) {
		t.Parallel()
		if !errors.Is(err, cause) {
			t.Errorf("errors.Is(%v, cause) = false, want true", err)
		}
	})
	t.Run("phase", func(t *testing.T) {
		t.Parallel()
		var we *WriteError
		if !errors.As(err, &we) || we.Phase != PhaseRename {
			t.Errorf("errors.As(%v) phase = %v, want %v", err, we, PhaseRename)
		}
	})
	t.Run("distinct_from_mode_unsupported", func(t *testing.T) {
		t.Parallel()
		if errors.Is(err, ErrModeUnsupported) {
			t.Errorf("errors.Is(%v, ErrModeUnsupported) = true, want false", err)
		}
	})
}

func TestWriteError_Error_FormatsPhasePrefix(t *testing.T) {
	t.Parallel()
	we := &WriteError{Phase: PhaseRename, Err: errors.New("disk gone")}
	got := we.Error()
	want := "filesafe: rename to final path: disk gone"
	if got != want {
		t.Errorf("WriteError.Error() = %q, want %q", got, want)
	}
}
