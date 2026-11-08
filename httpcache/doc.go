// Package httpcache is an in-memory revalidation cache for HTTP GET requests.
// It keeps earlier answers and replays each one's validator (If-None-Match or
// If-Modified-Since), and serves a held answer only when the server answers
// 304 Not Modified in the same exchange. Nothing is served on age.
//
// [NewRoundTripper] puts the cache beneath any [net/http.Client].
// [New] with [Cache.Begin] is the same cache for code that sends, reads and
// decodes its own answers, sent through a [Guard]. Retries are not this
// package's job: put github.com/cplieger/go/httpclient's RetryRoundTripper
// beneath the cache.
//
// docs/caching.md holds the full contract: what is kept, when a 304 serves
// it, the key and the memory bound.
package httpcache
