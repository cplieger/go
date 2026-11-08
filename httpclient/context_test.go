package httpclient

import (
	"context"
	"testing"
	"time"
)

// TestContextWithDefaultTimeout pins the deadline-application rule: the
// default bounds the context only when the caller brought no deadline, and
// a caller deadline is never undercut or extended.
func TestContextWithDefaultTimeout(t *testing.T) {
	t.Parallel()

	t.Run("no deadline applies default", func(t *testing.T) {
		t.Parallel()
		before := time.Now()
		ctx, cancel := ContextWithDefaultTimeout(t.Context(), time.Minute)
		defer cancel()
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected a deadline to be applied")
		}
		remaining := time.Until(dl)
		if remaining <= 0 || remaining > time.Minute {
			t.Errorf("deadline %v not within (now, now+1m]", dl)
		}
		if dl.Before(before) {
			t.Errorf("deadline %v before call time %v", dl, before)
		}
	})

	t.Run("caller deadline never undercut by smaller default", func(t *testing.T) {
		t.Parallel()
		parent, parentCancel := context.WithTimeout(t.Context(), time.Hour)
		defer parentCancel()
		want, _ := parent.Deadline()

		ctx, cancel := ContextWithDefaultTimeout(parent, time.Millisecond)
		defer cancel()
		got, ok := ctx.Deadline()
		if !ok || !got.Equal(want) {
			t.Errorf("deadline = %v (ok=%v), want caller's %v untouched", got, ok, want)
		}
		if ctx != parent {
			t.Error("expected the caller's context to pass through unchanged")
		}
	})

	t.Run("caller deadline never extended by larger default", func(t *testing.T) {
		t.Parallel()
		parent, parentCancel := context.WithTimeout(t.Context(), time.Millisecond)
		defer parentCancel()
		want, _ := parent.Deadline()

		ctx, cancel := ContextWithDefaultTimeout(parent, time.Hour)
		defer cancel()
		got, ok := ctx.Deadline()
		if !ok || !got.Equal(want) {
			t.Errorf("deadline = %v (ok=%v), want caller's %v untouched", got, ok, want)
		}
	})

	t.Run("non-positive default leaves ctx unbounded", func(t *testing.T) {
		t.Parallel()
		for _, def := range []time.Duration{0, -time.Second} {
			ctx, cancel := ContextWithDefaultTimeout(t.Context(), def)
			if _, ok := ctx.Deadline(); ok {
				t.Errorf("def=%v: expected no deadline", def)
			}
			cancel() // must be callable without effect
			if ctx.Err() != nil {
				t.Errorf("def=%v: no-op cancel cancelled the context: %v", def, ctx.Err())
			}
		}
	})

	t.Run("passthrough cancel does not cancel the parent", func(t *testing.T) {
		t.Parallel()
		parent, parentCancel := context.WithTimeout(t.Context(), time.Hour)
		defer parentCancel()
		_, cancel := ContextWithDefaultTimeout(parent, time.Minute)
		cancel()
		if parent.Err() != nil {
			t.Errorf("no-op cancel cancelled the parent: %v", parent.Err())
		}
	})

	t.Run("applied cancel releases the derived context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := ContextWithDefaultTimeout(t.Context(), time.Hour)
		cancel()
		if ctx.Err() == nil {
			t.Error("expected the derived context to be cancelled")
		}
	})
}
