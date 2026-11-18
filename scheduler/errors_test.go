package scheduler

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestErrorsMatch(t *testing.T) {
	t.Parallel()
	t.Run("recent", func(t *testing.T) {
		t.Parallel()
		until := time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)
		err := fmt.Errorf("start: %w", &RecentError{Until: until})
		if !errors.Is(err, ErrRecent) {
			t.Errorf("errors.Is(%v, ErrRecent) = false, want true", err)
		}
		var re *RecentError
		if !errors.As(err, &re) || !re.Until.Equal(until) {
			t.Errorf("errors.As(%v) = %v, want Until %v", err, re, until)
		}
		if errors.Is(err, ErrCovered) {
			t.Errorf("errors.Is(%v, ErrCovered) = true, want false", err)
		}
	})
	t.Run("covered", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("yield: %w", &CoveredError{By: "abc"})
		if !errors.Is(err, ErrCovered) {
			t.Errorf("errors.Is(%v, ErrCovered) = false, want true", err)
		}
		var ce *CoveredError
		if !errors.As(err, &ce) || ce.By != "abc" {
			t.Errorf("errors.As(%v) = %v, want By abc", err, ce)
		}
		if errors.Is(err, ErrRecent) {
			t.Errorf("errors.Is(%v, ErrRecent) = true, want false", err)
		}
	})
	t.Run("distinct", func(t *testing.T) {
		t.Parallel()
		all := []error{
			ErrUnknownKind, ErrScope, ErrTooManyScopes, ErrLease, ErrCovered, ErrYieldTooLong, ErrBatchSize, ErrProposed,
			ErrQueueFull, ErrCapacity, ErrRecent, ErrUnknownJob, ErrNotCancellable,
			ErrCommitting, ErrUnknownLine, ErrStoreUnavailable,
		}
		for i, a := range all {
			for j, b := range all {
				if i != j && errors.Is(a, b) {
					t.Errorf("errors.Is(%v, %v) = true, want distinct sentinels", a, b)
				}
			}
		}
	})
}
