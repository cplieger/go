package filesafe

// modesSupported refuses: Go's chmod on Windows only toggles the read-only
// attribute (os.Chmod), so no mode can be set or proved there.
func modesSupported() error { return ErrModeUnsupported }
