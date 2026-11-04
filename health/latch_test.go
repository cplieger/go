package health

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLatch_ForwardsBothValuesBeforeDrain covers the pass-through half: until
// BeginDrain runs, the latch is transparent in both directions.
func TestLatch_ForwardsBothValuesBeforeDrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	l := NewLatch(NewMarker(path))

	l.Set(true)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("marker should exist after Set(true) before drain: %v", err)
	}

	l.Set(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("marker should not exist after Set(false) before drain: %v", err)
	}
}

// TestLatch_BeginDrainMarksUnhealthyImmediately pins the promptness half of
// the contract: observers see the draining state at BeginDrain, not when
// in-flight work eventually reports.
func TestLatch_BeginDrainMarksUnhealthyImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	l := NewLatch(NewMarker(path))
	l.Set(true)

	l.BeginDrain()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("marker should be removed by BeginDrain itself: %v", err)
	}
}

// TestLatch_HealthyWriteAfterDrainIsDropped pins the monotonicity rule the
// type exists for: a run finishing clean during drain can never flip the
// marker back to healthy.
func TestLatch_HealthyWriteAfterDrainIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	l := NewLatch(NewMarker(path))
	l.Set(true)
	l.BeginDrain()

	l.Set(true)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Set(true) after BeginDrain must not restore the marker")
	}
}

// TestLatch_UnhealthyWriteAfterDrainStillLands pins the asymmetry: only
// healthy writes are latched. A failure discovered during drain is
// information and must reach the marker (observable here because dropping
// the write would leave the healthy marker in place).
func TestLatch_UnhealthyWriteAfterDrainStillLands(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".healthy")
	m := NewMarker(path)
	l := NewLatch(m)
	l.BeginDrain()

	// Recreate the marker behind the latch, standing in for any healthy
	// state a dropped unhealthy write would wrongly preserve.
	m.Set(true)

	l.Set(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Set(false) after BeginDrain must still remove the marker")
	}
}
