package httpcache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	// defaultCacheBytes is the bound a zero Config.MaxBytes takes.
	defaultCacheBytes = 32 << 20

	// entryOverhead is the fixed cost charged to every entry beside its bytes:
	// the list element, the map slot and the entry's own fields.
	entryOverhead = 512

	// maxValidatorBytes bounds a validator the cache replays, so a hostile server
	// cannot make every request carry an unbounded header.
	maxValidatorBytes = 1 << 10

	// entryShare is the fraction of the bound one entry may take: a larger one is
	// not kept, so one body cannot flush every other entry.
	entryShare = 4

	// maxNotModifiedBytes bounds the body of a 304, which carries no content (RFC
	// 9110 section 15.4.5), so only a misbehaving server reaches it.
	maxNotModifiedBytes = 8 << 20
)

// Config configures a [Cache] or a [RoundTripper]. The zero value is
// ready to use: 32 MiB, no held fields, and Saved counts nothing.
type Config struct {
	// Held are the response header fields the owner reads from a served answer.
	// A name is matched canonically; a name given twice is held once, required
	// if either entry is.
	Held []HeldField
	// MaxBytes bounds what the cache holds, evicting the least recently used
	// entry first. Zero means 32 MiB. A negative value holds nothing and adds no
	// validator, though a 304 is still sent once more without one.
	MaxBytes int64
	// FreeNotModified says the server bills nothing for a 304 answering a
	// conditional request that carries an Authorization header, as GitHub's REST
	// API does. It decides only what State.Saved counts.
	FreeNotModified bool
}

// HeldField is one response header field a served answer carries from the held
// copy. A field the 304 carries takes the 304's lines; one it omits keeps the
// held lines, which the 304 did not confirm. Where Required is set and the 304
// omits a field the entry holds, the held copy is not served and the read is
// sent once more without a validator.
type HeldField struct {
	Name     string
	Required bool
}

// State is what a cache has done since it was built, and what it holds now.
type State struct {
	Hits   int   // reads a held answer served after a 304
	Misses int   // every other stored read the server answered in full
	Saved  int   // served 304s on a server that bills none (FreeNotModified)
	Bytes  int64 // what the cache holds now
}

// Cache is a byte-bounded, in-memory revalidation cache for an owner that sends,
// reads and decodes its own answers through [Cache.Begin]. It is safe for
// concurrent use. A held answer is served only where the server answers its
// validator with 304 Not Modified in the same exchange; nothing is served on age.
// The zero Cache holds nothing and every read passes through it; build one with
// New to hold answers.
type Cache struct {
	index      map[string]*list.Element
	order      *list.List
	held       heldTable
	max        int64
	bytes      int64
	gen        uint64
	hits       atomic.Int64
	misses     atomic.Int64
	saved      atomic.Int64
	mu         sync.Mutex
	freeNotMod bool
}

// New builds a [Cache] from cfg.
func New(cfg Config) *Cache {
	maxBytes := cfg.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultCacheBytes
	}
	return &Cache{
		index: map[string]*list.Element{}, order: list.New(), max: max(maxBytes, 0),
		held: newHeldTable(cfg.Held), freeNotMod: cfg.FreeNotModified,
	}
}

// on reports whether the cache holds anything at all.
func (c *Cache) on() bool { return c.max > 0 }

// State is what the cache has done since it was built, and what it holds now.
func (c *Cache) State() State {
	c.mu.Lock()
	held := c.bytes
	c.mu.Unlock()
	return State{
		Hits:   int(c.hits.Load()),
		Misses: int(c.misses.Load()),
		Saved:  int(c.saved.Load()),
		Bytes:  held,
	}
}

// Clear empties the cache; a read begun before it keeps nothing and its 304 is
// not served.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.index = map[string]*list.Element{}
	if c.order != nil { // the zero Cache has no list and holds nothing
		c.order.Init()
	}
	c.bytes = 0
}

// cacheEntry is one kept entry with what it is charged.
type cacheEntry struct {
	page *pageEntry
	key  string
	cost int64
}

// heldField is a [HeldField] under its canonical name.
type heldField struct {
	name     string
	required bool
}

// heldTable is the only header fields a cache's entries keep. Every other field
// of a served answer, the rate headers included, is the live 304's own.
type heldTable []heldField

func newHeldTable(fields []HeldField) heldTable {
	t := heldTable{}
	for _, f := range fields {
		if f.Name == "" {
			continue
		}
		name := strings.Clone(http.CanonicalHeaderKey(f.Name))
		if i := slices.IndexFunc(t, func(h heldField) bool { return h.name == name }); i >= 0 {
			t[i].required = t[i].required || f.Required
			continue
		}
		t = append(t, heldField{name: name, required: f.Required})
	}
	return t
}

// pageEntry is one revalidatable answer: the body its owner keeps, the lines of
// its held fields in its cache's [heldTable] order, and the validator the server
// sent. Every string is its own copy, so [pageCost] charges all the entry holds.
type pageEntry struct {
	ETag         string
	LastModified string
	Fields       [][]string
	Body         []byte
}

// hold copies each held field's lines into storage of their own, nil for a field
// with no text. The lines stay apart: a field such as Set-Cookie cannot be
// combined into one (RFC 9110 section 5.3).
func (t heldTable) hold(h http.Header) [][]string {
	out := make([][]string, len(t))
	for i, f := range t {
		lines := h.Values(f.name)
		if !hasText(lines) {
			continue
		}
		out[i] = make([]string, len(lines))
		for j, line := range lines {
			out[i][j] = strings.Clone(line)
		}
	}
	return out
}

// refreshed is the entry a 304 leaves: each held field it carries replaced (RFC
// 9111 section 4.3.4), the body and validators unchanged. A kept entry is never
// changed, since a reader holds it past the lock.
func (p *pageEntry) refreshed(t heldTable, h http.Header) *pageEntry {
	fresh := t.hold(h)
	out := *p
	out.Fields = slices.Clone(p.Fields)
	for i, f := range t {
		if len(h.Values(f.name)) > 0 {
			out.Fields[i] = fresh[i]
		}
	}
	return &out
}

// served is the header a served answer carries: the live 304's, and each held
// field it omits.
func (p *pageEntry) served(t heldTable, live http.Header) http.Header {
	out := live.Clone()
	if out == nil {
		out = http.Header{}
	}
	for i, f := range t {
		if len(out.Values(f.name)) == 0 && len(p.Fields[i]) > 0 {
			out[f.name] = slices.Clone(p.Fields[i])
		}
	}
	return out
}

// answers reports whether a 304 may serve this entry. Its validators must name
// the held representation: its ETag weakly equal to the held one (RFC 9110
// section 8.8.3.2), or none well-formed where none is held, and its
// Last-Modified the held date (RFC 9111 section 4.3.4). It must also carry every
// required held field the entry holds.
func (p *pageEntry) answers(t heldTable, h http.Header) bool {
	for i, f := range t {
		if f.required && len(p.Fields[i]) > 0 && len(h.Values(f.name)) == 0 {
			return false
		}
	}
	tag := entityTag(h)
	switch {
	case p.ETag == "" && tag != "":
		return false
	case p.ETag != "" && len(h.Values("ETag")) > 0 && strings.TrimPrefix(tag, "W/") != strings.TrimPrefix(p.ETag, "W/"):
		return false
	}
	modified := h.Get("Last-Modified")
	if modified == "" || p.LastModified == "" {
		return true
	}
	fresh, err := http.ParseTime(modified)
	held, heldErr := http.ParseTime(p.LastModified)
	return err == nil && heldErr == nil && fresh.Equal(held)
}

// credential is r's Authorization lines under every name net/http sends them by,
// in sorted-name order: the request's credential, which [Cache.key] folds only
// as its digest.
func credential(r *http.Request) string {
	var lines []string
	for _, name := range slices.Sorted(maps.Keys(r.Header)) {
		if http.CanonicalHeaderKey(name) == "Authorization" {
			lines = append(lines, r.Header[name]...)
		}
	}
	return strings.Join(lines, "\n")
}

// key is one GET's key: the credential folded as its digest, never as itself, the
// variant, and everything of r that reaches the wire but the validators and the
// credential's own lines: its effective host, its URL and each header line under
// the name it is sent by. [Guard] holds the request that reaches the wire to
// the one keyed here.
func (*Cache) key(cred, variant string, r *http.Request) string {
	fields := make([]string, 0, 5+2*len(r.Header))
	fields = append(fields, digest([]byte(cred)), http.MethodGet, effectiveHost(r), r.URL.String(), variant)
	names := make([]string, 0, len(r.Header))
	for name := range r.Header {
		if isValidator(name) || http.CanonicalHeaderKey(name) == "Authorization" {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		fields = append(fields, name, strings.Join(r.Header[name], "\n"))
	}
	return digest([]byte(lengthPrefixed(fields...)))
}

func lengthPrefixed(fields ...string) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return b.String()
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// generation is the generation an exchange beginning now belongs to.
func (c *Cache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// current reports whether gen is the generation the cache holds entries for.
func (c *Cache) current(gen uint64) bool {
	return c.generation() == gen
}

// lookup is the entry kept under key, nil where none is. Recency is set where the
// exchange ends: [Cache.finish] puts a kept or served entry at the front.
func (c *Cache) lookup(key string) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[key]
	if !ok {
		return nil
	}
	e, _ := el.Value.(*cacheEntry)
	return e
}

// finish is the one change a stored exchange makes to the cache: it keeps the
// entry the exchange vouched for, and forgets the key on every other outcome. An
// exchange of an ended generation changes nothing.
func (c *Cache) finish(key string, gen uint64, kept *pageEntry) {
	var e *cacheEntry
	if kept != nil {
		e = &cacheEntry{key: key, page: kept, cost: pageCost(key, kept)}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	if e == nil {
		c.removeLocked(key)
		return
	}
	c.putLocked(e)
}

// tally counts one stored read the server answered in full: a hit where the held
// answer served it, a miss on every other outcome.
func (c *Cache) tally(hit bool) {
	if hit {
		c.hits.Add(1)
		return
	}
	c.misses.Add(1)
}

// putLocked holds e, evicting from the back until the bound holds. An entry over
// its share of the bound is not held, and the key's older entry is dropped with
// it rather than left to answer.
func (c *Cache) putLocked(e *cacheEntry) {
	c.removeLocked(e.key)
	if e.cost > c.max/entryShare {
		return
	}
	c.index[e.key] = c.order.PushFront(e)
	c.bytes += e.cost
	for c.bytes > c.max {
		victim, _ := c.order.Back().Value.(*cacheEntry)
		c.removeLocked(victim.key)
	}
}

func (c *Cache) removeLocked(key string) {
	el, ok := c.index[key]
	if !ok {
		return
	}
	e, _ := el.Value.(*cacheEntry)
	c.order.Remove(el)
	delete(c.index, key)
	c.bytes -= e.cost
}

// The sizes of the descriptors a held field's storage is made of.
var (
	linesSize  = int(reflect.TypeFor[[]string]().Size())
	stringSize = int(reflect.TypeFor[string]().Size())
)

// pageCost is what an entry is charged: its key, its body's backing array, its
// validators, its held fields' slots, line descriptors and bytes, and the fixed
// overhead.
func pageCost(key string, p *pageEntry) int64 {
	n := len(key) + cap(p.Body) + len(p.ETag) + len(p.LastModified) + entryOverhead + cap(p.Fields)*linesSize
	for _, lines := range p.Fields {
		n += cap(lines) * stringSize
		for _, line := range lines {
			n += len(line)
		}
	}
	return int64(n)
}

// entityTag is a response's ETag as If-None-Match replays it, empty unless it is
// exactly one entity-tag (RFC 9110 section 8.8.3): `*` or a list replayed would
// be a condition matching other representations.
func entityTag(h http.Header) string {
	tags := h.Values("ETag")
	if len(tags) != 1 || len(tags[0]) > maxValidatorBytes {
		return ""
	}
	opaque := strings.TrimPrefix(tags[0], "W/")
	if len(opaque) < 2 || opaque[0] != '"' || opaque[len(opaque)-1] != '"' {
		return ""
	}
	for i := 1; i < len(opaque)-1; i++ {
		if b := opaque[i]; b < 0x21 || b == '"' || b == 0x7f {
			return ""
		}
	}
	return tags[0]
}

// httpDate is a response's Last-Modified as If-Modified-Since replays it, empty
// unless it is one HTTP date (RFC 9110 section 8.8.2).
func httpDate(h http.Header) string {
	dates := h.Values("Last-Modified")
	if len(dates) != 1 || len(dates[0]) > maxValidatorBytes || !validFieldValue(dates[0]) {
		return ""
	}
	if _, err := http.ParseTime(dates[0]); err != nil {
		return ""
	}
	return dates[0]
}

// validFieldValue reports whether value has no byte a header field value may not
// carry (RFC 9110 section 5.5).
func validFieldValue(value string) bool {
	for i := range len(value) {
		if b := value[i]; b < 0x20 && b != '\t' || b == 0x7f {
			return false
		}
	}
	return true
}

// noStore reports whether a request or a response carries the no-store
// directive, which forbids keeping any part of the exchange (RFC 9111 sections
// 5.2.1.5 and 5.2.2.5).
func noStore(h http.Header) bool {
	for _, value := range h.Values("Cache-Control") {
		for directive := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(directive), "no-store") {
				return true
			}
		}
	}
	return false
}

// isValidator reports whether name is If-None-Match or If-Modified-Since, however
// it is cased.
func isValidator(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "If-None-Match", "If-Modified-Since":
		return true
	}
	return false
}

// carriesValidator reports whether h has a validator line with text under any
// name net/http sends it by; an empty line names nothing (RFC 9110 section 5.6.1).
func carriesValidator(h http.Header) bool {
	for name, lines := range h {
		if isValidator(name) && hasText(lines) {
			return true
		}
	}
	return false
}

// hasText reports whether any of a field's lines is not empty.
func hasText(lines []string) bool {
	return slices.ContainsFunc(lines, func(line string) bool { return line != "" })
}
