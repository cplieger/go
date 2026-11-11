package sse

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"time"

	"github.com/cplieger/go/httpserver"
)

// The scope limits a client must agree with, so they are fixed rather than
// options: the TypeScript half ships the same values, pinned by a test in each
// language.
const (
	// MaxScopeTopics bounds the topics one client-supplied scope may name.
	MaxScopeTopics = 256
	// MaxScopeHeaderBytes bounds the SSE-Topics request header; a client whose
	// scope encodes longer connects with no header and narrows by POST.
	MaxScopeHeaderBytes = 4096
	// maxTopicBytes bounds one topic in a client-supplied scope.
	maxTopicBytes = 128
	// maxScopeBody bounds a ScopeHandler request body: MaxScopeTopics topics of
	// maxTopicBytes, quoted and comma-separated, plus the envelope, fit with
	// room to spare, since the grammar admits nothing JSON escapes.
	maxScopeBody = 64 << 10
	scopeHeader  = "SSE-Topics"
	// scopeMarkInterval is how often a scoped connection may write a position
	// mark; at a 1,024-entry ring and about 174 frames a second the ring spans
	// about 6 s, so one mark a second keeps a narrow cursor inside it.
	scopeMarkInterval = time.Second
	connBytes         = 16
)

// Scope is the set of topics a connection receives beside broadcasts, the
// events published with an empty Topic, which every connection receives. The
// zero value is everything.
type Scope struct {
	topics map[string]struct{}
	scoped bool
}

// All is the scope that receives every event, the zero Scope.
func All() Scope { return Scope{} }

// Topics is the scope that receives broadcasts plus events on any of topics;
// with no topics it receives broadcasts only. It copies its arguments and
// drops "", which names no topic a scope could add.
func Topics(topics ...string) Scope {
	s := Scope{scoped: true, topics: make(map[string]struct{}, len(topics))}
	for _, t := range topics {
		if t != "" {
			s.topics[t] = struct{}{}
		}
	}
	return s
}

// topicScope is WithTopic's scope: "" is everything, any other topic itself.
func topicScope(topic string) Scope {
	if topic == "" {
		return All()
	}
	return Topics(topic)
}

// matches reports whether a connection in this scope receives an event
// published on topic.
func (s Scope) matches(topic string) bool {
	if topic == "" || !s.scoped {
		return true
	}
	_, ok := s.topics[topic]
	return ok
}

// ValidTopic reports whether topic may appear in a scope a client supplies: 1
// to 128 bytes of printable ASCII (0x21 to 0x7E) other than '"' and '\'. The
// grammar keeps the SSE-Topics header byte-identical to its JSON form, since
// fetch refuses a header byte above 255 and JSON escapes nothing it admits.
// WithTopic and WithScope accept any topic: a set the server chooses is never
// encoded.
func ValidTopic(topic string) bool {
	if topic == "" || len(topic) > maxTopicBytes {
		return false
	}
	for i := range len(topic) {
		if b := topic[i]; b < 0x21 || b > 0x7e || b == '"' || b == '\\' {
			return false
		}
	}
	return true
}

var errScope = errors.New("sse: invalid scope")

// parseScope decodes a client-supplied scope, JSON null (everything) or an
// array of topics, applying ValidTopic and MaxScopeTopics.
func parseScope(raw []byte) (Scope, error) {
	var topics []string
	if err := json.Unmarshal(raw, &topics); err != nil {
		return Scope{}, fmt.Errorf("%w: not null or an array of strings", errScope)
	}
	if topics == nil {
		return All(), nil
	}
	if len(topics) > MaxScopeTopics {
		return Scope{}, fmt.Errorf("%w: %d topics, max %d", errScope, len(topics), MaxScopeTopics)
	}
	for _, t := range topics {
		if !ValidTopic(t) {
			return Scope{}, fmt.Errorf("%w: a topic of %d bytes is outside the grammar", errScope, len(t))
		}
	}
	return Topics(topics...), nil
}

// requestScope reads the SSE-Topics header: absent is everything.
func requestScope(r *http.Request) (Scope, error) {
	values := r.Header.Values(scopeHeader)
	switch len(values) {
	case 0:
		return All(), nil
	case 1:
	default:
		return Scope{}, fmt.Errorf("%w: %d %s headers", errScope, len(values), scopeHeader)
	}
	if len(values[0]) > MaxScopeHeaderBytes {
		return Scope{}, fmt.Errorf("%w: header of %d bytes, max %d", errScope, len(values[0]), MaxScopeHeaderBytes)
	}
	return parseScope([]byte(values[0]))
}

// newConn mints a connection's scope capability: 16 bytes from crypto/rand,
// 22 characters of unpadded base64url, the shape the client validates.
func newConn() string {
	var b [connBytes]byte
	_, _ = rand.Read(b[:]) // never returns an error; a failed read crashes the program
	return base64.RawURLEncoding.EncodeToString(b[:])
}

type scopeRequest struct {
	Conn   string          `json:"conn"`
	Topics json.RawMessage `json:"topics"`
	Seq    int64           `json:"seq"`
}

type scopeResponse struct {
	Epoch  string `json:"epoch"`
	Offset uint64 `json:"offset,string"`
}

// ScopeHandler answers POST application/json {"conn","seq","topics"}, which
// replaces the scope of a live WithRequestScope connection, with 200
// {"epoch","offset"}: offset is the hub head at the swap, the last offset
// matched against the old scope. A seq at or below the last applied answers 409.
// authorize receives the owner WithRequestScope recorded and judges ownership,
// never topics (403). Mount it inside the application's authentication and
// cross-origin middleware. A nil authorize returns an error wrapping ErrConfig.
func (h *Hub) ScopeHandler(authorize func(r *http.Request, owner any) error) (http.Handler, error) {
	if authorize == nil {
		return nil, fmt.Errorf("%w: ScopeHandler needs an authorize function", ErrConfig)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, s, ok := h.decodeScopeRequest(w, r)
		if !ok {
			return
		}
		owner, ok := h.connOwner(req.Conn)
		if !ok {
			httpserver.WriteError(w, r, http.StatusNotFound, "scope_conn", "no live connection")
			return
		}
		if authErr := authorize(r, owner); authErr != nil {
			h.logger.Debug("sse: scope caller refused", "error", authErr)
			httpserver.WriteError(w, r, http.StatusForbidden, "scope_forbidden", "not this connection's owner")
			return
		}
		offset, err := h.setScopeSeq(req.Conn, uint64(req.Seq), s) //nolint:gosec // G115: decodeScopeRequest refuses a seq at or below zero
		switch {
		case errors.Is(err, errUnknownConn):
			httpserver.WriteError(w, r, http.StatusNotFound, "scope_conn", "no live connection")
		case errors.Is(err, errStaleSeq):
			httpserver.WriteError(w, r, http.StatusConflict, "scope_stale", "a later request already applied")
		default:
			httpserver.WriteJSON(w, scopeResponse{Epoch: h.epoch, Offset: offset})
		}
	}), nil
}

// decodeScopeRequest checks the method, the media type and the body, and
// answers every refusal itself; ok is false once it has.
func (h *Hub) decodeScopeRequest(w http.ResponseWriter, r *http.Request) (req scopeRequest, s Scope, ok bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpserver.WriteError(w, r, http.StatusMethodNotAllowed, "scope_method", "POST required")
		return req, s, false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		httpserver.WriteError(w, r, http.StatusUnsupportedMediaType, "scope_content_type", "Content-Type must be application/json")
		return req, s, false
	}
	if err := httpserver.DecodeJSONInto(w, r, &req, maxScopeBody); err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			httpserver.WriteError(w, r, http.StatusRequestEntityTooLarge, "scope_too_large", fmt.Sprintf("body exceeds %d bytes", maxScopeBody))
			return req, s, false
		}
		httpserver.WriteError(w, r, http.StatusBadRequest, "scope_invalid", "malformed JSON body")
		return req, s, false
	}
	// An absent topics field decodes to nil, which is not JSON null.
	if req.Conn == "" || req.Seq <= 0 || req.Topics == nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "scope_invalid", "conn, a positive seq and topics are required")
		return req, s, false
	}
	s, err := parseScope(req.Topics)
	if err != nil {
		h.logger.Debug("sse: scope refused", "error", err)
		httpserver.WriteError(w, r, http.StatusBadRequest, "sse_scope", "scope outside the topic grammar or bound")
		return req, s, false
	}
	return req, s, true
}

var (
	errUnknownConn = errors.New("sse: no live connection")
	errStaleSeq    = errors.New("sse: stale scope request")
)

// connOwner returns the owner of a live rescopable connection.
func (h *Hub) connOwner(conn string) (any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.byConn[conn]
	if !ok || h.closed {
		return nil, false
	}
	return c.owner, true
}

// setScopeSeq swaps the live scope of conn's connection under the lock
// Publish holds while it matches, so one head splits the stream exactly, and
// applies a request only when seq is above the last one applied: a request a
// client abandoned can still reach the handler after a later one.
func (h *Hub) setScopeSeq(conn string, seq uint64, s Scope) (uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.byConn[conn]
	if !ok || h.closed {
		return 0, errUnknownConn
	}
	if seq <= c.lastSeq {
		return 0, errStaleSeq
	}
	c.lastSeq = seq
	c.scope = s
	return h.seq, nil
}
