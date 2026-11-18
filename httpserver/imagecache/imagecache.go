package imagecache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"golang.org/x/sync/semaphore"
)

// Key names one upstream image, built from the app's stored ids only. Each
// part is at most 128 bytes; Serve answers image_not_found for a longer one.
type Key struct{ Space, ID, File string }

// Image is what the upstream answered: its declared media type and bytes.
type Image struct {
	Type string
	Data []byte
}

// Fetch is the app's upstream call for k. found false with a nil error means
// the upstream has no such image; img is then ignored. A non-nil error wins
// over found.
type Fetch func(ctx context.Context, k Key) (img Image, found bool, err error)

// Options configures New. A zero numeric field takes its default; any other
// value outside its range makes New fail.
type Options struct {
	// Root is the cache directory; the Cache owns every 64-hex-digit file
	// name in it, one Cache per directory. The caller closes it after
	// Shutdown.
	Root  *os.Root
	Fetch Fetch
	// Errors writes Serve's error answers; nil means httpserver.WriteError.
	Errors httpserver.ErrorResponder
	// Logger nil means slog.Default() at the time of each line.
	Logger *slog.Logger
	// MaxImage is the largest image kept: 64 KiB to 16 MiB, default 2 MiB.
	MaxImage int64
	// MaxBytes bounds the cache's files on disk: 1 MiB to 4 GiB, default
	// 256 MiB.
	MaxBytes int64
	// Refresh is the age past which a kept image is refetched in the
	// background: 1 hour to 30 days, default 7 days.
	Refresh time.Duration
	// Fetches bounds the Fetch calls in flight across the cache: 1 to 16,
	// default 4.
	Fetches int
}

const (
	kib = 1 << 10
	mib = 1 << 20
	gib = 1 << 30
	day = 24 * time.Hour
)

type limit[T ~int | ~int64] struct {
	lo, hi, dflt T
	span         string
}

var (
	maxImageLimit = limit[int64]{lo: 64 * kib, hi: 16 * mib, dflt: 2 * mib, span: "64 KiB to 16 MiB"}
	maxBytesLimit = limit[int64]{lo: 1 * mib, hi: 4 * gib, dflt: 256 * mib, span: "1 MiB to 4 GiB"}
	refreshLimit  = limit[time.Duration]{lo: time.Hour, hi: 30 * day, dflt: 7 * day, span: "1 hour to 30 days"}
	fetchesLimit  = limit[int]{lo: 1, hi: 16, dflt: 4, span: "1 to 16"}
)

func resolve[T ~int | ~int64](field string, v T, l limit[T]) (T, error) {
	if v == 0 {
		return l.dflt, nil
	}
	if v < l.lo || v > l.hi {
		return 0, fmt.Errorf("imagecache: %s %v is outside %s", field, v, l.span)
	}
	return v, nil
}

// Cache serves upstream images from a bounded on-disk cache. Its methods are
// safe for concurrent use.
type Cache struct {
	root    *os.Root
	fetch   Fetch
	errs    httpserver.ErrorResponder
	logger  *slog.Logger
	sem     *semaphore.Weighted
	index   map[string]*entry
	order   *list.List
	flights map[string]*flight
	live    map[*flight]struct{}
	// orphans holds the cache files on disk that the index does not vouch
	// for: one the start or Serve could not read, or one whose removal
	// failed. Serve never answers one and ForgetSpace removes them all. A
	// name leaves when a removal or a new write of it succeeds.
	orphans  map[string]struct{}
	wg       sync.WaitGroup
	maxImage int64
	maxBytes int64
	total    int64
	refresh  time.Duration
	// writeMu serializes every write and removal of a cache file, so a
	// Forget and a fetch landing for the same name cannot interleave. It is
	// taken before mu.
	writeMu sync.Mutex
	mu      sync.Mutex
	closed  bool
}

// New returns a Cache over o.Root, keeping the images a previous Cache left
// there (see the package doc). It fails on a nil Root or Fetch, an option
// outside its range, or a root it cannot list.
func New(o Options) (*Cache, error) {
	if o.Root == nil {
		return nil, errors.New("imagecache: Options.Root is nil")
	}
	if o.Fetch == nil {
		return nil, errors.New("imagecache: Options.Fetch is nil")
	}
	maxImage, err1 := resolve("MaxImage", o.MaxImage, maxImageLimit)
	maxBytes, err2 := resolve("MaxBytes", o.MaxBytes, maxBytesLimit)
	refresh, err3 := resolve("Refresh", o.Refresh, refreshLimit)
	fetches, err4 := resolve("Fetches", o.Fetches, fetchesLimit)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return nil, err
	}
	c := &Cache{
		root:     o.Root,
		fetch:    o.Fetch,
		errs:     o.Errors,
		logger:   o.Logger,
		sem:      semaphore.NewWeighted(int64(fetches)),
		index:    map[string]*entry{},
		order:    list.New(),
		flights:  map[string]*flight{},
		live:     map[*flight]struct{}{},
		orphans:  map[string]struct{}{},
		maxImage: maxImage,
		maxBytes: maxBytes,
		refresh:  refresh,
	}
	if c.errs == nil {
		c.errs = httpserver.WriteError
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Cache) log() *slog.Logger {
	if c.logger != nil {
		return c.logger
	}
	return slog.Default()
}

var codeSpecs = map[httpserver.ErrorCode]httpserver.CodeSpec{
	"image_not_found":   {Status: http.StatusNotFound, NoEffect: true, Remediation: "Show the item without its image; the upstream has none."},
	"image_unavailable": {Status: http.StatusServiceUnavailable, NoEffect: true, Remediation: "Wait for the Retry-After delay, then retry."},
}

// CodeSpecs returns the codes Serve answers, with their status and
// remediation, for the app's own code table. The map is the caller's copy.
func CodeSpecs() map[httpserver.ErrorCode]httpserver.CodeSpec { return maps.Clone(codeSpecs) }
