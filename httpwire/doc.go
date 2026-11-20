// Package httpwire declares a JSON API once, as a table of [Endpoint] rows,
// and binds typed handlers to it: decoding, the error envelope and its code
// catalog, Idempotency-Key replay, list pages and the wire version.
package httpwire
