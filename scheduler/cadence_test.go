package scheduler

import (
	"testing"
	"time"
)

func TestCadenceBounds(t *testing.T) {
	t.Run("the zero value is trigger-only", func(t *testing.T) {
		var zero Cadence
		if zero != TriggerOnly() {
			t.Errorf("Cadence{} = %+v, want TriggerOnly() %+v", zero, TriggerOnly())
		}
		if every, ok := zero.Every(); ok || every != 0 {
			t.Errorf("Cadence{}.Every() = %v, %v; want 0, false", every, ok)
		}
		if got := zero.wait(false); got != 0 {
			t.Errorf("Cadence{}.wait(false) = %v, want 0 (no timer)", got)
		}
	})

	t.Run("scheduled is at least one second", func(t *testing.T) {
		for name, tc := range map[string]struct {
			every, want time.Duration
		}{
			"zero":         {0, time.Second},
			"negative":     {-time.Hour, time.Second},
			"sub-second":   {500 * time.Millisecond, time.Second},
			"one second":   {time.Second, time.Second},
			"fifteen min":  {15 * time.Minute, 15 * time.Minute},
			"one day kept": {24 * time.Hour, 24 * time.Hour},
		} {
			every, ok := Scheduled(tc.every).Every()
			if !ok || every != tc.want {
				t.Errorf("%s: Scheduled(%v).Every() = %v, %v; want %v, true", name, tc.every, every, ok, tc.want)
			}
		}
	})

	t.Run("an end waits the interval without retry", func(t *testing.T) {
		c := Scheduled(24 * time.Hour)
		for _, done := range []bool{true, false} {
			if got := c.wait(done); got != 24*time.Hour {
				t.Errorf("Scheduled(24h).wait(done=%v) = %v, want 24h", done, got)
			}
		}
	})

	t.Run("retry is raised to one second and capped at the interval", func(t *testing.T) {
		for name, tc := range map[string]struct {
			retry, want time.Duration
		}{
			"zero":        {0, time.Second},
			"sub-second":  {10 * time.Millisecond, time.Second},
			"in range":    {15 * time.Minute, 15 * time.Minute},
			"at interval": {time.Hour, time.Hour},
			"over":        {2 * time.Hour, time.Hour},
		} {
			c := Scheduled(time.Hour).Retry(tc.retry)
			if got := c.wait(false); got != tc.want {
				t.Errorf("%s: Scheduled(1h).Retry(%v).wait(false) = %v, want %v", name, tc.retry, got, tc.want)
			}
			if got := c.wait(true); got != time.Hour {
				t.Errorf("%s: Scheduled(1h).Retry(%v).wait(true) = %v, want the interval 1h", name, tc.retry, got)
			}
			if every, ok := c.Every(); !ok || every != time.Hour {
				t.Errorf("%s: Retry changed Every() to %v, %v; want 1h, true", name, every, ok)
			}
		}
	})

	t.Run("the last retry wins", func(t *testing.T) {
		c := Scheduled(time.Hour).Retry(5 * time.Minute).Retry(10 * time.Minute)
		if got := c.wait(false); got != 10*time.Minute {
			t.Errorf("Retry(5m).Retry(10m).wait(false) = %v, want 10m", got)
		}
		if c != Scheduled(time.Hour).Retry(10*time.Minute) {
			t.Errorf("Retry(5m).Retry(10m) = %+v, want it equal to Retry(10m) alone", c)
		}
	})

	t.Run("trigger-only ignores retry", func(t *testing.T) {
		c := TriggerOnly().Retry(15 * time.Minute)
		if c != TriggerOnly() {
			t.Errorf("TriggerOnly().Retry(15m) = %+v, want TriggerOnly()", c)
		}
		if every, ok := c.Every(); ok || every != 0 {
			t.Errorf("TriggerOnly().Retry(15m).Every() = %v, %v; want 0, false", every, ok)
		}
		if got := c.wait(false); got != 0 {
			t.Errorf("TriggerOnly().Retry(15m).wait(false) = %v, want 0", got)
		}
	})
}
