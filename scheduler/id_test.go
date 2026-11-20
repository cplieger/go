package scheduler

import "testing"

func TestNewIDIs32LowercaseHex(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for range 64 {
		id := newID()
		if len(id) != 32 {
			t.Fatalf("newID() = %q, want 32 characters", id)
		}
		for _, c := range id {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				t.Fatalf("newID() = %q, want lowercase hex only", id)
			}
		}
		if seen[id] {
			t.Fatalf("newID() repeated %q", id)
		}
		seen[id] = true
	}
	if !validScope(newID()) {
		t.Error("a new ID is outside scope grammar, so a covered reason cannot name it")
	}
}
