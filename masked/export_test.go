package masked

// MaskedBytes returns the masked bytes s holds, or nil for the zero Secret.
func MaskedBytes(s Secret) []byte {
	if s.p == nil {
		return nil
	}
	return s.p.b
}
