package imagecache

import (
	"bytes"
	"cmp"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/cplieger/go/filesafe"
)

const (
	maxPart = 128
	// headerCap bounds a file header: the magic, three length-prefixed parts
	// of at most maxPart bytes, the longest type, the time, the digest and
	// the data length come to 461 bytes.
	headerCap = 512
	// sniffLen is what http.DetectContentType reads.
	sniffLen = 512
)

var magic = []byte("ICACHE1\n")

var kept = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

var (
	errHeader  = errors.New("imagecache: malformed cache file")
	errNotKept = errors.New("imagecache: cache file fails a check")
)

type entry struct {
	fetched time.Time
	elem    *list.Element
	name    string
	etag    string
	key     Key
	size    int64
}

// header is the self-describing front of a cache file. The file layout is
// magic, the key encoding, uvarint-prefixed type, varint fetch time in
// Unix nanoseconds, the SHA-256 of the data, varint data length, then the
// data.
type header struct {
	fetched time.Time
	typ     string
	key     Key
	n       int64
	sum     [sha256.Size]byte
}

// appendKey encodes k with each part length-prefixed, so the encoding is
// prefix-free and two different keys never encode alike.
func appendKey(b []byte, k Key) []byte {
	for _, p := range [...]string{k.Space, k.ID, k.File} {
		b = binary.AppendUvarint(b, uint64(len(p)))
		b = append(b, p...)
	}
	return b
}

func fileName(k Key) string {
	sum := sha256.Sum256(appendKey(nil, k))
	return hex.EncodeToString(sum[:])
}

func etagOf(sum [sha256.Size]byte) string { return `"` + hex.EncodeToString(sum[:]) + `"` }

// longPart answers the first part of k over maxPart bytes, or "".
func longPart(k Key) (name string, n int) {
	for _, p := range [...]struct {
		name, v string
	}{{"Space", k.Space}, {"ID", k.ID}, {"File", k.File}} {
		if len(p.v) > maxPart {
			return p.name, len(p.v)
		}
	}
	return "", 0
}

func appendHeader(b []byte, h *header) []byte {
	b = append(b, magic...)
	b = appendKey(b, h.key)
	b = binary.AppendUvarint(b, uint64(len(h.typ)))
	b = append(b, h.typ...)
	b = binary.AppendVarint(b, h.fetched.UnixNano())
	b = append(b, h.sum[:]...)
	return binary.AppendVarint(b, h.n)
}

type cursor struct {
	b   []byte
	bad bool
}

func (c *cursor) take(n uint64) []byte {
	if c.bad || uint64(len(c.b)) < n {
		c.bad = true
		return nil
	}
	out := c.b[:n]
	c.b = c.b[n:]
	return out
}

func (c *cursor) uvarint() uint64 {
	if c.bad {
		return 0
	}
	v, w := binary.Uvarint(c.b)
	if w <= 0 {
		c.bad = true
		return 0
	}
	c.b = c.b[w:]
	return v
}

func (c *cursor) varint() int64 {
	if c.bad {
		return 0
	}
	v, w := binary.Varint(c.b)
	if w <= 0 {
		c.bad = true
		return 0
	}
	c.b = c.b[w:]
	return v
}

func (c *cursor) str() string {
	n := c.uvarint()
	if n > maxPart {
		c.bad = true
	}
	return string(c.take(n))
}

// parseHeader decodes the header at the front of b, the leading bytes of a
// file of size bytes, and answers it with its own length. It refuses a
// header whose data length does not end exactly at size.
func parseHeader(b []byte, size int64) (header, int, error) {
	var h header
	if !bytes.HasPrefix(b, magic) {
		return h, 0, errHeader
	}
	cur := cursor{b: b[len(magic):]}
	h.key = Key{Space: cur.str(), ID: cur.str(), File: cur.str()}
	h.typ = cur.str()
	ns := cur.varint()
	sum := cur.take(sha256.Size)
	h.n = cur.varint()
	hlen := len(b) - len(cur.b)
	if cur.bad || !kept[h.typ] || h.n != size-int64(hlen) {
		return header{}, 0, errHeader
	}
	h.fetched = time.Unix(0, ns)
	copy(h.sum[:], sum)
	return h, hlen, nil
}

// load rebuilds the index from the files a previous Cache left in the root.
func (c *Cache) load() error {
	res, err := filesafe.CleanupStaleTempsInRoot(context.Background(), c.root, time.Minute)
	if err != nil {
		return fmt.Errorf("imagecache: sweep temps: %w", err)
	}
	if res.Failed > 0 {
		c.log().Warn("imagecache: stale temps not removed", "failed", res.Failed)
	}
	dir, err := c.root.Open(".")
	if err != nil {
		return fmt.Errorf("imagecache: open root: %w", err)
	}
	ents, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return fmt.Errorf("imagecache: list root: %w", err)
	}
	found := c.scan(ents)
	slices.SortFunc(found, func(a, b *entry) int {
		return cmp.Or(a.fetched.Compare(b.fetched), cmp.Compare(a.name, b.name))
	})
	for _, e := range found {
		e.elem = c.order.PushBack(e)
		c.index[e.name] = e
		c.total += e.size
	}
	c.dropFiles(c.evictLocked(nil))
	return nil
}

// scan answers the entries of the cache files in ents it can keep and
// removes those that fail a check. A file it cannot read becomes an orphan,
// never removed: a transient I/O error must not delete a valid image.
func (c *Cache) scan(ents []fs.DirEntry) []*entry {
	var found []*entry
	var bad []string
	for _, de := range ents {
		if !de.Type().IsRegular() || !isCacheName(de.Name()) {
			continue
		}
		e, err := c.loadFile(de.Name())
		switch {
		case errors.Is(err, errNotKept):
			bad = append(bad, de.Name())
		case err != nil:
			c.log().Warn("imagecache: cache file not read", "file", de.Name(), "err", err)
			c.mu.Lock()
			c.orphans[de.Name()] = struct{}{}
			c.mu.Unlock()
		default:
			found = append(found, e)
		}
	}
	if len(bad) > 0 {
		c.dropFiles(bad)
		c.log().Warn("imagecache: cache files it cannot keep", "count", len(bad))
	}
	return found
}

func isCacheName(name string) bool {
	if len(name) != 2*sha256.Size {
		return false
	}
	for _, r := range name {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// loadFile answers name's entry when the file holds an image Serve would
// have kept under the current options, errNotKept when it fails a check,
// and the I/O error when it cannot be read.
func (c *Cache) loadFile(name string) (*entry, error) {
	f, err := c.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	buf := make([]byte, min(st.Size(), headerCap+sniffLen))
	if _, err = io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	h, hlen, err := parseHeader(buf, st.Size())
	if err != nil || fileName(h.key) != name || h.n > c.maxImage || h.n < 1 ||
		http.DetectContentType(buf[hlen:]) != h.typ {
		return nil, errNotKept
	}
	return &entry{key: h.key, name: name, etag: etagOf(h.sum), fetched: h.fetched, size: st.Size()}, nil
}

// store writes a freshly fetched image and indexes it, unless fl was
// forgotten or the cache shut down while it ran.
func (c *Cache) store(fl *flight, img Image, at time.Time) {
	h := header{key: fl.key, typ: img.Type, fetched: at, n: int64(len(img.Data)), sum: sha256.Sum256(img.Data)}
	file := append(appendHeader(make([]byte, 0, headerCap+len(img.Data)), &h), img.Data...)
	size := int64(len(file))
	if size > c.maxBytes {
		c.log().Warn("imagecache: image larger than MaxBytes not kept",
			"space", fl.key.Space, "file", fl.key.File, "size", len(img.Data))
		return
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	skip := fl.forgotten || c.closed
	c.mu.Unlock()
	if skip {
		return
	}
	if _, err := filesafe.WriteFileInRoot(context.Background(), c.root, fl.name, file); err != nil {
		c.log().Warn("imagecache: write failed", "space", fl.key.Space, "file", fl.key.File, "err", err)
		return
	}
	e := &entry{key: fl.key, name: fl.name, etag: etagOf(h.sum), fetched: at, size: size}
	c.mu.Lock()
	victims := c.evictLocked(c.indexLocked(e))
	c.mu.Unlock()
	c.dropFiles(victims)
}

// indexLocked adds or replaces e as the newest fetched entry.
func (c *Cache) indexLocked(e *entry) *entry {
	if old := c.index[e.name]; old != nil {
		c.unindexLocked(old)
	}
	delete(c.orphans, e.name)
	e.elem = c.order.PushBack(e)
	c.index[e.name] = e
	c.total += e.size
	return e
}

func (c *Cache) unindexLocked(e *entry) {
	c.order.Remove(e.elem)
	delete(c.index, e.name)
	c.total -= e.size
}

// evictLocked unindexes the oldest fetched entries, never keep, until the
// total fits MaxBytes, and answers their file names for dropFiles.
func (c *Cache) evictLocked(keep *entry) []string {
	var names []string
	for c.total > c.maxBytes {
		front, _ := c.order.Front().Value.(*entry)
		if front == keep {
			break
		}
		c.unindexLocked(front)
		names = append(names, front.name)
	}
	return names
}

// removeFiles removes the named files, keeping each one whose removal fails
// in orphans so a ForgetSpace retries it, and joins the failures. It runs
// under writeMu, or alone in load.
func (c *Cache) removeFiles(names []string) error {
	var errs []error
	var failed, gone []string
	for _, name := range names {
		if err := c.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("imagecache: remove: %w", err))
			failed = append(failed, name)
			continue
		}
		gone = append(gone, name)
	}
	c.mu.Lock()
	for _, name := range failed {
		c.orphans[name] = struct{}{}
	}
	for _, name := range gone {
		delete(c.orphans, name)
	}
	c.mu.Unlock()
	return errors.Join(errs...)
}

// dropFiles is removeFiles for files the cache itself let go, whose
// failures only a log line reports.
func (c *Cache) dropFiles(names []string) {
	if err := c.removeFiles(names); err != nil {
		c.log().Warn("imagecache: remove failed", "err", err)
	}
}

// Forget drops k's kept image and any fetch of it in flight, so the next
// Serve of k fetches anew, and removes k's file whether or not it is kept.
// An error means the file may still be on disk, where a restart can serve
// it again; retry the call. A key never kept is not an error.
func (c *Cache) Forget(k Key) error {
	if part, _ := longPart(k); part != "" {
		return nil
	}
	name := fileName(k)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	if fl := c.flights[name]; fl != nil {
		c.forgetFlightLocked(fl)
	}
	if e := c.index[name]; e != nil {
		c.unindexLocked(e)
	}
	c.mu.Unlock()
	return c.removeFiles([]string{name})
}

// ForgetSpace is Forget for every key whose Space is space, joining each
// removal's error, with Forget's meaning. It also removes every orphan (see
// the package doc), since an orphan's space is unknown, and fails a space
// over 128 bytes once those removals are done.
func (c *Cache) ForgetSpace(space string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	names := maps.Clone(c.orphans)
	for name, fl := range c.flights {
		if fl.key.Space == space {
			c.forgetFlightLocked(fl)
			names[name] = struct{}{}
		}
	}
	for name, e := range c.index {
		if e.key.Space == space {
			c.unindexLocked(e)
			names[name] = struct{}{}
		}
	}
	c.mu.Unlock()
	err := c.removeFiles(slices.Sorted(maps.Keys(names)))
	if len(space) > maxPart {
		err = errors.Join(err, fmt.Errorf("imagecache: space of %d bytes is over 128", len(space)))
	}
	return err
}
