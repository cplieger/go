package httpclient_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cplieger/go/httpclient"
)

func TestConcurrentReadsKeepTheCacheConsistent(t *testing.T) {
	const bound = 16 << 10
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := newOwner(serveOrigin(t, o), ownerConfig{cache: httpclient.CacheConfig{MaxBytes: bound}})
	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Go(func() {
			for i := range 20 {
				path := "/user"
				if worker%2 == 1 {
					path = fmt.Sprintf("/user/%d", (worker*20+i)%37)
				}
				got, _, err := c.get(t, path)
				if err != nil {
					t.Errorf("a concurrent read = %v", err)
					return
				}
				if got.Login != "octo" {
					t.Errorf("a concurrent read = %+v, want the body the server holds", got)
					return
				}
			}
		})
	}
	wg.Wait()
	if got := c.cache.State(); got.Bytes > bound || got.Hits+got.Misses != 16*20 {
		t.Errorf("State = %+v, want at most %d bytes held and every one of the %d reads counted once", got, bound, 16*20)
	}
}
