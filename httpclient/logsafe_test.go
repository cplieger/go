package httpclient

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
)

// LogSafeError keeps this package's marks and every errors.Join branch around
// the reduced cause, and drops any other wrapper above a *url.Error, whose
// message was rendered with the URL in it.
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
		{"MarkTransient", MarkTransient(urlErr()), func(t *testing.T, got error) {
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
