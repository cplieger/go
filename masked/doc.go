// Package masked holds a secret in memory so that no printer shows it.
//
// A [Secret] renders "[redacted]" through every fmt verb that reaches its
// Format method, log/slog and encoding/json; %p prints an address or fmt's
// bad-verb form and %T the type name, neither of them the text. It holds
// only masked bytes for a printer that walks fields by reflection. Only
// [Secret.Reveal] returns the text. A
// Secret is not a wire or storage type and has no decoder: a caller wraps a
// decoded string with [NewSecret] where the secret enters and calls Reveal
// where it is sent (a header, a seal, a hash).
//
// Package maskedtest checks, in a caller's tests, that no field of the types
// that hold its secrets carries one in clear, that no wire field named like
// a credential carries text unreviewed, and that no printer output contains
// a secret.
package masked
