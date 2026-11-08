//go:build !windows

package filesafe

// modesSupported reports that fchmod and fstat can set and prove a mode.
func modesSupported() error { return nil }
