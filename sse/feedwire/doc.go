// Package feedwire is the wire of the change feed: the changed and listening
// frames and the Feed-Position header. A server writes every frame and header
// value through its encoders, and @cplieger/sse/feed in this module's web/
// tree reads them; both halves test against testdata/feed/cases.json.
//
// The decoders take untrusted input and are strict: every member is required
// or named, every name and epoch matches its grammar, every number is an
// integer in its range, and frame data above 64 KiB is refused unread. It
// imports only the standard library.
package feedwire
