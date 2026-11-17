package fixture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/sse/v2"
	"github.com/cplieger/go/sse/v2/feedwire"
)

const (
	// feedReplay keeps a session's ring small, so a drop without resume can
	// publish past it and the reconnect's hello answers gap_floor.
	feedReplay       = 64
	feedFiller       = feedReplay + 8
	maxFeedSessions  = 1024
	maxFeedSources   = 8
	maxFeedRows      = 1024
	feedConnWait     = 2 * time.Second
	feedFailReject   = "reject"
	feedFailUnavail  = "unavailable"
	feedAnswerRows   = "rows"
	feedAnswerFull   = "full"
	feedAnswerMaint  = "maintenance"
	feedAnswerRefuse = "bad_query"
	maxChurn         = time.Minute
	// newerKind is the kind a newer build's frames name on the first source.
	newerKind = "zz_newer_kind"
)

// feedRegistry holds the change-feed sessions: each one a hub, its sources and
// their rows, so tests in different engines never share feed state.
type feedRegistry struct {
	sessions map[string]*feedSession
	opts     []sse.Option
	mu       sync.Mutex
	next     int
}

type feedSource struct {
	rows    map[string]map[string]string // kind to key to value
	floors  map[string]uint64
	name    string
	epoch   string
	kinds   []string
	changes []feedChange
	start   uint64 // the log's first revision: a catch-up from below it is full
	rev     uint64
	logged  bool
	started bool
}

type feedChange struct {
	kind string
	key  string
	rev  uint64
}

type feedConn struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type changesQuery struct {
	Source string   `json:"source"`
	Epoch  string   `json:"epoch"`
	Answer string   `json:"answer"`
	Kinds  []string `json:"kinds"`
	Since  uint64   `json:"since"`
}

// feedSession is one test's feed server. A nil release means new streams join
// the hub at once; a non-nil one holds them until it is closed.
type feedSession struct {
	hub            *sse.Hub
	release        chan struct{}
	changesRelease chan struct{}
	conns          map[int]*feedConn
	reads          map[string]int
	wire           string
	readFailMode   string
	readFailKind   string
	hubOpts        []sse.Option
	sources        []*feedSource
	queries        []changesQuery
	mu             sync.Mutex
	nextConn       int
	connects       int
	changesHeld    int
	format         int
	changesFail    int
	readFail       int
	newer          bool
}

func newFeedRegistry(opts []sse.Option) *feedRegistry {
	return &feedRegistry{sessions: make(map[string]*feedSession), opts: opts}
}

func (fr *feedRegistry) mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /control/feed/sessions", fr.handleCreate)
	mux.HandleFunc("GET /feed/{id}/events", fr.with((*feedSession).serveEvents))
	mux.HandleFunc("GET /feed/{id}/read", fr.with((*feedSession).serveRead))
	mux.HandleFunc("GET /feed/{id}/changes", fr.with((*feedSession).serveChanges))
	mux.HandleFunc("GET /control/feed/{id}/state", fr.with((*feedSession).serveState))
	controls := map[string]func(*feedSession, http.ResponseWriter, *http.Request){
		"commit":       (*feedSession).handleCommit,
		"bump":         (*feedSession).handleBump,
		"reopen":       (*feedSession).handleReopen,
		"start-source": (*feedSession).handleStartSource,
		"hold":         (*feedSession).handleHold,
		"release":      (*feedSession).handleRelease,
		"drop":         (*feedSession).handleDrop,
		"restart":      (*feedSession).handleRestart,
		"bad-frame":    (*feedSession).handleBadFrame,
		"format":       (*feedSession).handleFormat,
		"newer-build":  (*feedSession).handleNewerBuild,
		"changes-fail": (*feedSession).handleChangesFail,
		"changes-hold": (*feedSession).handleChangesHold,
		"read-fail":    (*feedSession).handleReadFail,
		"trim":         (*feedSession).handleTrim,
		"churn":        (*feedSession).handleChurn,
	}
	for verb, h := range controls {
		mux.HandleFunc("POST /control/feed/{id}/"+verb, fr.with(h))
	}
}

func (fr *feedRegistry) with(h func(*feedSession, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fr.mu.Lock()
		s, ok := fr.sessions[r.PathValue("id")]
		fr.mu.Unlock()
		if !ok {
			httpserver.WriteError(w, r, http.StatusNotFound, "feed_session_unknown", "no feed session with that id")
			return
		}
		h(s, w, r)
	}
}

// shutdown ends every session's streams.
func (fr *feedRegistry) shutdown(ctx context.Context) error {
	fr.mu.Lock()
	sessions := slices.Collect(maps.Values(fr.sessions))
	fr.mu.Unlock()
	errs := make([]error, 0, len(sessions))
	for _, s := range sessions {
		s.mu.Lock()
		hub := s.hub
		s.mu.Unlock()
		errs = append(errs, hub.Shutdown(ctx))
	}
	return errors.Join(errs...)
}

type sourceRequest struct {
	Name    string   `json:"name"`
	Kinds   []string `json:"kinds"`
	Rev     uint64   `json:"rev"`
	Log     bool     `json:"log"`
	Pending bool     `json:"pending"`
}

type sessionRequest struct {
	Wire    string          `json:"wire"`
	Sources []sourceRequest `json:"sources"`
}

type sessionResponse struct {
	ID   string `json:"id"`
	Wire string `json:"wire"`
}

// handleCreate starts a session. A source marked pending is declared but not
// started: the start-source control starts it. An empty wire is drawn at random.
func (fr *feedRegistry) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req sessionRequest
	if !decodeControl(w, r, &req) {
		return
	}
	sources, err := newFeedSources(req.Sources)
	if err == nil && req.Wire != "" && !isLowerHex(req.Wire, 16) {
		err = errors.New("wire must be 16 lowercase hex")
	}
	if err != nil {
		httpserver.WriteError(w, r, http.StatusBadRequest, "control_invalid", err.Error())
		return
	}
	wire := req.Wire
	if wire == "" {
		wire = randomHex(8)
	}
	hubOpts := append([]sse.Option{
		sse.WithReplay(feedReplay), sse.WithReplayTTL(fixtureReplayTTL), sse.WithClientBuffer(fixtureClientBuffer),
	}, fr.opts...)
	s := &feedSession{
		hub:     sse.MustNew(hubOpts...),
		hubOpts: hubOpts,
		conns:   make(map[int]*feedConn),
		reads:   make(map[string]int),
		sources: sources,
		wire:    wire,
		format:  feedwire.Version,
	}
	fr.mu.Lock()
	if len(fr.sessions) >= maxFeedSessions {
		fr.mu.Unlock()
		httpserver.WriteError(w, r, http.StatusTooManyRequests, "control_invalid", "too many feed sessions")
		return
	}
	fr.next++
	id := "s" + strconv.Itoa(fr.next)
	fr.sessions[id] = s
	fr.mu.Unlock()
	httpserver.WriteJSON(w, sessionResponse{ID: id, Wire: wire})
}

func newFeedSources(reqs []sourceRequest) ([]*feedSource, error) {
	if len(reqs) == 0 || len(reqs) > maxFeedSources {
		return nil, fmt.Errorf("a session holds 1 to %d sources", maxFeedSources)
	}
	seen := make(map[string]bool)
	out := make([]*feedSource, 0, len(reqs))
	for _, sr := range reqs {
		if !feedwire.ValidSource(sr.Name) || seen[sr.Name] || len(sr.Kinds) == 0 || sr.Rev > feedwire.MaxRev {
			return nil, fmt.Errorf("source %q: a new valid name, at least one kind and a rev up to MaxRev", sr.Name)
		}
		seen[sr.Name] = true
		src := &feedSource{
			name: sr.Name, kinds: slices.Sorted(slices.Values(sr.Kinds)), logged: sr.Log, started: !sr.Pending,
			rows: make(map[string]map[string]string),
		}
		for _, k := range src.kinds {
			if !feedwire.ValidKind(k) || seen["kind:"+k] {
				return nil, fmt.Errorf("kind %q: a valid name held by one source", k)
			}
			seen["kind:"+k] = true
			src.rows[k] = make(map[string]string)
		}
		src.reset(sr.Rev)
		out = append(out, src)
	}
	return out, nil
}

// reset starts a new epoch at rev: no kind has a floor and the log is empty.
func (src *feedSource) reset(rev uint64) {
	src.epoch = randomHex(16)
	src.rev, src.start = rev, rev
	src.floors = make(map[string]uint64)
	src.changes = nil
}

func (src *feedSource) holds(kind string) bool {
	_, ok := slices.BinarySearch(src.kinds, kind)
	return ok
}

func (src *feedSource) listening() feedwire.ListeningSource {
	return feedwire.ListeningSource{Source: src.name, Epoch: src.epoch, Floors: maps.Clone(src.floors)}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return hex.EncodeToString(b)
}

func isLowerHex(s string, n int) bool {
	return len(s) == n && strings.Trim(s, "0123456789abcdef") == ""
}

// sourceLocked returns the named source, or nil.
func (s *feedSession) sourceLocked(name string) *feedSource {
	for _, src := range s.sources {
		if src.name == name {
			return src
		}
	}
	return nil
}

func (s *feedSession) kindSourceLocked(kind string) *feedSource {
	for _, src := range s.sources {
		if src.holds(kind) {
			return src
		}
	}
	return nil
}

// serveEvents joins the session's hub once no hold is set. The check and the
// registration share one lock, so a drop that holds and then ends every
// registered stream leaves none connected while it publishes its filler.
func (s *feedSession) serveEvents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn := &feedConn{cancel: cancel, done: make(chan struct{})}
	defer close(conn.done)
	var hub *sse.Hub
	var id int
	for hub == nil {
		s.mu.Lock()
		release := s.release
		if release == nil {
			hub, id = s.hub, s.nextConn
			s.nextConn++
			s.connects++
			s.conns[id] = conn
		}
		s.mu.Unlock()
		if release != nil {
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
		}
	}
	defer func() {
		s.mu.Lock()
		delete(s.conns, id)
		s.mu.Unlock()
	}()
	hub.Serve(w, r.WithContext(ctx), sse.WithOnConnect(func(sw *sse.Writer, _ sse.Hello) error { return s.writeListening(sw) }))
}

// writeListening is the connect hook: the session's floors are copied under
// the lock and written after it, as the change feed's own hook does.
func (s *feedSession) writeListening(sw *sse.Writer) error {
	s.mu.Lock()
	l := feedwire.Listening{Wire: s.wire}
	for _, src := range s.sources {
		if src.started {
			l.Sources = append(l.Sources, src.listening())
		}
	}
	format := s.format
	if s.newer {
		l.Sources = newerBuild(l.Sources)
	}
	s.mu.Unlock()
	if format != feedwire.Version {
		return sw.Event("listening", fmt.Appendf(nil, `{"feed":%d,"wire":%q,"sources":{"format":%d}}`, format, l.Wire, format))
	}
	data, err := feedwire.EncodeListening(l)
	if err != nil {
		return err
	}
	return sw.Event("listening", data)
}

type feedRow struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type readResponse struct {
	Kind string    `json:"kind"`
	Rows []feedRow `json:"rows"`
}

// serveRead answers a kind's rows, with Feed-Position for its source when the
// source has started, taken under the same lock as the rows.
func (s *feedSession) serveRead(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	s.mu.Lock()
	src := s.kindSourceLocked(kind)
	if src == nil {
		s.mu.Unlock()
		httpserver.WriteError(w, r, http.StatusBadRequest, "bad_query", "kind is not one of the session's")
		return
	}
	s.reads[kind]++
	if s.readFail > 0 && (s.readFailKind == "" || s.readFailKind == kind) {
		s.readFail--
		mode := s.readFailMode
		s.mu.Unlock()
		failRead(w, r, mode)
		return
	}
	resp := readResponse{Kind: kind, Rows: rowsOf(src.rows[kind])}
	var position string
	if src.started {
		position, _ = feedwire.FormatPosition([]feedwire.Position{{Source: src.name, Epoch: src.epoch, Rev: src.rev}})
	}
	s.mu.Unlock()
	if position != "" {
		w.Header().Set("Feed-Position", position)
	}
	httpserver.WriteJSON(w, resp)
}

// failRead either closes the connection with nothing written, which a fetch
// rejects, or answers 503.
func failRead(w http.ResponseWriter, r *http.Request, mode string) {
	if mode == feedFailReject {
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			_ = conn.Close()
			return
		}
	}
	httpserver.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "failure requested by read-fail")
}

func rowsOf(rows map[string]string) []feedRow {
	out := make([]feedRow, 0, len(rows))
	for _, k := range slices.Sorted(maps.Keys(rows)) {
		out = append(out, feedRow{Key: k, Value: rows[k]})
	}
	return out
}

type changesAnswer struct {
	Upserts map[string][]feedRow `json:"upserts"`
	Removed map[string][]string  `json:"removed"`
	Source  string               `json:"source"`
	Epoch   string               `json:"epoch"`
	Rev     uint64               `json:"rev"`
	Full    bool                 `json:"full"`
}

// serveChanges answers a catch-up from the source's log. Every query is
// recorded with its answer, refused ones included, so a test counts requests.
func (s *feedSession) serveChanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := changesQuery{Source: q.Get("source"), Epoch: q.Get("epoch"), Kinds: strings.Split(q.Get("kinds"), ",")}
	since, sinceErr := strconv.ParseUint(q.Get("since"), 10, 64)
	query.Since = since
	s.mu.Lock()
	src := s.sourceLocked(query.Source)
	valid := sinceErr == nil && src != nil && src.logged && feedwire.ValidEpoch(query.Epoch) &&
		!slices.ContainsFunc(query.Kinds, func(k string) bool { return !src.holds(k) })
	held := s.changesRelease
	s.mu.Unlock()
	if !valid {
		s.record(&query, feedAnswerRefuse)
		httpserver.WriteError(w, r, http.StatusBadRequest, "bad_query", "malformed query, unknown kind, or an unlogged source")
		return
	}
	if held != nil && !s.waitHeld(r.Context(), held) {
		return
	}
	s.mu.Lock()
	if s.changesFail > 0 {
		s.changesFail--
		query.Answer = feedAnswerMaint
		s.queries = append(s.queries, query)
		s.mu.Unlock()
		httpserver.WriteError(w, r, http.StatusServiceUnavailable, "maintenance", "failure requested by changes-fail")
		return
	}
	answer := src.answer(&query)
	query.Answer = feedAnswerRows
	if answer.Full {
		query.Answer = feedAnswerFull
	}
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	httpserver.WriteJSON(w, answer)
}

// waitHeld waits for the changes-hold release, counted in the state's
// heldChanges meanwhile; false when the client left first.
func (s *feedSession) waitHeld(ctx context.Context, held <-chan struct{}) bool {
	s.mu.Lock()
	s.changesHeld++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.changesHeld--
		s.mu.Unlock()
	}()
	select {
	case <-held:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *feedSession) record(q *changesQuery, answer string) {
	q.Answer = answer
	s.mu.Lock()
	s.queries = append(s.queries, *q)
	s.mu.Unlock()
}

// answer is full when the query names another epoch or a revision outside the
// log; otherwise it holds each key changed after since in its current state.
func (src *feedSource) answer(q *changesQuery) changesAnswer {
	a := changesAnswer{Source: src.name, Epoch: src.epoch, Rev: src.rev, Upserts: map[string][]feedRow{}, Removed: map[string][]string{}}
	if q.Epoch != src.epoch || q.Since < src.start || q.Since > src.rev {
		a.Full = true
		return a
	}
	for kind, keys := range src.changedSince(q.Since, q.Kinds) {
		for _, key := range slices.Sorted(maps.Keys(keys)) {
			if v, ok := src.rows[kind][key]; ok {
				a.Upserts[kind] = append(a.Upserts[kind], feedRow{Key: key, Value: v})
			} else {
				a.Removed[kind] = append(a.Removed[kind], key)
			}
		}
	}
	return a
}

// changedSince returns, per asked kind, the keys changed after since.
func (src *feedSource) changedSince(since uint64, kinds []string) map[string]map[string]bool {
	changed := make(map[string]map[string]bool)
	for _, c := range src.changes {
		if c.rev <= since || !slices.Contains(kinds, c.kind) {
			continue
		}
		if changed[c.kind] == nil {
			changed[c.kind] = make(map[string]bool)
		}
		changed[c.kind][c.key] = true
	}
	return changed
}

type commitRow struct {
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	Removed bool   `json:"removed"`
}

type commitRequest struct {
	Source string      `json:"source"`
	Kinds  []string    `json:"kinds"`
	Rows   []commitRow `json:"rows"`
	Count  int         `json:"count"`
}

type commitResponse struct {
	Epoch string `json:"epoch"`
	Rev   uint64 `json:"rev"`
}

// handleCommit writes rows in one commit and publishes its changed frame.
func (s *feedSession) handleCommit(w http.ResponseWriter, r *http.Request) {
	var req commitRequest
	if !decodeControl(w, r, &req) {
		return
	}
	kinds := make([]string, 0, len(req.Rows))
	for _, row := range req.Rows {
		kinds = append(kinds, row.Kind)
	}
	s.commit(w, r, req.Source, kinds, req.Rows, 1)
}

// handleBump publishes count commits of the given kinds that change no row,
// as an unlogged source's Bump does.
func (s *feedSession) handleBump(w http.ResponseWriter, r *http.Request) {
	var req commitRequest
	if !decodeControl(w, r, &req) {
		return
	}
	s.commit(w, r, req.Source, req.Kinds, nil, max(req.Count, 1))
}

func (s *feedSession) commit(w http.ResponseWriter, r *http.Request, source string, kinds []string, rows []commitRow, count int) {
	if len(rows) > maxFeedRows || count > maxFeedRows || len(kinds) == 0 {
		httpserver.WriteError(w, r, http.StatusBadRequest, "control_invalid", "a commit names 1 to 1024 rows or kinds, and at most 1024 bumps")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.sourceLocked(source)
	if src == nil || !src.started || slices.ContainsFunc(kinds, func(k string) bool { return !src.holds(k) }) {
		httpserver.WriteError(w, r, http.StatusBadRequest, "control_invalid", "commit to a started source, naming its own kinds")
		return
	}
	if err := s.publishCommitsLocked(src, rows, sortedKinds(kinds), count); err != nil {
		httpserver.WriteError(w, r, http.StatusUnprocessableEntity, "publish_refused", err.Error())
		return
	}
	httpserver.WriteJSON(w, commitResponse{Epoch: src.epoch, Rev: src.rev})
}

func sortedKinds(kinds []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(kinds)))
}

// publishCommitsLocked applies count commits and publishes each one's changed
// frame; under newer-build the first source's frames also name newerKind.
func (s *feedSession) publishCommitsLocked(src *feedSource, rows []commitRow, kinds []string, count int) error {
	for range count {
		c := src.apply(rows, kinds)
		if s.newer && src == s.sources[0] {
			c.Kinds = sortedKinds(append(slices.Clone(c.Kinds), newerKind))
		}
		data, err := feedwire.EncodeChanged(c)
		if err == nil {
			_, err = s.hub.Publish(sse.Event{Name: "changed", Data: data})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// apply writes one commit at the next revision and returns its changed frame.
func (src *feedSource) apply(rows []commitRow, kinds []string) feedwire.Changed {
	src.rev++
	for _, row := range rows {
		if row.Removed {
			delete(src.rows[row.Kind], row.Key)
		} else {
			src.rows[row.Kind][row.Key] = row.Value
		}
		if src.logged {
			src.changes = append(src.changes, feedChange{kind: row.Kind, key: row.Key, rev: src.rev})
		}
	}
	for _, k := range kinds {
		src.floors[k] = src.rev
	}
	return feedwire.Changed{Source: src.name, Epoch: src.epoch, Rev: src.rev, Kinds: kinds}
}

type sourceControl struct {
	Source string `json:"source"`
	Rev    uint64 `json:"rev"`
}

// handleReopen starts a new epoch for one source at the given revision and
// publishes its listening frame, with an id and no wire, as an open hook does.
func (s *feedSession) handleReopen(w http.ResponseWriter, r *http.Request) {
	var req sourceControl
	if !decodeControl(w, r, &req) {
		return
	}
	s.publishSource(w, r, req.Source, func(src *feedSource) bool {
		if !src.started || req.Rev > feedwire.MaxRev {
			return false
		}
		src.reset(req.Rev)
		return true
	})
}

// handleStartSource starts a pending source and publishes its listening frame.
func (s *feedSession) handleStartSource(w http.ResponseWriter, r *http.Request) {
	var req sourceControl
	if !decodeControl(w, r, &req) {
		return
	}
	s.publishSource(w, r, req.Source, func(src *feedSource) bool {
		if src.started {
			return false
		}
		src.started = true
		return true
	})
}

func (s *feedSession) publishSource(w http.ResponseWriter, r *http.Request, name string, change func(*feedSource) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.sourceLocked(name)
	if src == nil || !change(src) {
		httpserver.WriteError(w, r, http.StatusBadRequest, "control_invalid", "no such source in a state this control applies to")
		return
	}
	data, err := feedwire.EncodeListening(feedwire.Listening{Sources: []feedwire.ListeningSource{src.listening()}})
	if err == nil {
		_, err = s.hub.Publish(sse.Event{Name: "listening", Data: data})
	}
	if err != nil {
		httpserver.WriteError(w, r, http.StatusUnprocessableEntity, "publish_refused", err.Error())
		return
	}
	httpserver.WriteJSON(w, commitResponse{Epoch: src.epoch, Rev: src.rev})
}

// handleHold makes new streams wait before they join the hub; release lets
// them join.
func (s *feedSession) handleHold(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	if s.release == nil {
		s.release = make(chan struct{})
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *feedSession) handleRelease(w http.ResponseWriter, _ *http.Request) {
	s.releaseHold()
	w.WriteHeader(http.StatusNoContent)
}

func (s *feedSession) releaseHold() {
	s.mu.Lock()
	if s.release != nil {
		close(s.release)
		s.release = nil
	}
	s.mu.Unlock()
}

type dropRequest struct {
	Resume bool `json:"resume"`
}

// handleDrop ends every stream. Without resume it publishes filler frames past
// the replay ring while the streams are held off, so each reconnect's hello
// answers gap_floor. A hold set before the drop stays set.
func (s *feedSession) handleDrop(w http.ResponseWriter, r *http.Request) {
	var req dropRequest
	if !decodeControl(w, r, &req) {
		return
	}
	s.mu.Lock()
	ownHold := s.release == nil
	if ownHold {
		s.release = make(chan struct{})
	}
	hub := s.hub
	conns := slices.Collect(maps.Values(s.conns))
	s.mu.Unlock()
	if err := endConns(r.Context(), conns); err != nil {
		if ownHold {
			s.releaseHold()
		}
		httpserver.WriteError(w, r, http.StatusInternalServerError, "drop_timeout", err.Error())
		return
	}
	if !req.Resume {
		for range feedFiller {
			if _, err := hub.Publish(sse.Event{Name: "filler", Data: []byte("{}")}); err != nil {
				httpserver.WriteError(w, r, http.StatusUnprocessableEntity, "publish_refused", err.Error())
				return
			}
		}
	}
	if ownHold {
		s.releaseHold()
	}
	w.WriteHeader(http.StatusNoContent)
}

// endConns cancels each stream and waits until its handler returned.
func endConns(ctx context.Context, conns []*feedConn) error {
	for _, c := range conns {
		c.cancel()
	}
	timer := time.NewTimer(feedConnWait)
	defer timer.Stop()
	for _, c := range conns {
		select {
		case <-c.done:
		case <-timer.C:
			return errors.New("a stream did not end in time")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// handleRestart replaces the hub and gives every source a new epoch, keeping
// its revision and rows, as a process restart does: each stream ends with
// sse:reset and reconnects to an epoch_changed hello.
func (s *feedSession) handleRestart(w http.ResponseWriter, r *http.Request) {
	next := sse.MustNew(s.hubOpts...)
	s.mu.Lock()
	old := s.hub
	s.hub = next
	for _, src := range s.sources {
		src.reset(src.rev)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), restartShutdownWait)
	defer cancel()
	_ = old.Shutdown(ctx)
	w.WriteHeader(http.StatusNoContent)
}

type badFrameRequest struct {
	Kind   string      `json:"kind"`
	Source string      `json:"source"`
	Rows   []commitRow `json:"rows"`
	Commit bool        `json:"commit"`
}

// handleBadFrame publishes a changed frame the tab cannot apply: malformed, or
// on an epoch the source never had. With commit it stands for a real commit
// of the rows (or a bump of the first kind), applied before the frame names
// its revision; otherwise the source's state is unchanged.
func (s *feedSession) handleBadFrame(w http.ResponseWriter, r *http.Request) {
	var req badFrameRequest
	if !decodeControl(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.sourceLocked(req.Source)
	if src == nil || !src.started || (req.Kind != "malformed" && req.Kind != "other-epoch") ||
		len(req.Rows) > maxFeedRows || slices.ContainsFunc(req.Rows, func(c commitRow) bool { return !src.holds(c.Kind) }) {
		httpserver.WriteError(w, r, http.StatusBadRequest, "control_invalid",
			`bad-frame takes a started source, kind "malformed" or "other-epoch", and rows of its kinds`)
		return
	}
	kinds := src.kinds[:1]
	if len(req.Rows) > 0 {
		kinds = make([]string, 0, len(req.Rows))
		for _, row := range req.Rows {
			kinds = append(kinds, row.Kind)
		}
		kinds = sortedKinds(kinds)
	}
	rev := src.rev + 1
	if req.Commit {
		rev = src.apply(req.Rows, kinds).Rev
	}
	data := fmt.Appendf(nil, `{"source":%q,"epoch":"not-an-epoch","rev":%d,"kinds":[%q]}`, src.name, rev, kinds[0])
	if req.Kind == "other-epoch" {
		data, _ = feedwire.EncodeChanged(feedwire.Changed{Source: src.name, Epoch: randomHex(16), Rev: rev, Kinds: kinds})
	}
	if _, err := s.hub.Publish(sse.Event{Name: "changed", Data: data}); err != nil {
		httpserver.WriteError(w, r, http.StatusUnprocessableEntity, "publish_refused", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type formatRequest struct {
	Feed int `json:"feed"`
}

// handleFormat sets the feed format the connect hook writes: another number
// than feedwire.Version writes only the frozen members a newer format keeps.
func (s *feedSession) handleFormat(w http.ResponseWriter, r *http.Request) {
	var req formatRequest
	if !decodeControl(w, r, &req) {
		return
	}
	if req.Feed < 1 {
		httpserver.WriteError(w, r, http.StatusBadRequest, "control_invalid", "feed must be a positive integer")
		return
	}
	s.mu.Lock()
	s.format = req.Feed
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// newerBuild adds what a server running a newer build names: newerKind on the
// first source, and a source no tab declared.
func newerBuild(sources []feedwire.ListeningSource) []feedwire.ListeningSource {
	if len(sources) > 0 {
		sources[0].Floors[newerKind] = 1
	}
	return append(sources, feedwire.ListeningSource{
		Source: "zz_newer_source", Epoch: randomHex(16), Floors: map[string]uint64{"zz_newer_only": 1},
	})
}

// handleNewerBuild makes the connect hook write newerBuild's extra kind and
// source from the next connection, and every later changed frame of the
// first source name newerKind.
func (s *feedSession) handleNewerBuild(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.newer = true
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

type failRequest struct {
	Mode  string `json:"mode"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	On    bool   `json:"on"`
}

// handleChangesFail answers the next count catch-ups 503 maintenance.
func (s *feedSession) handleChangesFail(w http.ResponseWriter, r *http.Request) {
	var req failRequest
	if !decodeControl(w, r, &req) {
		return
	}
	s.mu.Lock()
	s.changesFail = max(req.Count, 0)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// handleChangesHold holds every catch-up until it is called with on false.
func (s *feedSession) handleChangesHold(w http.ResponseWriter, r *http.Request) {
	var req failRequest
	if !decodeControl(w, r, &req) {
		return
	}
	s.mu.Lock()
	switch {
	case req.On && s.changesRelease == nil:
		s.changesRelease = make(chan struct{})
	case !req.On && s.changesRelease != nil:
		close(s.changesRelease)
		s.changesRelease = nil
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// handleReadFail fails the next count reads, of one kind when kind is set, by
// closing the connection (mode "reject") or with 503 (mode "unavailable").
func (s *feedSession) handleReadFail(w http.ResponseWriter, r *http.Request) {
	var req failRequest
	if !decodeControl(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if (req.Mode != feedFailReject && req.Mode != feedFailUnavail) || (req.Kind != "" && s.kindSourceLocked(req.Kind) == nil) {
		httpserver.WriteError(w, r, http.StatusBadRequest, "control_invalid", `mode is "reject" or "unavailable", and kind empty or one of the session's`)
		return
	}
	s.readFail, s.readFailMode, s.readFailKind = max(req.Count, 0), req.Mode, req.Kind
	w.WriteHeader(http.StatusNoContent)
}

// handleTrim drops a logged source's log up to its revision, so a catch-up
// from below it is answered full on the same epoch.
func (s *feedSession) handleTrim(w http.ResponseWriter, r *http.Request) {
	var req sourceControl
	if !decodeControl(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.sourceLocked(req.Source)
	if src == nil || !src.logged {
		httpserver.WriteError(w, r, http.StatusBadRequest, "control_invalid", "trim takes a logged source of the session")
		return
	}
	src.start, src.changes = src.rev, nil
	w.WriteHeader(http.StatusNoContent)
}

type churnRequest struct {
	Source  string   `json:"source"`
	Kinds   []string `json:"kinds"`
	EveryMs int      `json:"everyMs"`
	ForMs   int      `json:"forMs"`
}

// handleChurn answers at once and then bumps the kinds every everyMs for
// forMs, as a busy unlogged source does, without one control call per bump.
func (s *feedSession) handleChurn(w http.ResponseWriter, r *http.Request) {
	var req churnRequest
	if !decodeControl(w, r, &req) {
		return
	}
	every, span := time.Duration(req.EveryMs)*time.Millisecond, time.Duration(req.ForMs)*time.Millisecond
	s.mu.Lock()
	src := s.sourceLocked(req.Source)
	ok := src != nil && src.started && len(req.Kinds) > 0 && every > 0 && span >= every && span <= maxChurn &&
		!slices.ContainsFunc(req.Kinds, func(k string) bool { return !src.holds(k) })
	s.mu.Unlock()
	if !ok {
		httpserver.WriteError(w, r, http.StatusBadRequest, "control_invalid", "churn takes a started source, its kinds, and 0 < everyMs <= forMs <= 60000")
		return
	}
	go s.churn(src, sortedKinds(req.Kinds), every, span)
	w.WriteHeader(http.StatusNoContent)
}

// churn stops at the end of its span, or at the first publish the hub
// refuses, which a shutdown causes.
func (s *feedSession) churn(src *feedSource, kinds []string, every, span time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	end := time.NewTimer(span)
	defer end.Stop()
	for {
		select {
		case <-end.C:
			return
		case <-tick.C:
			s.mu.Lock()
			err := s.publishCommitsLocked(src, nil, kinds, 1)
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

type feedSourceState struct {
	Floors  map[string]uint64 `json:"floors"`
	Name    string            `json:"name"`
	Epoch   string            `json:"epoch"`
	Rev     uint64            `json:"rev"`
	Started bool              `json:"started"`
}

type feedStateResponse struct {
	Reads       map[string]int    `json:"reads"`
	Sources     []feedSourceState `json:"sources"`
	Queries     []changesQuery    `json:"queries"`
	Connects    int               `json:"connects"`
	Connected   int               `json:"connected"`
	HeldChanges int               `json:"heldChanges"`
}

func (s *feedSession) serveState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	resp := feedStateResponse{
		Reads:       maps.Clone(s.reads),
		Queries:     slices.Clone(s.queries),
		Connects:    s.connects,
		Connected:   len(s.conns),
		HeldChanges: s.changesHeld,
	}
	for _, src := range s.sources {
		resp.Sources = append(resp.Sources, feedSourceState{
			Floors: maps.Clone(src.floors), Name: src.name, Epoch: src.epoch, Rev: src.rev, Started: src.started,
		})
	}
	s.mu.Unlock()
	if resp.Queries == nil {
		resp.Queries = []changesQuery{}
	}
	httpserver.WriteJSON(w, resp)
}
