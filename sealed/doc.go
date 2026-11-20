// Package sealed keeps an app's key file and seals short values under the
// keys it holds, with AES-256-GCM.
//
// A [KeyFile] is the file at a path the app chooses. It holds a current key
// and, between [KeyFile.Rotate] and [KeyFile.Retire], the previous one. Its
// [Sealer] seals under the current key and opens under either, and reports
// which one opened a value, so the caller can seal it again. Every value is
// bound to a [Purpose], and a Sealer derives keys for other uses by a
// [Label]: [Sealer.Subkey] and [Sealer.MAC].
//
// A KeyFile holding a key holds the file's lease until [KeyFile.Close], so
// Rotate and Retire run in the process that serves the key file, and an
// admin command reaches that process instead of opening the file.
//
// The formats of the key file and of sealed values are in docs/format.md.
package sealed
