package imagecache_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2/imagecache"
)

// The leading bytes WHATWG mimesniff section 6.1 matches for each image
// type, which http.DetectContentType implements.
var signatures = map[string]string{
	"image/jpeg": "\xFF\xD8\xFF",
	"image/png":  "\x89PNG\r\n\x1a\n",
	"image/gif":  "GIF89a",
	"image/webp": "RIFF\x00\x00\x00\x00WEBPVP",
}

// imageOf returns n bytes that sniff as typ, filled after the signature
// with fill so two fixtures of one type differ.
func imageOf(typ string, n int, fill byte) []byte {
	b := bytes.Repeat([]byte{fill}, n)
	copy(b, signatures[typ])
	return b
}

func pngOf(n int, fill byte) imagecache.Image {
	return imagecache.Image{Type: "image/png", Data: imageOf("image/png", n, fill)}
}

const svgDoc = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`

var hexName = regexp.MustCompile(`^[0-9a-f]{64}$`)

type reply struct {
	err   error
	img   imagecache.Image
	found bool
}

// fakeFetch answers each key's reply, or found false for a key it has none
// for. With hold set, every call waits for a release or its ctx to end.
type fakeFetch struct {
	replies   map[imagecache.Key]reply
	hold      chan struct{}
	deadlines []time.Time
	ctxErrs   []error
	mu        sync.Mutex
	calls     atomic.Int64
	inFlight  int
	peak      int
}

func newFake() *fakeFetch { return &fakeFetch{replies: map[imagecache.Key]reply{}} }

func (f *fakeFetch) set(k imagecache.Key, r reply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[k] = r
}

func (f *fakeFetch) fetch(ctx context.Context, k imagecache.Key) (imagecache.Image, bool, error) {
	f.calls.Add(1)
	f.mu.Lock()
	dl, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, dl)
	f.inFlight++
	f.peak = max(f.peak, f.inFlight)
	hold := f.hold
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			f.mu.Lock()
			f.ctxErrs = append(f.ctxErrs, ctx.Err())
			f.mu.Unlock()
			return imagecache.Image{}, false, ctx.Err()
		}
	}
	f.mu.Lock()
	r := f.replies[k]
	f.mu.Unlock()
	return r.img, r.found, r.err
}

// holdCalls makes every later call wait for a release or its ctx to end,
// and answers the channel that releases them.
func (f *fakeFetch) holdCalls() chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hold = make(chan struct{})
	return f.hold
}

func (f *fakeFetch) seen() (deadlines []time.Time, ctxErrs []error, inFlight, peak int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deadlines), slices.Clone(f.ctxErrs), f.inFlight, f.peak
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

type lockedBuffer struct {
	b  bytes.Buffer
	mu sync.Mutex
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLogger() (*slog.Logger, *lockedBuffer) {
	buf := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

func keyN(i int) imagecache.Key {
	return imagecache.Key{Space: "sonarr-1", ID: strconv.Itoa(i), File: "poster-500.jpg"}
}

// keptFills answers the fill byte of each hex-named file in dir: every
// fixture ends in its fill byte, and a kept file ends in the image's bytes.
func keptFills(t *testing.T, dir string) map[byte]bool {
	t.Helper()
	out := map[byte]bool{}
	for _, name := range files(t, dir) {
		if !hexName.MatchString(name) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > 0 {
			out[b[len(b)-1]] = true
		}
	}
	return out
}

// newCache builds a Cache over a fresh directory with f as its Fetch, and
// shuts it down when the test ends.
func newCache(t *testing.T, o imagecache.Options, f *fakeFetch) (*imagecache.Cache, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	o.Root = root
	if o.Fetch == nil {
		o.Fetch = f.fetch
	}
	if o.Logger == nil {
		o.Logger = discardLogger()
	}
	c, err := imagecache.New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Shutdown(context.WithoutCancel(t.Context())) })
	return c, dir
}

// get builds a GET request; hdr holds header name and value pairs.
func get(ctx context.Context, hdr ...string) *http.Request {
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/img", http.NoBody)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Add(hdr[i], hdr[i+1])
	}
	return r
}

func serve(c *imagecache.Cache, r *http.Request, k imagecache.Key) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c.Serve(rec, r, k)
	return rec
}

// envelopeCode answers the code of rec's JSON error envelope, or "" when
// the body is not one.
func envelopeCode(rec *httptest.ResponseRecorder) string {
	var env struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &env) != nil {
		return ""
	}
	return env.Code
}

// files answers the names of the regular files in dir.
func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	return names
}

func body(rec *httptest.ResponseRecorder) []byte {
	b, _ := io.ReadAll(rec.Result().Body)
	return b
}
