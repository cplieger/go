package httpclient

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
)

// LogSafeError keeps this package's marks, the transport's retry steps and
// every errors.Join or multi-%w branch around the reduced cause, and drops any
// other wrapper above a *url.Error, whose message was rendered with the URL in
// it.
func TestLogSafeError_KeepsMarksAndJoinBranchesDropsOtherWrappers(t *testing.T) {
	t.Parallel()
	const secretURL = "https://api.example/x?token=s3cret"
	urlErr := func() error { return &url.Error{Op: "Get", URL: secretURL, Err: io.ErrUnexpectedEOF} }
	sibling := errors.New("independent failure")
	cases := []struct {
		name  string
		in    error
		check func(t *testing.T, got error)
	}{
		{"Permanent", Permanent(urlErr()), func(t *testing.T, got error) {
			if !IsPermanent(got) || !errors.Is(got, io.ErrUnexpectedEOF) {
				t.Errorf("got %v: IsPermanent=%v, want the mark kept over the cause", got, IsPermanent(got))
			}
		}},
		// The cause is not transient by itself, so only the kept mark makes
		// the reduced error transient.
		{"MarkTransient", MarkTransient(&url.Error{Op: "Get", URL: secretURL, Err: errors.New("boom")}), func(t *testing.T, got error) {
			if !IsTransient(got) {
				t.Errorf("got %v, want the transient mark kept", got)
			}
		}},
		{"AttemptTimeout", AttemptTimeout(urlErr()), func(t *testing.T, got error) {
			if !IsAttemptTimeout(got) {
				t.Errorf("got %v, want the attempt-timeout mark kept", got)
			}
		}},
		{"errors.Join sibling", errors.Join(sibling, urlErr()), func(t *testing.T, got error) {
			if !errors.Is(got, sibling) || !errors.Is(got, io.ErrUnexpectedEOF) {
				t.Errorf("got %v, want both branches kept", got)
			}
		}},
		{"fmt.Errorf wrapper", fmt.Errorf("fetch items: %w", urlErr()), func(t *testing.T, got error) {
			if !errors.Is(got, io.ErrUnexpectedEOF) {
				t.Errorf("got %v, want the cause", got)
			}
		}},
		{"multi-%w wrapper", fmt.Errorf("a: %w, b: %w", sibling, urlErr()), func(t *testing.T, got error) {
			if !errors.Is(got, sibling) || !errors.Is(got, io.ErrUnexpectedEOF) {
				t.Errorf("got %v, want both wrapped errors kept", got)
			}
			if got.Error() != "independent failure\nunexpected EOF" {
				t.Errorf("got %q, want the branches rejoined without the wrapper's text", got)
			}
		}},
		{"retry step", &stageError{stage: "prepare retry", err: urlErr()}, func(t *testing.T, got error) {
			if got.Error() != "prepare retry: unexpected EOF" || !errors.Is(got, io.ErrUnexpectedEOF) {
				t.Errorf("got %q, want the step kept over the cause", got)
			}
		}},
		{"retry step over a cause that reduces to nothing", &stageError{stage: "rewind request body", err: emptyJoinURL{}}, func(t *testing.T, got error) {
			if got.Error() != "rewind request body: request failed" {
				t.Errorf("got %q, want the step over the placeholder", got)
			}
		}},
		{"a join branch that reduces to nothing", errors.Join(sibling, emptyJoinURL{}), func(t *testing.T, got error) {
			if got.Error() != "independent failure\nrequest failed" {
				t.Errorf("got %q, want the sibling and the placeholder", got)
			}
		}},
		{"a join of nil branches", nilBranchesURL{}, func(t *testing.T, got error) {
			if got.Error() != "request failed" {
				t.Errorf("got %q, want the placeholder", got)
			}
		}},
		{"a cause that reduces to nothing", emptyJoinURL{}, func(t *testing.T, got error) {
			if got.Error() != "request failed" {
				t.Errorf("got %q, want the placeholder", got)
			}
		}},
		{"Permanent over a cause that reduces to nothing", Permanent(emptyJoinURL{}), func(t *testing.T, got error) {
			if got.Error() != "request failed" || !IsPermanent(got) {
				t.Errorf("got %q, want the mark over the placeholder", got)
			}
		}},
		{"MarkTransient over a cause that reduces to nothing", MarkTransient(emptyJoinURL{}), func(t *testing.T, got error) {
			if got.Error() != "request failed" || !IsTransient(got) {
				t.Errorf("got %q, want the mark over the placeholder", got)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := LogSafeError(tc.in)
			if got == nil {
				t.Fatal("LogSafeError reduced a non-nil error to nil")
			}
			if strings.Contains(got.Error(), "s3cret") || strings.Contains(got.Error(), "api.example") {
				t.Fatalf("LogSafeError(%s) = %q, still renders the URL", tc.name, got)
			}
			tc.check(t, got)
		})
	}
}

// formattedCause renders one text through Error and another through fmt's %v.
type formattedCause struct{}

func (formattedCause) Error() string { return "plain" }
func (formattedCause) Format(f fmt.State, verb rune) {
	if verb == 'v' {
		_, _ = f.Write([]byte("formatted-v"))
	}
}

// A retry step renders its cause as fmt.Errorf's %w does, so a cause that
// implements fmt.Formatter reads the same through the step.
func TestStageError_renders_the_cause_as_w_does(t *testing.T) {
	t.Parallel()
	got := (&stageError{stage: "prepare retry", err: formattedCause{}}).Error()
	if want := fmt.Errorf("prepare retry: %w", formattedCause{}).Error(); got != want || got != "prepare retry: formatted-v" {
		t.Errorf("stageError.Error() = %q, want %q as fmt.Errorf renders it", got, want)
	}
}

// emptyJoinURL surfaces a *url.Error through an As method and unwraps to no
// branches, so its branches rejoin to nothing and it reduces to the placeholder.
type emptyJoinURL struct{}

func (emptyJoinURL) Error() string   { return "Get \"https://api.example/x?token=s3cret\": EOF" }
func (emptyJoinURL) Unwrap() []error { return nil }
func (emptyJoinURL) As(target any) bool {
	if p, ok := target.(**url.Error); ok {
		*p = &url.Error{Op: "Get", URL: "https://api.example/x?token=s3cret", Err: io.EOF}
		return true
	}
	return false
}

// nilBranchesURL is emptyJoinURL with one nil branch, which errors.Join drops.
type nilBranchesURL struct{ emptyJoinURL }

func (nilBranchesURL) Unwrap() []error { return []error{nil} }

// asURLError surfaces a *url.Error through an As method and no Unwrap, so the
// reduction has no chain to descend.
type asURLError struct{}

func (asURLError) Error() string { return "Get \"https://api.example/x?token=s3cret\": EOF" }
func (asURLError) As(target any) bool {
	if p, ok := target.(**url.Error); ok {
		*p = &url.Error{Op: "Get", URL: "https://api.example/x?token=s3cret", Err: io.EOF}
		return true
	}
	return false
}

func TestLogSafeError_AnUnwrapLessURLErrorReducesToAPlaceholder(t *testing.T) {
	t.Parallel()
	got := LogSafeError(asURLError{})
	if got == nil || strings.Contains(got.Error(), "s3cret") {
		t.Fatalf("LogSafeError(asURLError) = %v, want a non-nil error without the URL", got)
	}
}
