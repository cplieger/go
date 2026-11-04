package yamlenv

import "testing"

// TestIsLinePrefix pins the boundary cases of the "line <digits>" guard that
// gates the duplicate-key and unknown-key rebuilds in sanitizeEntry: only an
// exact bare "line N" prefix qualifies; empty input, a missing/non-numeric
// number, and an unmarshal-shaped prefix all fail.
func TestIsLinePrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want bool
	}{
		{"line 4", true},
		{"line 123", true},
		{"", false},
		{"line", false},
		{"line ", false},
		{"line 4x", false},
		{"line x", false},
		{"LINE 4", false},
		{" line 4", false},
		{"line 4: cannot unmarshal !!str `x`", false},
	}
	for _, tt := range tests {
		if got := isLinePrefix(tt.in); got != tt.want {
			t.Errorf("isLinePrefix(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
