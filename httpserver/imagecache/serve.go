package imagecache

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/cplieger/go/httpserver/v2/internal/httpfield"
)

const cacheControl = "private, max-age=86400"

// Serve answers k's image: a kept one at once (refetching it in the
// background once older than Refresh), else one fetched under r's context,
// with callers of one key sharing one fetch. A 200 carries the image's type,
// a strong ETag and Cache-Control: private, max-age=86400, and a matching
// If-None-Match answers 304. Errors go through Options.Errors:
// image_not_found 404 when the upstream has no such image or a key part is
// over 128 bytes, image_unavailable 503 with Retry-After: 60 when it cannot
// give one now and nothing is kept.
func (c *Cache) Serve(w http.ResponseWriter, r *http.Request, k Key) {
	if part, n := longPart(k); part != "" {
		c.log().Warn("imagecache: key part over 128 bytes", "part", part, "len", n)
		c.notFound(w, r)
		return
	}
	name := fileName(k)
	if e := c.lookup(r, k, name); e != nil {
		if notModified(w, r, e.etag) || c.serveFile(w, r, e) {
			return
		}
		c.mu.Lock()
		if c.index[name] == e {
			c.unindexLocked(e)
			c.orphans[name] = struct{}{}
		}
		c.mu.Unlock()
	}
	fl, ok := c.join(r, k, name)
	if !ok {
		c.unavailable(w, r)
		return
	}
	select {
	case <-fl.done:
	case <-r.Context().Done():
		c.leave(fl)
		c.unavailable(w, r)
		return
	}
	switch fl.res {
	case resImage:
		etag := etagOf(sha256.Sum256(fl.img.Data))
		if !notModified(w, r, etag) {
			send(w, r, fl.img.Type, etag, int64(len(fl.img.Data)), bytes.NewReader(fl.img.Data))
		}
	case resNotFound:
		c.notFound(w, r)
	default:
		c.unavailable(w, r)
	}
}

// lookup answers name's kept entry, starting its background refetch when it
// is older than Refresh.
func (c *Cache) lookup(r *http.Request, k Key, name string) *entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.index[name]
	if e != nil && time.Since(e.fetched) > c.refresh {
		c.refetchLocked(r, k, name)
	}
	return e
}

// serveFile streams e's file, answering false when it is gone or unreadable
// and nothing was written. Type, ETag and length come from the opened file
// itself, which a concurrent refetch or eviction cannot change under it.
func (c *Cache) serveFile(w http.ResponseWriter, r *http.Request, e *entry) bool {
	f, err := c.root.Open(e.name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			c.log().Warn("imagecache: open failed", "file", e.name, "err", err)
		}
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		c.log().Warn("imagecache: stat failed", "file", e.name, "err", err)
		return false
	}
	buf := make([]byte, min(st.Size(), headerCap))
	if _, err = io.ReadFull(f, buf); err != nil {
		c.log().Warn("imagecache: read failed", "file", e.name, "err", err)
		return false
	}
	h, hlen, err := parseHeader(buf, st.Size())
	if err != nil {
		c.log().Warn("imagecache: cache file unreadable", "file", e.name, "err", err)
		return false
	}
	send(w, r, h.typ, etagOf(h.sum), h.n, io.NewSectionReader(f, int64(hlen), h.n))
	return true
}

// notModified answers 304 when If-None-Match matches etag (RFC 9110 section
// 13.1.2); a 304 carries the headers a 200 would cache by (section 15.4.5).
func notModified(w http.ResponseWriter, r *http.Request, etag string) bool {
	if !httpfield.IfNoneMatch(r.Header, etag) {
		return false
	}
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", cacheControl)
	w.WriteHeader(http.StatusNotModified)
	return true
}

func send(w http.ResponseWriter, r *http.Request, typ, etag string, n int64, body io.Reader) {
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("Content-Length", strconv.FormatInt(n, 10))
	h.Set("ETag", etag)
	h.Set("Cache-Control", cacheControl)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.CopyN(w, body, n)
	}
}

func (c *Cache) notFound(w http.ResponseWriter, r *http.Request) {
	c.errs(w, r, http.StatusNotFound, "image_not_found", "image not found")
}

func (c *Cache) unavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	c.errs(w, r, http.StatusServiceUnavailable, "image_unavailable", "image unavailable")
}
