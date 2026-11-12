package httpserver

import (
	"context"
	"log/slog"
	"slices"
	"sync"
)

// maxLogAttrs bounds the attributes AddLogAttrs adds to one access line, and
// the members of each group among them.
const maxLogAttrs = 8

// maxLogAttrValue bounds each added string value: a handler often logs a value
// the client chose, such as a key's label.
const maxLogAttrValue = 256

// reservedLogKeys are the access line's own attribute keys, which an added
// attribute may not repeat.
var reservedLogKeys = []string{"method", "path", "status", "duration_ms", "request_id", "client_ip"}

type logAttrsKey struct{}

// logAttrs holds one request's added attributes until its access line takes
// them.
type logAttrs struct {
	attrs []slog.Attr
	mu    sync.Mutex
}

// AddLogAttrs adds attrs to the access line Logging writes for the request ctx
// belongs to, after the line's own attributes, for what only a handler knows,
// such as the credential a request authenticated with. It is safe from any
// goroutine and does nothing without Logging, for a skipped request, or once
// the line is written. It drops an attribute with an empty key or one of the
// line's own keys, and any past the eighth. Each value is resolved when added:
// a string, or another value written as text, is cut at 256 bytes like an
// over-long path, and a group keeps its first eight members.
func AddLogAttrs(ctx context.Context, attrs ...slog.Attr) {
	h, ok := ctx.Value(logAttrsKey{}).(*logAttrs)
	if !ok {
		return
	}
	bounded := make([]slog.Attr, 0, min(len(attrs), maxLogAttrs))
	for _, a := range attrs {
		if a.Key == "" || slices.Contains(reservedLogKeys, a.Key) {
			continue
		}
		if len(bounded) == maxLogAttrs {
			break
		}
		bounded = append(bounded, boundLogAttr(a))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attrs = append(h.attrs, bounded[:min(len(bounded), maxLogAttrs-len(h.attrs))]...)
}

// take returns h's attributes as slog.Logger.Log arguments.
func (h *logAttrs) take() []any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]any, len(h.attrs))
	for i, a := range h.attrs {
		out[i] = a
	}
	return out
}

// boundLogAttr resolves a's value and bounds it. Resolving runs a LogValuer
// now, outside the holder's lock, and slog recovers a panicking one.
func boundLogAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString, slog.KindAny:
		return slog.String(a.Key, boundLoggedPath(v.String(), maxLogAttrValue))
	case slog.KindGroup:
		members := v.Group()
		out := make([]slog.Attr, 0, min(len(members), maxLogAttrs))
		for _, m := range members[:min(len(members), maxLogAttrs)] {
			out = append(out, boundLogAttr(m))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}
