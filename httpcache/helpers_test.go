package httpcache_test

import (
	"net/http"
	"sync/atomic"
)

// idleCounter is a round tripper that counts the calls to close its idle
// connections.
type idleCounter struct {
	http.RoundTripper
	closed atomic.Int32
}

func (c *idleCounter) CloseIdleConnections() { c.closed.Add(1) }
