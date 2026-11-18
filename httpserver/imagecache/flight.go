package imagecache

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"time"
)

// budget bounds a fetch whose request carries no deadline, as the upstream
// clients this package replaces did.
const budget = 30 * time.Second

type result uint8

const (
	resUnavailable result = iota
	resNotFound
	resImage
)

// flight is one fetch of one file name. img and res are read only after done
// is closed; waiters, forgotten and the maps holding it are guarded by
// Cache.mu; the other fields are set at start and never change.
type flight struct {
	done       chan struct{}
	cancel     context.CancelFunc
	name       string
	key        Key
	img        Image
	waiters    int
	res        result
	background bool
	forgotten  bool
}

var errFetchPanic = errors.New("imagecache: Fetch panicked")

// startLocked registers and runs a flight for k. The caller holds mu and
// has checked c.closed, which keeps wg.Go from racing Shutdown's Wait.
func (c *Cache) startLocked(ctx context.Context, cancel context.CancelFunc, k Key, name string, background bool) *flight {
	fl := &flight{done: make(chan struct{}), cancel: cancel, name: name, key: k, background: background}
	if !background {
		fl.waiters = 1
	}
	c.flights[name] = fl
	c.live[fl] = struct{}{}
	c.wg.Go(func() { c.run(ctx, fl) })
	return fl
}

// foregroundContext keeps the first caller's values and deadline, or the
// budget when it has none, but not its cancellation: the flight serves every
// caller that joins it and ends when the last one leaves.
func foregroundContext(r *http.Request) (context.Context, context.CancelFunc) {
	ctx := context.WithoutCancel(r.Context())
	if dl, ok := r.Context().Deadline(); ok {
		return context.WithDeadline(ctx, dl)
	}
	return context.WithTimeout(ctx, budget)
}

// refetchLocked starts the background refetch of a stale image unless one
// is already in flight. The caller holds mu.
func (c *Cache) refetchLocked(r *http.Request, k Key, name string) {
	if c.closed || c.flights[name] != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), budget)
	c.startLocked(ctx, cancel, k, name, true)
}

// join answers the flight a Serve with nothing kept waits on, starting one
// when none is in flight, or false once the cache is shut down.
func (c *Cache) join(r *http.Request, k Key, name string) (*flight, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, false
	}
	if fl := c.flights[name]; fl != nil {
		fl.waiters++
		return fl, true
	}
	ctx, cancel := foregroundContext(r)
	return c.startLocked(ctx, cancel, k, name, false), true
}

// leave is a waiter giving up before fl is done. The last waiter of a
// foreground flight cancels it and frees its name for a fresh fetch.
func (c *Cache) leave(fl *flight) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fl.waiters--
	if fl.waiters > 0 || fl.background {
		return
	}
	fl.cancel()
	if c.flights[fl.name] == fl {
		delete(c.flights, fl.name)
	}
}

func (c *Cache) forgetFlightLocked(fl *flight) {
	fl.forgotten = true
	delete(c.flights, fl.name)
	if fl.background {
		fl.cancel()
	}
}

// run fetches and keeps the image before answering the flight's waiters, so
// a Serve that answered 200 left the image kept, unless its write failed.
func (c *Cache) run(ctx context.Context, fl *flight) {
	defer fl.cancel()
	img, res := c.fetchChecked(ctx, fl.key)
	if res == resImage {
		c.store(fl, img, time.Now())
	}
	c.mu.Lock()
	fl.img, fl.res = img, res
	close(fl.done)
	if c.flights[fl.name] == fl {
		delete(c.flights, fl.name)
	}
	delete(c.live, fl)
	c.mu.Unlock()
}

func (c *Cache) fetchChecked(ctx context.Context, k Key) (Image, result) {
	if err := c.sem.Acquire(ctx, 1); err != nil {
		c.log().Debug("imagecache: fetch not started", "space", k.Space, "file", k.File, "err", err)
		return Image{}, resUnavailable
	}
	img, found, err := c.call(ctx, k)
	c.sem.Release(1)
	switch {
	case err != nil:
		c.log().Debug("imagecache: fetch failed", "space", k.Space, "file", k.File, "err", err)
		return Image{}, resUnavailable
	case !found:
		return Image{}, resNotFound
	}
	if why := c.check(img); why != "" {
		c.log().Warn("imagecache: image dropped", "space", k.Space, "file", k.File, "reason", why,
			"type", img.Type, "sniffed", http.DetectContentType(img.Data), "size", len(img.Data))
		return Image{}, resUnavailable
	}
	return img, resImage
}

// call runs the app's Fetch, turning a panic into a failed fetch so one bad
// callback cannot end the process.
func (c *Cache) call(ctx context.Context, k Key) (img Image, found bool, err error) {
	defer func() {
		if p := recover(); p != nil {
			c.log().Error("imagecache: Fetch panicked", "space", k.Space, "file", k.File,
				"panic", p, "stack", string(debug.Stack()))
			img, found, err = Image{}, false, errFetchPanic
		}
	}()
	return c.fetch(ctx, k)
}

// check answers why img is not kept, or "" when it is: its declared type is
// one of the four and is what http.DetectContentType sniffs, and its size
// is 1 to MaxImage.
func (c *Cache) check(img Image) string {
	switch n := int64(len(img.Data)); {
	case n == 0:
		return "empty"
	case n > c.maxImage:
		return "over MaxImage"
	case !kept[img.Type]:
		return "type not kept"
	case http.DetectContentType(img.Data) != img.Type:
		return "sniffed type differs"
	}
	return ""
}

// Shutdown cancels every fetch the Cache runs, foreground and background,
// and waits for each to return, or for ctx to end, answering ctx.Err().
// After it, Serve answers kept images without refetching and a miss with
// image_unavailable, never calling Fetch. Call it before closing Root. It
// is safe to call more than once.
func (c *Cache) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	for fl := range c.live {
		fl.cancel()
	}
	c.mu.Unlock()
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
