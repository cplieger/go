// Package masked holds a secret in memory so that no printer shows it.
//
// A [Secret] renders "[redacted]" through every printing route (fmt verbs,
// log/slog, encoding/json) and holds only masked bytes for a printer that
// walks fields by reflection. Only [Secret.Reveal] returns the text. A
// Secret is not a wire or storage type and has no decoder: a caller wraps a
// decoded string with [NewSecret] where the secret enters and calls Reveal
// where it is sent (a header, a seal, a hash).
//
// Package maskedtest checks, in a caller's tests, that no field of its
// types holds a secret in clear and that no printer output contains one.
package masked
