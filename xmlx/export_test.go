package xmlx

// Resolve is the unexported Limits.resolve, for the external test that pins
// each field's zero-to-default substitution.
func Resolve(l Limits) (Limits, error) { return l.resolve() }
