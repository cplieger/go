package sse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestValidTopic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		topic string
		want  bool
	}{
		{"a workflow topic", "run:wf_25c798e7b048a8e0", true},
		{"one byte", "a", true},
		{"128 bytes", strings.Repeat("x", 128), true},
		{"every printable byte but quote and backslash", "!#$%&'()*+,-./09:;<=>?@AZ[]^_`az{|}~", true},
		{"empty", "", false},
		{"129 bytes", strings.Repeat("x", 129), false},
		{"space", "run a", false},
		{"quote", `run"a`, false},
		{"backslash", `run\a`, false},
		{"control", "run\ta", false},
		{"DEL", "run\x7fa", false},
		{"Latin-1", "run:é", false},
		{"CJK", "run:日本", false},
		{"emoji", "run:😀", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidTopic(tc.topic); got != tc.want {
				t.Errorf("ValidTopic(%q) = %v, want %v", tc.topic, got, tc.want)
			}
		})
	}
}

func TestParseScope(t *testing.T) {
	many := make([]string, MaxScopeTopics+1)
	for i := range many {
		many[i] = fmt.Sprintf("t%d", i)
	}
	tooMany, _ := json.Marshal(many)
	atMax, _ := json.Marshal(many[:MaxScopeTopics])
	for _, tc := range []struct {
		name    string
		raw     string
		wantErr bool
		all     bool
		topics  []string
	}{
		{name: "null is everything", raw: "null", all: true},
		{name: "an empty array is broadcasts only", raw: "[]"},
		{name: "a set", raw: `["run:a","run:b"]`, topics: []string{"run:a", "run:b"}},
		{name: "duplicates collapse", raw: `["run:a","run:a"]`, topics: []string{"run:a"}},
		{name: "at the topic bound", raw: string(atMax), topics: many[:MaxScopeTopics]},
		{name: "past the topic bound", raw: string(tooMany), wantErr: true},
		{name: "a topic outside the grammar", raw: `["run a"]`, wantErr: true},
		{name: "an empty topic", raw: `[""]`, wantErr: true},
		{name: "a non-ASCII topic", raw: `["run:\u00e9"]`, wantErr: true},
		{name: "an object", raw: `{"topics":[]}`, wantErr: true},
		{name: "a string", raw: `"run:a"`, wantErr: true},
		{name: "a number in the array", raw: `[1]`, wantErr: true},
		{name: "not JSON", raw: `[run:a]`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := parseScope([]byte(tc.raw))
			if tc.wantErr {
				if !errors.Is(err, errScope) {
					t.Fatalf("parseScope(%s) error = %v, want errScope", tc.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseScope(%s) = %v", tc.raw, err)
			}
			if s.scoped == tc.all {
				t.Fatalf("parseScope(%s) everything = %v, want %v", tc.raw, !s.scoped, tc.all)
			}
			if len(s.topics) != len(tc.topics) {
				t.Fatalf("parseScope(%s) holds %d topics, want %d", tc.raw, len(s.topics), len(tc.topics))
			}
			for _, topic := range tc.topics {
				if !s.matches(topic) {
					t.Errorf("parseScope(%s) does not admit %q", tc.raw, topic)
				}
			}
		})
	}
}

func TestRequestScopeHeader(t *testing.T) {
	req := func(values ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		for _, v := range values {
			r.Header.Add(scopeHeader, v)
		}
		return r
	}
	if s, err := requestScope(req()); err != nil || s.scoped {
		t.Errorf("no header = (%+v, %v), want everything", s, err)
	}
	if s, err := requestScope(req(`["run:a"]`)); err != nil || !s.scoped || !s.matches("run:a") || s.matches("run:b") {
		t.Errorf(`header ["run:a"] = (%+v, %v), want that set`, s, err)
	}
	long := `["` + strings.Repeat("x", MaxScopeHeaderBytes) + `"]`
	if _, err := requestScope(req(long)); !errors.Is(err, errScope) {
		t.Errorf("a header over MaxScopeHeaderBytes = %v, want errScope", err)
	}
	if _, err := requestScope(req(`["run:a"]`, `["run:b"]`)); !errors.Is(err, errScope) {
		t.Errorf("two SSE-Topics headers = %v, want errScope", err)
	}
}

// A scope delivers broadcasts plus its own topics; the two-workflow case is
// the job: two connections scoped to two workflows each see only their own
// frames.
func TestScope_deliveryByScope(t *testing.T) {
	h := mustNew(t, withEpoch(testEpoch))
	subscribeTo := func(s Scope) *subscription {
		sub, err := h.subscribe(subRequest{scope: s}, Cursor{}, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { h.unsubscribe(sub.c) })
		return sub
	}
	runA, runB := subscribeTo(Topics("run:a")), subscribeTo(Topics("run:b"))
	broadcasts, everything := subscribeTo(Topics()), subscribeTo(All())
	for _, topic := range []string{"run:a", "run:b", "", "chat"} {
		if _, err := h.Publish(Event{Topic: topic, Data: []byte(topic)}); err != nil {
			t.Fatal(err)
		}
	}
	received := func(sub *subscription) []string {
		var got []string
		for len(sub.c.ch) > 0 {
			got = append(got, (<-sub.c.ch).event.Topic)
		}
		return got
	}
	for _, tc := range []struct {
		name string
		sub  *subscription
		want []string
	}{
		{"run:a", runA, []string{"run:a", ""}},
		{"run:b", runB, []string{"run:b", ""}},
		{"broadcasts only", broadcasts, []string{""}},
		{"everything", everything, []string{"run:a", "run:b", "", "chat"}},
	} {
		if got := received(tc.sub); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s received topics %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestScope_lastOptionWins(t *testing.T) {
	for _, tc := range []struct {
		name        string
		opts        []ServeOption
		wantScoped  bool
		wantRequest bool
		wantTopic   string
	}{
		{"WithTopic then WithScope", []ServeOption{WithTopic("t"), WithScope(Topics("a", "b"))}, true, false, ""},
		{"WithScope then WithTopic", []ServeOption{WithScope(Topics("b")), WithTopic("a")}, true, false, "a"},
		{"WithRequestScope then WithTopic everything", []ServeOption{WithRequestScope("p"), WithTopic("")}, false, false, ""},
		{"WithTopic then WithRequestScope", []ServeOption{WithTopic("t"), WithRequestScope("p")}, false, true, ""},
		{"WithRequestScope then WithScope", []ServeOption{WithRequestScope("p"), WithScope(Topics("a"))}, true, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sc serveConfig
			for _, o := range tc.opts {
				o(&sc)
			}
			if sc.scope.scoped != tc.wantScoped || sc.requestScope != tc.wantRequest || sc.topic != tc.wantTopic {
				t.Errorf("config = (scoped %v, request %v, topic %q), want (%v, %v, %q)",
					sc.scope.scoped, sc.requestScope, sc.topic, tc.wantScoped, tc.wantRequest, tc.wantTopic)
			}
			if (sc.owner != nil) != tc.wantRequest {
				t.Errorf("owner = %v, want one only under WithRequestScope", sc.owner)
			}
			if tc.wantScoped && (!sc.scope.matches("a") || sc.scope.matches("z") || !sc.scope.matches("")) {
				t.Errorf("scope admits a %v, z %v, broadcasts %v; want only its own topics and broadcasts",
					sc.scope.matches("a"), sc.scope.matches("z"), sc.scope.matches(""))
			}
		})
	}
}

func TestScope_connMintedOnlyForRequestScope(t *testing.T) {
	h := mustNew(t, withEpoch(testEpoch))
	plain, err := h.subscribe(topicReq("t"), Cursor{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer h.unsubscribe(plain.c)
	if plain.hello.Conn != "" {
		t.Errorf("a WithTopic connection's hello carries conn %q, want none", plain.hello.Conn)
	}
	rescopable, err := h.subscribe(subRequest{scope: All(), rescopable: true}, Cursor{}, false)
	if err != nil {
		t.Fatal(err)
	}
	conn := rescopable.hello.Conn
	raw, err := base64.RawURLEncoding.DecodeString(conn)
	if len(conn) != 22 || err != nil || len(raw) != 16 {
		t.Fatalf("conn %q: %d characters, decode error %v, %d bytes; want 22 characters of unpadded base64url for 16 bytes", conn, len(conn), err, len(raw))
	}
	if _, ok := h.connOwner(conn); !ok {
		t.Fatal("the hub does not index the rescopable connection by its conn")
	}
	h.unsubscribe(rescopable.c)
	if _, ok := h.connOwner(conn); ok {
		t.Error("the conn outlives its connection")
	}
}

func TestServe_helloCarriesConnUnderRequestScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := mustNew(t, withEpoch(testEpoch))
		srv := startServer(t, h, WithRequestScope("p"))
		resp, sc := openStream(t, srv, srv.URL, http.Header{scopeHeader: {`["run:a"]`}})
		defer resp.Body.Close()
		_, lines := readHello(t, sc)
		data, _ := strings.CutPrefix(lines[len(lines)-1], "data: ")
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 9 {
			t.Errorf("hello has %d fields, want 9 with conn: %s", len(raw), data)
		}
		var conn string
		if err := json.Unmarshal(raw["conn"], &conn); err != nil || len(conn) != 22 {
			t.Errorf("hello conn = %s (%v), want 22 characters", raw["conn"], err)
		}
	})
}

func TestServe_refusesABadScopeHeader(t *testing.T) {
	h := mustNew(t, withEpoch(testEpoch))
	for _, value := range []string{`["run a"]`, `[""]`, `{"x":1}`, `["` + strings.Repeat("x", 129) + `"]`} {
		r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		r.Header.Set(scopeHeader, value)
		w := httptest.NewRecorder()
		h.Serve(w, r, WithRequestScope("p"))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"sse_scope"`) {
			t.Errorf("SSE-Topics %s answered %d %s, want 400 sse_scope", value, w.Code, w.Body.String())
		}
	}
	if n := h.ClientCount(); n != 0 {
		t.Errorf("a refused connect left %d clients subscribed", n)
	}
}

// The reply cap counts what the connection would receive: one in-scope frame
// among 300 out-of-scope ones resumes, and more eligible frames than the cap
// refuse the whole resume.
func TestScope_replyCapCountsEligibleFrames(t *testing.T) {
	h := mustNew(t, withEpoch(testEpoch), WithReplay(1024), WithReplayTTL(replayTTL), WithReplyMaxEvents(256))
	publish := func(topic string, n int) {
		for range n {
			if _, err := h.Publish(Event{Topic: topic, Data: []byte("x")}); err != nil {
				t.Fatal(err)
			}
		}
	}
	publish("seed", 1)
	publish("run:a", 1)
	publish("run:b", 300)
	sub, err := h.subscribe(subRequest{scope: Topics("run:a")}, Cursor{Epoch: testEpoch, Offset: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sub.hello.Resumed || len(sub.replay) != 1 || sub.replay[0].event.Topic != "run:a" {
		t.Errorf("resume over 300 out-of-scope frames = (%s, %d frames), want resumed with the one run:a frame", sub.hello.Verdict, len(sub.replay))
	}
	h.unsubscribe(sub.c)

	wide, err := h.subscribe(subRequest{scope: Topics("run:b")}, Cursor{Epoch: testEpoch, Offset: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer h.unsubscribe(wide.c)
	if wide.hello.Verdict != VerdictGapBudget || wide.hello.Resumed || len(wide.replay) != 0 {
		t.Errorf("resume with 300 eligible frames over a cap of 256 = (%s, %v, %d frames), want (gap_budget, false, 0)", wide.hello.Verdict, wide.hello.Resumed, len(wide.replay))
	}
}

// The floor stays unfiltered: an evicted entry may have been in scope, so a
// cursor below it is gap_floor though no in-scope frame is known to be lost.
func TestScope_floorStaysUnfiltered(t *testing.T) {
	h := mustNew(t, withEpoch(testEpoch), WithReplay(4), WithReplayTTL(replayTTL))
	for range 10 {
		if _, err := h.Publish(Event{Topic: "run:b", Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := h.subscribe(subRequest{scope: Topics("run:a")}, Cursor{Epoch: testEpoch, Offset: 2}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer h.unsubscribe(sub.c)
	if sub.hello.Verdict != VerdictGapFloor {
		t.Errorf("cursor below the floor = %s, want gap_floor", sub.hello.Verdict)
	}
}

func TestScope_presenceDescribesTheConnectScope(t *testing.T) {
	var mu sync.Mutex
	var events []PresenceEvent
	h := mustNew(t, withEpoch(testEpoch), WithPresence(func(ev PresenceEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}))
	synctest.Test(t, func(t *testing.T) {
		srv := startServer(t, h, WithRequestScope("p"))
		resp, sc := openStream(t, srv, srv.URL, http.Header{scopeHeader: {`["run:a","run:b"]`}})
		readHello(t, sc)
		resp.Body.Close()
		synctest.Wait()
	})
	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("no presence event")
	}
	if ev := events[0]; !ev.Scoped || ev.TopicCount != 2 || ev.Topic != "" {
		t.Errorf("connected event = (scoped %v, %d topics, topic %q), want (true, 2, \"\")", ev.Scoped, ev.TopicCount, ev.Topic)
	}
}

// scopeServer serves the stream with WithRequestScope and mounts the scope
// handler beside it, authorizing a request whose X-Principal matches the owner.
func scopeServer(t *testing.T, h *Hub, authorize func(r *http.Request, owner any) error) (*httptest.Server, http.Handler) {
	t.Helper()
	if authorize == nil {
		authorize = func(r *http.Request, owner any) error {
			if r.Header.Get("X-Principal") != owner {
				return errors.New("not the owner")
			}
			return nil
		}
	}
	handler, err := h.ScopeHandler(authorize)
	if err != nil {
		t.Fatal(err)
	}
	srv := startServer(t, h, WithRequestScope("p"))
	return srv, handler
}

// postScope sends one ScopeHandler request with the owner's principal.
func postScope(handler http.Handler, method, contentType, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/scope", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-Principal", "p")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestScopeHandler_nilAuthorize(t *testing.T) {
	h := mustNew(t, withEpoch(testEpoch))
	if _, err := h.ScopeHandler(nil); !errors.Is(err, ErrConfig) {
		t.Errorf("ScopeHandler(nil) error = %v, want ErrConfig", err)
	}
}

func TestScopeHandler_answers(t *testing.T) {
	h := mustNew(t, withEpoch(testEpoch))
	_, handler := scopeServer(t, h, nil)
	sub, err := h.subscribe(subRequest{scope: Topics("run:a"), owner: "p", rescopable: true}, Cursor{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer h.unsubscribe(sub.c)
	hello := sub.hello
	body := func(seq int, topics string) string {
		return fmt.Sprintf(`{"conn":%q,"seq":%d,"topics":%s}`, hello.Conn, seq, topics)
	}
	for _, tc := range []struct {
		name, method, contentType, body string
		principal                       string
		wantStatus                      int
		wantCode                        string
	}{
		{"GET", http.MethodGet, "application/json", body(1, "[]"), "p", http.StatusMethodNotAllowed, "scope_method"},
		{"a text body", http.MethodPost, "text/plain", body(1, "[]"), "p", http.StatusUnsupportedMediaType, "scope_content_type"},
		{"over the body cap", http.MethodPost, "application/json", `{"conn":"x","seq":1,"topics":["` + strings.Repeat("x", maxScopeBody) + `"]}`, "p", http.StatusRequestEntityTooLarge, "scope_too_large"},
		{"malformed JSON", http.MethodPost, "application/json", `{"conn":`, "p", http.StatusBadRequest, "scope_invalid"},
		{"absent topics", http.MethodPost, "application/json", fmt.Sprintf(`{"conn":%q,"seq":1}`, hello.Conn), "p", http.StatusBadRequest, "scope_invalid"},
		{"absent seq", http.MethodPost, "application/json", fmt.Sprintf(`{"conn":%q,"topics":[]}`, hello.Conn), "p", http.StatusBadRequest, "scope_invalid"},
		{"seq zero", http.MethodPost, "application/json", body(0, "[]"), "p", http.StatusBadRequest, "scope_invalid"},
		{"a topic outside the grammar", http.MethodPost, "application/json", body(1, `["run a"]`), "p", http.StatusBadRequest, "sse_scope"},
		{"an unknown conn", http.MethodPost, "application/json", `{"conn":"AAAAAAAAAAAAAAAAAAAAAA","seq":1,"topics":[]}`, "p", http.StatusNotFound, "scope_conn"},
		{"another principal", http.MethodPost, "application/json", body(1, "[]"), "q", http.StatusForbidden, "scope_forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/scope", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			r.Header.Set("X-Principal", tc.principal)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.wantStatus || !strings.Contains(w.Body.String(), `"`+tc.wantCode+`"`) {
				t.Errorf("answered %d %s, want %d %s", w.Code, w.Body.String(), tc.wantStatus, tc.wantCode)
			}
			if tc.wantStatus == http.StatusMethodNotAllowed && w.Header().Get("Allow") != http.MethodPost {
				t.Errorf("405 Allow = %q, want POST", w.Header().Get("Allow"))
			}
		})
	}
	for range 3 {
		if _, err := h.Publish(Event{Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}
	ok := postScope(handler, http.MethodPost, "application/json", body(1, `["run:b"]`))
	var answer struct {
		Epoch  string `json:"epoch"`
		Offset string `json:"offset"`
	}
	if ok.Code != http.StatusOK || json.Unmarshal(ok.Body.Bytes(), &answer) != nil || answer.Epoch != testEpoch || answer.Offset != "3" {
		t.Fatalf("a valid request answered %d %s, want 200 with this epoch and the head 3 as a decimal string", ok.Code, ok.Body.String())
	}
	if all := postScope(handler, http.MethodPost, "application/json", body(2, "null")); all.Code != http.StatusOK {
		t.Fatalf("an explicit null answered %d %s, want 200", all.Code, all.Body.String())
	}
	h.mu.Lock()
	everything := !h.byConn[hello.Conn].scope.scoped
	h.mu.Unlock()
	if !everything {
		t.Error("an explicit null did not widen the connection to everything")
	}
	if stale := postScope(handler, http.MethodPost, "application/json", body(2, "[]")); stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"scope_stale"`) {
		t.Errorf("a repeated seq answered %d %s, want 409 scope_stale", stale.Code, stale.Body.String())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = h.Shutdown(ctx)
	if gone := postScope(handler, http.MethodPost, "application/json", body(3, "[]")); gone.Code != http.StatusNotFound {
		t.Errorf("a request after Shutdown answered %d, want 404", gone.Code)
	}
}

func TestScopeHandler_unknownAfterSlowEviction(t *testing.T) {
	h := mustNew(t, withEpoch(testEpoch), WithClientBuffer(clientBufferFloor))
	sub, err := h.subscribe(subRequest{scope: All(), rescopable: true}, Cursor{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer h.wg.Done()
	for range clientBufferFloor + 1 {
		if _, err := h.Publish(Event{Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}
	if !isClosed(sub.c.reset) {
		t.Fatal("the full client was not evicted")
	}
	if _, err := h.setScopeSeq(sub.hello.Conn, 1, Topics()); !errors.Is(err, errUnknownConn) {
		t.Errorf("setScopeSeq after a slow eviction = %v, want errUnknownConn", err)
	}
}

func TestScopeResponse_offsetAtMaxOffset(t *testing.T) {
	b, err := json.Marshal(scopeResponse{Epoch: testEpoch, Offset: MaxOffset})
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf(`{"epoch":%q,"offset":"9007199254740991"}`, testEpoch); string(b) != want {
		t.Errorf("response = %s, want %s", b, want)
	}
}

// One head splits the stream: every frame at or below the offset a swap
// returns was matched against the old scope, every later one against the new,
// with publishers running concurrently. Each publisher stops at 400 frames
// until the swap and sends 100 after it, so the split always lands mid-stream
// and the stream fits the ring.
func TestSetScopeSeq_splitsTheStreamAtOneHead(t *testing.T) {
	h := mustNew(t, withEpoch(testEpoch), WithClientBuffer(4096), WithReplay(1024), WithReplayTTL(replayTTL))
	sub, err := h.subscribe(subRequest{scope: Topics("a"), rescopable: true}, Cursor{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer h.unsubscribe(sub.c)
	var wg sync.WaitGroup
	var swapped atomic.Bool
	defer func() { swapped.Store(true); wg.Wait() }()
	start := make(chan struct{})
	for _, topic := range []string{"a", "b"} {
		wg.Go(func() {
			<-start
			published := 0
			for after := 0; after < 100; {
				switch {
				case swapped.Load():
					after++
				case published == 400:
					runtime.Gosched()
					continue
				}
				if _, err := h.Publish(Event{Topic: topic, Data: []byte(topic)}); err != nil {
					t.Error(err)
					return
				}
				published++
				runtime.Gosched()
			}
		})
	}
	close(start)
	deadline := time.Now().Add(30 * time.Second)
	for h.Position().Head < 250 {
		if time.Now().After(deadline) {
			t.Fatalf("head %d: the publishers stopped before 250", h.Position().Head)
		}
		runtime.Gosched()
	}
	split, err := h.setScopeSeq(sub.hello.Conn, 1, Topics("b"))
	swapped.Store(true)
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if head := h.Position().Head; split < 250 || split >= head {
		t.Fatalf("split %d, want mid-stream in [250, %d)", split, head)
	}
	got := map[uint64]string{}
	for len(sub.c.ch) > 0 {
		e := <-sub.c.ch
		got[e.offset] = e.event.Topic
	}
	for _, e := range h.ring.since(0, All()) {
		topic, delivered := got[e.offset]
		want := (e.offset <= split && e.event.Topic == "a") || (e.offset > split && e.event.Topic == "b")
		if delivered != want {
			t.Fatalf("offset %d (%s, split %d) delivered = %v, want %v (topic received %q)", e.offset, e.event.Topic, split, delivered, want, topic)
		}
	}
}

// A request a client abandoned can reach the handler after a later one; the
// per-connection seq keeps it from undoing the later scope.
func TestScopeHandler_ordersRequestsBySeq(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := mustNew(t, withEpoch(testEpoch))
		first := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		srv, handler := scopeServer(t, h, func(r *http.Request, owner any) error {
			if r.Header.Get("X-Seq") == "1" {
				once.Do(func() { close(first) })
				<-release
			}
			return nil
		})
		resp, sc := openStream(t, srv, srv.URL, http.Header{scopeHeader: {`["run:a"]`}})
		defer resp.Body.Close()
		hello, _ := readHello(t, sc)
		send := func(seq int, topics string) *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodPost, "/scope", strings.NewReader(fmt.Sprintf(`{"conn":%q,"seq":%d,"topics":%s}`, hello.Conn, seq, topics)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Seq", fmt.Sprint(seq))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w
		}
		var stale *httptest.ResponseRecorder
		done := make(chan struct{})
		go func() {
			stale = send(1, "[]")
			close(done)
		}()
		<-first
		if w := send(2, `["run:a"]`); w.Code != http.StatusOK {
			t.Fatalf("the later request answered %d %s, want 200", w.Code, w.Body.String())
		}
		close(release)
		<-done
		if stale.Code != http.StatusConflict {
			t.Errorf("the abandoned request answered %d %s, want 409", stale.Code, stale.Body.String())
		}
		h.mu.Lock()
		c := h.byConn[hello.Conn]
		holdsA := c != nil && c.scope.matches("run:a")
		h.mu.Unlock()
		if !holdsA {
			t.Error("the abandoned request undid the later scope")
		}
	})
}

// marksOf reads the stream until it has seen n id-only blocks and returns
// their ids.
func marksOf(t *testing.T, sc *bufio.Scanner, n int) []string {
	t.Helper()
	var marks []string
	var block []string
	start := time.Now()
	for len(marks) < n {
		if waited := time.Since(start); waited > 10*scopeMarkInterval {
			t.Fatalf("%d marks after %v, want %d", len(marks), waited, n)
		}
		if !sc.Scan() {
			t.Fatalf("stream ended after %d marks: %v", len(marks), sc.Err())
		}
		line := sc.Text()
		if line != "" {
			block = append(block, line)
			continue
		}
		if len(block) == 1 && strings.HasPrefix(block[0], "id: ") {
			marks = append(marks, strings.TrimPrefix(block[0], "id: "))
		}
		block = block[:0]
	}
	return marks
}

// A narrow cursor advances on marks, so a resume after more out-of-scope
// traffic than the ring holds still resumes.
func TestScope_marksKeepANarrowCursorInsideTheRing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := mustNew(t, withEpoch(testEpoch), WithReplay(1024), WithReplayTTL(replayTTL))
		srv := startServer(t, h, WithRequestScope("p"))
		header := http.Header{scopeHeader: {`["run:a"]`}}
		resp, sc := openStream(t, srv, srv.URL, header)
		readHello(t, sc)
		for range 1100 {
			if _, err := h.Publish(Event{Topic: "run:b", Data: []byte("x")}); err != nil {
				t.Fatal(err)
			}
		}
		// The immediate first mark may already have left mid-publish; the next
		// tick carries the head.
		time.Sleep(scopeMarkInterval)
		want := (Cursor{Epoch: testEpoch, Offset: 1100}).String()
		var last string
		for range 3 {
			if last = marksOf(t, sc, 1)[0]; last == want {
				break
			}
		}
		resp.Body.Close()
		if last != want {
			t.Fatalf("mark = %s, want %s", last, want)
		}
		header.Set("Last-Event-ID", last)
		again, sc2 := openStream(t, srv, srv.URL, header)
		defer again.Body.Close()
		if hello, _ := readHello(t, sc2); !hello.Resumed {
			t.Errorf("a resume from the mark = %s, want resumed", hello.Verdict)
		}
	})
}

// A connection that connected wide and was narrowed by a request gets marks
// too, since the live scope decides.
func TestScope_marksFollowTheLiveScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := mustNew(t, withEpoch(testEpoch), WithReplay(1024), WithReplayTTL(replayTTL))
		srv, handler := scopeServer(t, h, nil)
		resp, sc := openStream(t, srv, srv.URL, nil)
		defer resp.Body.Close()
		hello, _ := readHello(t, sc)
		if w := postScope(handler, http.MethodPost, "application/json", fmt.Sprintf(`{"conn":%q,"seq":1,"topics":["run:a"]}`, hello.Conn)); w.Code != http.StatusOK {
			t.Fatalf("narrowing answered %d %s", w.Code, w.Body.String())
		}
		for range 5 {
			if _, err := h.Publish(Event{Topic: "run:b", Data: []byte("x")}); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(scopeMarkInterval)
		if mark := marksOf(t, sc, 1)[0]; mark != (Cursor{Epoch: testEpoch, Offset: 5}).String() {
			t.Errorf("mark after narrowing = %s, want offset 5", mark)
		}
	})
}

// A mark is written only when the channel is empty, and never while the hub
// lock is held: a peer that stopped reading must not block Publish.
func TestScope_markWritesOutsideTheLock(t *testing.T) {
	h := mustNew(t, withEpoch(testEpoch))
	sub, err := h.subscribe(subRequest{scope: Topics("run:a")}, Cursor{}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer h.unsubscribe(sub.c)
	if _, err := h.Publish(Event{Topic: "run:b", Data: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	rw := &blockingWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
	sw := &Writer{w: rw, rc: http.NewResponseController(rw), timeout: time.Second}
	errc := make(chan error, 1)
	go func() { errc <- h.mark(sw, sub.c) }()
	<-rw.entered
	published := make(chan error, 1)
	go func() {
		_, err := h.Publish(Event{Topic: "run:b", Data: []byte("y")})
		published <- err
	}()
	select {
	case err := <-published:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked behind a mark write")
	}
	close(rw.release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if got, want := rw.buf.String(), "id: "+(Cursor{Epoch: testEpoch, Offset: 1}).String()+"\n\n"; got != want {
		t.Errorf("mark wrote %q, want %q", got, want)
	}

	if _, err := h.Publish(Event{Topic: "run:a", Data: []byte("queued")}); err != nil {
		t.Fatal(err)
	}
	quiet := &bytes.Buffer{}
	rec := &recorderWriter{ResponseRecorder: httptest.NewRecorder(), buf: quiet}
	if err := h.mark(&Writer{w: rec, rc: http.NewResponseController(rec), timeout: time.Second, lastOffset: 1}, sub.c); err != nil {
		t.Fatal(err)
	}
	if quiet.Len() != 0 {
		t.Errorf("a mark was written while a frame was queued: %q", quiet.String())
	}
}

type blockingWriter struct {
	header  http.Header
	entered chan struct{}
	release chan struct{}
	buf     bytes.Buffer
	once    sync.Once
}

func (b *blockingWriter) Header() http.Header { return b.header }
func (*blockingWriter) WriteHeader(int)       {}
func (*blockingWriter) Flush()                {}
func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return b.buf.Write(p)
}

type recorderWriter struct {
	*httptest.ResponseRecorder
	buf io.Writer
}

func (r *recorderWriter) Write(p []byte) (int, error) { return r.buf.Write(p) }

// nextBlock reads the stream's next block: the lines up to a blank one.
func nextBlock(t *testing.T, sc *bufio.Scanner) []string {
	t.Helper()
	var block []string
	for sc.Scan() {
		if sc.Text() == "" {
			if len(block) > 0 {
				return block
			}
			continue
		}
		block = append(block, sc.Text())
	}
	t.Fatalf("stream ended: %v", sc.Err())
	return nil
}

// A connection whose live scope is everything writes no marks, even after
// out-of-scope frames left its cursor behind while it was narrowed; a scoped
// one writes none at an offset it already received.
func TestScope_marksOnlyWhereTheCursorLags(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := mustNew(t, withEpoch(testEpoch), WithReplay(64), WithReplayTTL(replayTTL))
		srv, handler := scopeServer(t, h, nil)
		wide, sc := openStream(t, srv, srv.URL, nil)
		defer wide.Body.Close()
		hello, _ := readHello(t, sc)
		post := func(seq int, topics string) {
			t.Helper()
			body := fmt.Sprintf(`{"conn":%q,"seq":%d,"topics":%s}`, hello.Conn, seq, topics)
			if w := postScope(handler, http.MethodPost, "application/json", body); w.Code != http.StatusOK {
				t.Fatalf("scope %s answered %d", topics, w.Code)
			}
		}
		post(1, `["run:a"]`)
		if _, err := h.Publish(Event{Topic: "run:b", Data: []byte("b")}); err != nil {
			t.Fatal(err)
		}
		post(2, "null")
		time.Sleep(2 * scopeMarkInterval)
		if _, err := h.Publish(Event{Topic: "run:c", Data: []byte("c")}); err != nil {
			t.Fatal(err)
		}
		if block := nextBlock(t, sc); len(block) < 2 {
			t.Errorf("widened connection wrote %q before its next frame, want no mark", block)
		}

		narrow, sc2 := openStream(t, srv, srv.URL, http.Header{scopeHeader: {`["run:a"]`}})
		defer narrow.Body.Close()
		readHello(t, sc2)
		if _, err := h.Publish(Event{Topic: "run:a", Data: []byte("a2")}); err != nil {
			t.Fatal(err)
		}
		if block := nextBlock(t, sc2); len(block) < 2 {
			t.Fatalf("scoped connection: first block %q, want the frame", block)
		}
		time.Sleep(2 * scopeMarkInterval)
		if _, err := h.Publish(Event{Topic: "run:a", Data: []byte("a3")}); err != nil {
			t.Fatal(err)
		}
		if block := nextBlock(t, sc2); len(block) < 2 {
			t.Errorf("scoped connection wrote %q at an offset it already received, want no mark", block)
		}
	})
}

// A resumed hello with nothing to replay leaves the cursor at the presented
// offset, so the first mark goes out at once.
func TestScope_firstMarkIsImmediateOnResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := mustNew(t, withEpoch(testEpoch), WithReplay(64), WithReplayTTL(replayTTL))
		for range 3 {
			if _, err := h.Publish(Event{Topic: "run:b", Data: []byte("x")}); err != nil {
				t.Fatal(err)
			}
		}
		srv := startServer(t, h, WithRequestScope("p"))
		header := http.Header{
			scopeHeader:     {`["run:a"]`},
			"Last-Event-ID": {(Cursor{Epoch: testEpoch, Offset: 1}).String()},
		}
		resp, sc := openStream(t, srv, srv.URL, header)
		defer resp.Body.Close()
		if hello, _ := readHello(t, sc); !hello.Resumed {
			t.Fatalf("hello = %s, want resumed", hello.Verdict)
		}
		start := time.Now()
		if mark := marksOf(t, sc, 1)[0]; mark != (Cursor{Epoch: testEpoch, Offset: 3}).String() {
			t.Errorf("first mark = %s, want offset 3", mark)
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("the first mark waited %v, want none", waited)
		}
	})
}

// A filtered replay that ends below the head leaves the cursor at its last
// frame, so the first mark goes out at once.
func TestScope_firstMarkAfterAFilteredReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := mustNew(t, withEpoch(testEpoch), WithReplay(64), WithReplayTTL(replayTTL))
		for _, topic := range []string{"run:b", "run:a", "run:b", "run:b"} {
			if _, err := h.Publish(Event{Topic: topic, Data: []byte("x")}); err != nil {
				t.Fatal(err)
			}
		}
		srv := startServer(t, h, WithTopic("run:a"))
		header := http.Header{"Last-Event-ID": {(Cursor{Epoch: testEpoch, Offset: 1}).String()}}
		resp, sc := openStream(t, srv, srv.URL, header)
		defer resp.Body.Close()
		if hello, _ := readHello(t, sc); !hello.Resumed {
			t.Fatalf("hello = %s, want resumed", hello.Verdict)
		}
		start := time.Now()
		if mark := marksOf(t, sc, 1)[0]; mark != (Cursor{Epoch: testEpoch, Offset: 4}).String() {
			t.Errorf("first mark = %s, want offset 4", mark)
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("the first mark waited %v, want none", waited)
		}
	})
}

// A fresh connect's cursor is the hello's head, so the first mark waits for
// the head to move past it.
func TestScope_firstMarkAfterAFreshHelloIsPastItsHead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := mustNew(t, withEpoch(testEpoch), WithReplay(64), WithReplayTTL(replayTTL))
		for range 3 {
			if _, err := h.Publish(Event{Topic: "run:b", Data: []byte("x")}); err != nil {
				t.Fatal(err)
			}
		}
		srv := startServer(t, h, WithTopic("run:a"))
		resp, sc := openStream(t, srv, srv.URL, nil)
		defer resp.Body.Close()
		readHello(t, sc)
		time.Sleep(2 * scopeMarkInterval)
		if _, err := h.Publish(Event{Topic: "run:b", Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(scopeMarkInterval)
		if mark := marksOf(t, sc, 1)[0]; mark != (Cursor{Epoch: testEpoch, Offset: 4}).String() {
			t.Errorf("first mark = %s, want offset 4", mark)
		}
	})
}

// A set the server chooses is never encoded, so WithScope takes any topic and
// mints no conn.
func TestScope_withScopeTakesAnyTopic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := mustNew(t, withEpoch(testEpoch))
		srv := startServer(t, h, WithScope(Topics("run a", "run:\u00e9")))
		resp, sc := openStream(t, srv, srv.URL, nil)
		defer resp.Body.Close()
		hello, _ := readHello(t, sc)
		if hello.Conn != "" {
			t.Errorf("a WithScope hello carries conn %q, want none", hello.Conn)
		}
		if _, err := h.Publish(Event{Topic: "run a", Data: []byte("spaced")}); err != nil {
			t.Fatal(err)
		}
		if block := nextBlock(t, sc); !slices.Contains(block, "data: spaced") {
			t.Errorf("block %q, want the frame on topic \"run a\"", block)
		}
	})
}
