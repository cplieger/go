package httpwire_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/go/httpwire"
)

type (
	sessionToken string
	delegateName string
	selfFlag     string
	machineSlot  int64
	// envTag is four lowercase letters, held by its own UnmarshalText,
	// which names its own field code.
	envTag       string
	agentHeaders struct {
		As      *delegateName `header:"grelot-as"`
		Session sessionToken  `header:"Grelot-Session"`
		Self    selfFlag      `header:"Grelot-Self,omitempty"`
		Env     envTag        `header:"X-Env,omitempty"`
		Slot    machineSlot   `header:"X-Slot,omitzero"`
		Count   uint64        `header:"X-Count,omitempty"`
		Fast    bool          `header:"X-Fast,omitempty"`
	}
	agentParams struct {
		Kind itemKind `json:"kind"`
	}
	agentQuery struct {
		Limit itemNum `json:"limit"`
	}
	agentBody struct {
		Name label `json:"name"`
	}
	agentAct struct {
		ID string `json:"id"`
	}
	seenEvent struct {
		At int64 `json:"at"`
	}
)

func (sessionToken) MaxBytes() int            { return 8 }
func (delegateName) MaxBytes() int            { return 4 }
func (selfFlag) MaxBytes() int                { return 1 }
func (machineSlot) Range() (lo, hi int64)     { return 1, 9 }
func (envTag) MaxBytes() int                  { return 4 }
func (envTag) Refusal() string                { return "env_tag" }
func (e envTag) MarshalText() ([]byte, error) { return []byte(e), nil }
func (e *envTag) UnmarshalText(b []byte) error {
	if len(b) != 4 || strings.Trim(string(b), "abcdefghijklmnopqrstuvwxyz") != "" {
		return errors.New("not four lowercase letters")
	}
	*e = envTag(b)
	return nil
}

// headerTable is one row of each kind taking agentHeaders: a JSON read, a
// JSON write with every other decoded part, a Raw read, a Stream and a
// Keyed write.
func headerTable(t *testing.T) *httpwire.Table {
	t.Helper()
	h := reflect.TypeFor[agentHeaders]()
	return &httpwire.Table{
		Prefix: "/api", Catalog: fixtureCatalog(t),
		Endpoints: []httpwire.Endpoint{
			{
				Name: "agents.get", Method: http.MethodGet, Path: "/agents", Summary: "Read the agent", Description: "Reads the calling agent.",
				Headers: h, Access: public,
			},
			{
				Name: "agents.post", Method: http.MethodPost, Path: "/agents/{kind}", Summary: "Post to an agent",
				Description: "Posts one message to an agent.", Params: reflect.TypeFor[agentParams](), Query: reflect.TypeFor[agentQuery](),
				Request: reflect.TypeFor[agentBody](), Headers: h, Access: public,
			},
			{
				Name: "agents.file", Method: http.MethodGet, Path: "/agents/file", Summary: "Download the agent's file",
				Description: "Downloads the calling agent's file.", Kind: httpwire.Raw, ContentType: "text/plain", Headers: h, Access: public,
			},
			{
				Name: "agents.watch", Method: http.MethodGet, Path: "/agents/watch", Summary: "Watch the agent",
				Description: "Streams the calling agent's events.", Kind: httpwire.Stream, Headers: h, Access: public,
				Events: []httpwire.Event{{Name: "seen", Data: reflect.TypeFor[seenEvent]()}},
			},
			{
				Name: "agents.act", Method: http.MethodPost, Path: "/agents/act", Summary: "Act as the agent",
				Description: "Runs one action as the calling agent.", Request: reflect.TypeFor[agentBody](), Response: reflect.TypeFor[agentAct](),
				Headers: h, Access: session, Keyed: true,
			},
		},
	}
}

func headerDoc(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/openapi/headers.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// editHeaderDoc answers the header document after f edits one operation's
// parameter of that name.
func editHeaderDoc(t *testing.T, path, method, name string, f func(params []any, i int) []any) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(headerDoc(t), &doc); err != nil {
		t.Fatal(err)
	}
	op := doc["paths"].(map[string]any)[path].(map[string]any)[method].(map[string]any)
	params := op["parameters"].([]any)
	i := slices.IndexFunc(params, func(p any) bool { return p.(map[string]any)["name"] == name })
	if i < 0 {
		t.Fatalf("the document's %s %s has no parameter %q", method, path, name)
	}
	op["parameters"] = f(params, i)
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// headerAPI serves headerTable, recording what each handler was given.
type headerAPI struct {
	api      *httpwire.API
	headers  []agentHeaders // each JSON handler's Call.Headers
	sessions []string       // each Raw or Stream handler's Grelot-Session
	acts     int
	mu       sync.Mutex
}

func (h *headerAPI) record(c agentHeaders) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.headers = append(h.headers, c)
}

func (h *headerAPI) recordRaw(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions = append(h.sessions, r.Header.Get("Grelot-Session"))
}

func (h *headerAPI) acted() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.acts
}

func (h *headerAPI) runs() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.headers) + len(h.sessions)
}

func (h *headerAPI) bindings() map[string]httpwire.Binding {
	return map[string]httpwire.Binding{
		"agents.get": httpwire.Bind("agents.get", func(_ context.Context, c httpwire.Call[noP, noQ, noB, agentHeaders]) (httpwire.None, error) {
			h.record(c.Headers)
			return httpwire.None{}, nil
		}),
		"agents.post": httpwire.Bind("agents.post", func(_ context.Context, c httpwire.Call[agentParams, agentQuery, agentBody, agentHeaders]) (httpwire.None, error) {
			h.record(c.Headers)
			return httpwire.None{}, nil
		}),
		"agents.file": httpwire.BindRaw("agents.file", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.recordRaw(r)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "file")
		})),
		"agents.watch": httpwire.BindStream("agents.watch", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.recordRaw(r)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: seen\ndata: {\"at\":1}\n\n")
		})),
		"agents.act": httpwire.Bind("agents.act", func(_ context.Context, c httpwire.Call[noP, noQ, agentBody, agentHeaders]) (agentAct, error) {
			h.record(c.Headers)
			h.mu.Lock()
			defer h.mu.Unlock()
			h.acts++
			return agentAct{ID: string(c.Headers.Session) + "-" + strconv.Itoa(h.acts)}, nil
		}),
	}
}

// buildHeaderAPI builds tb over doc, swap replacing the bindings it names.
func buildHeaderAPI(tb *httpwire.Table, doc []byte, swap map[string]httpwire.Binding) (*headerAPI, error) {
	h := &headerAPI{}
	binds := h.bindings()
	maps.Copy(binds, swap)
	var bs []httpwire.Binding
	for _, name := range slices.Sorted(maps.Keys(binds)) {
		bs = append(bs, binds[name])
	}
	api, err := httpwire.New(tb, bs,
		httpwire.WithSchema(doc),
		httpwire.WithGuard(fixtureGuard{}),
		httpwire.WithCredential(func(*http.Request) string { return "s:a" }),
		httpwire.WithCursorOwner(func(*http.Request) string { return "u:a" }),
		httpwire.WithHold(func(next http.Handler) http.Handler { return next }),
	)
	h.api = api
	return h, err
}

func newHeaderAPI(t *testing.T) *headerAPI {
	t.Helper()
	h, err := buildHeaderAPI(headerTable(t), headerDoc(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// get serves GET target with the header pairs given, each added in turn.
func (h *headerAPI) get(target string, header ...string) *httptest.ResponseRecorder {
	return answer(h.api, request(http.MethodGet, target, "", header...))
}

func TestHeadersDecode(t *testing.T) {
	t.Run("every field type decodes", func(t *testing.T) {
		h := newHeaderAPI(t)
		rec := h.get("/api/agents", "Grelot-Session", "abc", "Grelot-As", "bot", "Grelot-Self", "1", "X-Env", "prod", "X-Slot", "9",
			"X-Count", "18446744073709551615", "X-Fast", "true", "X-Other", "not a field")
		if rec.Code != http.StatusNoContent || len(h.headers) != 1 {
			t.Fatalf("answer %d %q after %d runs", rec.Code, rec.Body.String(), len(h.headers))
		}
		got := h.headers[0]
		if got.As == nil || *got.As != "bot" {
			t.Errorf("As = %v, want bot", got.As)
		}
		got.As = nil
		if want := (agentHeaders{Session: "abc", Self: "1", Env: "prod", Slot: 9, Count: 1<<64 - 1, Fast: true}); got != want {
			t.Errorf("Headers = %+v, want %+v", got, want)
		}
	})
	t.Run("optional headers left out stay zero", func(t *testing.T) {
		h := newHeaderAPI(t)
		rec := h.get("/api/agents", "Grelot-Session", "abc")
		if rec.Code != http.StatusNoContent || len(h.headers) != 1 || h.headers[0] != (agentHeaders{Session: "abc"}) {
			t.Errorf("answer %d, Headers %+v", rec.Code, h.headers)
		}
	})
	t.Run("a header name in any case", func(t *testing.T) {
		h := newHeaderAPI(t)
		srv := httptest.NewServer(h.api)
		t.Cleanup(srv.Close)
		r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/agents", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header["grelot-session"] = []string{"abc"}
		r.Header["GRELOT-AS"] = []string{"bot"}
		resp, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent || len(h.headers) != 1 || h.headers[0].Session != "abc" || h.headers[0].As == nil || *h.headers[0].As != "bot" {
			t.Errorf("answer %d, Headers %+v", resp.StatusCode, h.headers)
		}
	})
	for name, tc := range map[string]struct {
		header []string
		want   []string
	}{
		"a required header missing":               {nil, []string{"Grelot-Session=required"}},
		"a header given twice":                    {[]string{"Grelot-Session", "abc", "Grelot-Session", "abd"}, []string{"Grelot-Session=malformed"}},
		"a string over MaxBytes":                  {[]string{"Grelot-Session", strings.Repeat("s", 9)}, []string{"Grelot-Session=range"}},
		"a string under the document's minLength": {[]string{"Grelot-Session", "ab"}, []string{"Grelot-Session=range"}},
		"invalid UTF-8":                           {[]string{"Grelot-Session", "ab\xff"}, []string{"Grelot-Session=malformed"}},
		"a field whose tag is not canonical":      {[]string{"Grelot-Session", "abc", "Grelot-As", "robot"}, []string{"Grelot-As=range"}},
		"a value outside the document's enum":     {[]string{"Grelot-Session", "abc", "Grelot-Self", "2"}, []string{"Grelot-Self=enum"}},
		"an integer below Range":                  {[]string{"Grelot-Session", "abc", "X-Slot", "0"}, []string{"X-Slot=range"}},
		"an integer above Range":                  {[]string{"Grelot-Session", "abc", "X-Slot", "10"}, []string{"X-Slot=range"}},
		"an integer with a leading zero":          {[]string{"Grelot-Session", "abc", "X-Slot", "07"}, []string{"X-Slot=malformed"}},
		"an empty integer":                        {[]string{"Grelot-Session", "abc", "X-Slot", ""}, []string{"X-Slot=malformed"}},
		"a uint64 below zero":                     {[]string{"Grelot-Session", "abc", "X-Count", "-1"}, []string{"X-Count=malformed"}},
		"past uint64":                             {[]string{"Grelot-Session", "abc", "X-Count", "18446744073709551616"}, []string{"X-Count=range"}},
		"a bool in another spelling":              {[]string{"Grelot-Session", "abc", "X-Fast", "True"}, []string{"X-Fast=malformed"}},
		"a text its type refuses":                 {[]string{"Grelot-Session", "abc", "X-Env", "PROD"}, []string{"X-Env=env_tag"}},
		"a text over its MaxBytes":                {[]string{"Grelot-Session", "abc", "X-Env", "prods"}, []string{"X-Env=env_tag"}},
		"each fault at once":                      {[]string{"X-Slot", "0", "X-Fast", "no"}, []string{"Grelot-Session=required", "X-Fast=malformed", "X-Slot=range"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHeaderAPI(t)
			rec := h.get("/api/agents", tc.header...)
			env := envelopeOf(t, rec)
			if rec.Code != http.StatusBadRequest || env.Code != "bad_header" || !slices.Equal(fieldCodes(env), tc.want) {
				t.Errorf("answer %d %s %v, want 400 bad_header %v", rec.Code, env.Code, fieldCodes(env), tc.want)
			}
			if h.runs() != 0 {
				t.Error("the handler ran")
			}
		})
	}
}

// A Raw or Stream handler reads its headers itself, after the binder has
// held them to the row's Headers.
func TestHeadersCheckedBeforeARawOrStreamHandler(t *testing.T) {
	for name, target := range map[string]string{"Raw": "/api/agents/file", "Stream": "/api/agents/watch"} {
		t.Run(name+" refused", func(t *testing.T) {
			h := newHeaderAPI(t)
			rec := h.get(target, "X-Slot", "0")
			env := envelopeOf(t, rec)
			if rec.Code != http.StatusBadRequest || env.Code != "bad_header" || !slices.Equal(fieldCodes(env), []string{"Grelot-Session=required", "X-Slot=range"}) {
				t.Errorf("answer %d %s %v, want 400 bad_header", rec.Code, env.Code, fieldCodes(env))
			}
			if h.runs() != 0 {
				t.Error("the handler ran")
			}
		})
		t.Run(name+" served", func(t *testing.T) {
			h := newHeaderAPI(t)
			rec := h.get(target, "Grelot-Session", "abc")
			if rec.Code != http.StatusOK || !slices.Equal(h.sessions, []string{"abc"}) {
				t.Errorf("answer %d %q, the handler read %v", rec.Code, rec.Body.String(), h.sessions)
			}
		})
	}
}

// The headers decode after the path and the query and before the body.
func TestHeadersDecodeOrder(t *testing.T) {
	post := func(h *headerAPI, target, body string, header ...string) *httptest.ResponseRecorder {
		return answer(h.api, request(http.MethodPost, target, body, header...))
	}
	for name, tc := range map[string]struct {
		target, body string
		header       []string
		status       int
		code         string
	}{
		"a path naming nothing first":       {"/api/agents/tape?limit=1", `{"name":"a"}`, nil, http.StatusNotFound, "not_found"},
		"the query before the headers":      {"/api/agents/book?limit=0", `{"name":"a"}`, nil, http.StatusBadRequest, "bad_query"},
		"the headers before the body":       {"/api/agents/book?limit=1", `{`, nil, http.StatusBadRequest, "bad_header"},
		"the headers before the media type": {"/api/agents/book?limit=1", `{"name":"a"}`, []string{"Content-Type", "text/plain"}, http.StatusBadRequest, "bad_header"},
		"the body after the headers":        {"/api/agents/book?limit=1", `{`, []string{"Grelot-Session", "abc"}, http.StatusBadRequest, "bad_body"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHeaderAPI(t)
			rec := post(h, tc.target, tc.body, tc.header...)
			if rec.Code != tc.status || codeOf(t, rec) != tc.code {
				t.Errorf("answer %d %s, want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}
	t.Run("every part decodes", func(t *testing.T) {
		h := newHeaderAPI(t)
		rec := post(h, "/api/agents/book?limit=1", `{"name":"a"}`, "Grelot-Session", "abc")
		if rec.Code != http.StatusNoContent || len(h.headers) != 1 || h.headers[0] != (agentHeaders{Session: "abc"}) {
			t.Errorf("answer %d %s, Headers %+v", rec.Code, rec.Body, h.headers)
		}
	})
}

// A Keyed row's fingerprint covers the decoded headers, so a resend that
// acts for another delegate is not the request that ran.
func TestHeadersInTheFingerprint(t *testing.T) {
	h := newHeaderAPI(t)
	act := func(header ...string) *httptest.ResponseRecorder {
		return answer(h.api, keyedRequest(http.MethodPost, "/api/agents/act", `{"name":"a"}`, `"k"`, header...))
	}
	first := act("Grelot-Session", "abc", "Grelot-As", "bot")
	if first.Code != http.StatusOK || h.acted() != 1 {
		t.Fatalf("first act %d %s", first.Code, first.Body)
	}
	for name, header := range map[string][]string{
		"another header value": {"Grelot-Session", "abc", "Grelot-As", "bat"},
		"a header left out":    {"Grelot-Session", "abc"},
		"another session":      {"Grelot-Session", "abd", "Grelot-As", "bot"},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := act(header...); codeOf(t, rec) != "idempotency_key_reused" || h.acted() != 1 {
				t.Errorf("answer %d %s after %d runs, want idempotency_key_reused and one run", rec.Code, rec.Body, h.acted())
			}
		})
	}
	t.Run("the same headers and one that is no field", func(t *testing.T) {
		rec := act("Grelot-Session", "abc", "Grelot-As", "bot", "X-Other", "x")
		if rec.Code != http.StatusOK || rec.Body.String() != first.Body.String() || h.acted() != 1 {
			t.Errorf("answer %d %s after %d runs, want the replay", rec.Code, rec.Body, h.acted())
		}
	})
}

func TestUnionOfARowWithHeaders(t *testing.T) {
	t.Run("a table row", func(t *testing.T) {
		tb := fixtureTable(t)
		row(tb, "jobs.list").Headers = reflect.TypeFor[agentHeaders]()
		got, err := httpwire.Derive(tb, guardCodes)
		if err != nil {
			t.Fatal(err)
		}
		want := join([]string{"bad_query", "bad_header", "idempotency_key_unsupported", "upgrade_required", "store_unavailable", "auth_required"}, everyRow)
		if e := row(got, "jobs.list").Errors; !slices.Equal(e, want) {
			t.Errorf("Errors = %v, want %v", e, want)
		}
	})
	t.Run("a mount row, whose handler answers its own refusals", func(t *testing.T) {
		before, err := httpwire.Derive(fixtureTable(t), guardCodes)
		if err != nil {
			t.Fatal(err)
		}
		tb := fixtureTable(t)
		row(tb, "auth.keys").Headers = reflect.TypeFor[agentHeaders]()
		got, err := httpwire.Derive(tb, guardCodes)
		if err != nil {
			t.Fatal(err)
		}
		if e, want := row(got, "auth.keys").Errors, row(before, "auth.keys").Errors; !slices.Equal(e, want) {
			t.Errorf("Errors = %v, want the row's union without Headers %v", e, want)
		}
	})
}

// oneHeader is a Headers struct of one field of type ft, tagged tag.
func oneHeader(ft reflect.Type, tag string) reflect.Type {
	return reflect.StructOf([]reflect.StructField{{Name: "V", Type: ft, Tag: reflect.StructTag(tag)}})
}

func headers(t reflect.Type) func(*httpwire.Table) {
	return edit("jobs.list", func(ep *httpwire.Endpoint) { ep.Headers = t })
}

func TestDeriveRefusesHeaders(t *testing.T) {
	text := reflect.TypeFor[sessionToken]()
	cases := map[string]refusal{
		"the table's ClientHeader": {
			headers(oneHeader(text, `header:"fixture-version"`)),
			[]string{`row "jobs.list": Headers field "Fixture-Version" names the table's ClientHeader`},
		},
		"a field with no header tag": {
			headers(reflect.TypeFor[struct{ V sessionToken }]()),
			[]string{`row "jobs.list": Headers field V names no header`},
		},
		"a header tag with no name": {
			headers(oneHeader(text, `header:",omitempty"`)),
			[]string{`row "jobs.list": Headers field V names no header`},
		},
		"a name outside the token grammar": {
			headers(oneHeader(text, `header:"Bad Header"`)),
			[]string{`row "jobs.list": Headers field V names "Bad Header", which is not a header name`},
		},
		"one header named twice": {headers(reflect.TypeFor[struct {
			A sessionToken `header:"X-Thing"`
			B sessionToken `header:"x-thing"`
		}]()), []string{`row "jobs.list": Headers field "X-Thing" is declared twice`}},
		"a field outside the binder set": {
			headers(oneHeader(reflect.TypeFor[float64](), `header:"X-Thing"`)),
			[]string{`row "jobs.list": Headers field "X-Thing" is float64, outside the types a header value parses into`},
		},
		"a list": {
			headers(oneHeader(reflect.TypeFor[heldPaths](), `header:"X-Thing"`)),
			[]string{`row "jobs.list": Headers field "X-Thing" is a list; a header takes one value`},
		},
		"a cursor": {
			headers(oneHeader(reflect.TypeFor[httpwire.Cursor](), `header:"X-Thing"`)),
			[]string{`row "jobs.list": Headers field "X-Thing" is an httpwire.Cursor`},
		},
		"a Refusal outside the code grammar": {
			headers(oneHeader(reflect.TypeFor[badRefusal](), `header:"X-Thing"`)),
			[]string{`row "jobs.list"`, `Refusal "Not-A-Code"`},
		},
		"Headers not a struct": {headers(reflect.TypeFor[string]()), []string{`row "jobs.list": Headers string is not a struct`}},
		"Headers is None":      {headers(reflect.TypeFor[httpwire.None]()), []string{`row "jobs.list": Headers is httpwire.None`}},
		"a Keyed row's header that decodes itself one way": {edit("jobs.start", func(ep *httpwire.Endpoint) {
			ep.Headers = oneHeader(reflect.TypeFor[keyList](), `header:"X-Thing"`)
		}), []string{`row "jobs.start": a Keyed row's fingerprint marshals the decoded request, but httpwire_test.keyList decodes itself with no MarshalText`}},
		"a mount row naming an owned header": {
			edit("auth.keys", func(ep *httpwire.Endpoint) { ep.Headers = oneHeader(text, `header:"Authorization"`) }),
			[]string{`row "auth.keys": Headers field "Authorization" names a header httpwire or the platform owns`},
		},
	}
	// RFC 9110 sections 7.6.1 and 8, httputil's hop-by-hop list, and the
	// headers the binder reads itself.
	for tag, canonical := range map[string]string{
		"Content-Type": "Content-Type", "content-length": "Content-Length", "Idempotency-Key": "Idempotency-Key", "If-Match": "If-Match",
		"Authorization": "Authorization", "Cookie": "Cookie", "Host": "Host", "Connection": "Connection", "Keep-Alive": "Keep-Alive",
		"Proxy-Connection": "Proxy-Connection", "Proxy-Authenticate": "Proxy-Authenticate", "Proxy-Authorization": "Proxy-Authorization",
		"TE": "Te", "Trailer": "Trailer", "Transfer-Encoding": "Transfer-Encoding", "Upgrade": "Upgrade", "Wire-Version": "Wire-Version",
		"X-Request-ID": "X-Request-Id",
	} {
		cases["the owned header "+tag] = refusal{
			headers(oneHeader(text, `header:"`+tag+`"`)),
			[]string{`row "jobs.list": Headers field "` + canonical + `" names a header httpwire or the platform owns`},
		}
	}
	runRefusals(t, cases)
}

func TestNewHoldsHeadersToTheBindingAndTheDocument(t *testing.T) {
	agentsGet := func(h httpwire.Handler[noP, noQ, noB, noH, httpwire.None]) httpwire.Binding {
		return httpwire.Bind("agents.get", h)
	}
	param := func(f func(p map[string]any)) func(params []any, i int) []any {
		return func(params []any, i int) []any {
			f(params[i].(map[string]any))
			return params
		}
	}
	drop := func(params []any, i int) []any { return slices.Delete(params, i, i+1) }
	for name, tc := range map[string]struct {
		table func(*httpwire.Table)
		doc   func(t *testing.T) []byte
		swap  map[string]httpwire.Binding
		want  string
	}{
		"a binding's H of None on a row with Headers": {
			swap: map[string]httpwire.Binding{"agents.get": agentsGet(func(context.Context, httpwire.Call[noP, noQ, noB, noH]) (httpwire.None, error) {
				return httpwire.None{}, nil
			})},
			want: `row "agents.get": the binding's H is httpwire.None, but the row's Headers is httpwire_test.agentHeaders`,
		},
		"a binding's H on a row with no Headers": {
			table: func(tb *httpwire.Table) { row(tb, "agents.get").Headers = nil },
			doc: func(t *testing.T) []byte {
				var doc map[string]any
				if err := json.Unmarshal(headerDoc(t), &doc); err != nil {
					t.Fatal(err)
				}
				delete(doc["paths"].(map[string]any)["/api/agents"].(map[string]any)["get"].(map[string]any), "parameters")
				b, _ := json.Marshal(doc)
				return b
			},
			want: `row "agents.get": the binding's H is httpwire_test.agentHeaders, but the row's Headers is none (httpwire.None)`,
		},
		"a field with no header parameter": {
			doc:  func(t *testing.T) []byte { return editHeaderDoc(t, "/api/agents", "get", "X-Fast", drop) },
			want: `row "agents.get": Headers field "X-Fast" has no header parameter in the document`,
		},
		"a required field whose parameter is optional": {
			doc: func(t *testing.T) []byte {
				return editHeaderDoc(t, "/api/agents", "get", "Grelot-Session", param(func(p map[string]any) { p["required"] = false }))
			},
			want: `row "agents.get": Headers field "Grelot-Session" is required, but the document's parameter is optional`,
		},
		"an optional field whose parameter is required": {
			doc: func(t *testing.T) []byte {
				return editHeaderDoc(t, "/api/agents", "get", "X-Fast", param(func(p map[string]any) { p["required"] = true }))
			},
			want: `row "agents.get": Headers field "X-Fast" is optional, but the document's parameter is required`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			tb := headerTable(t)
			if tc.table != nil {
				tc.table(tb)
			}
			doc := headerDoc(t)
			if tc.doc != nil {
				doc = tc.doc(t)
			}
			_, err := buildHeaderAPI(tb, doc, tc.swap)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("New answered %v, want a refusal holding %s", err, tc.want)
			}
		})
	}
}

// Header names are case-insensitive (RFC 9110 section 5.1), so a document
// declares each once whatever its case, and CheckParam finds it by any.
func TestSchemaHeaderNamesIgnoreCase(t *testing.T) {
	t.Run("one header declared twice", func(t *testing.T) {
		doc := editHeaderDoc(t, "/api/agents", "get", "X-Fast", func(params []any, i int) []any {
			return append(params, map[string]any{"in": "header", "name": "x-fast", "required": false, "schema": map[string]any{"type": "boolean"}})
		})
		if _, err := httpwire.ParseSchema(doc); err == nil || !strings.Contains(err.Error(), `parameter header "x-fast" declared twice`) {
			t.Errorf("ParseSchema = %v, want the second spelling refused", err)
		}
	})
	t.Run("CheckParam by another case", func(t *testing.T) {
		s, err := httpwire.ParseSchema(headerDoc(t))
		if err != nil {
			t.Fatal(err)
		}
		for name, tc := range map[string]struct {
			header string
			value  any
			want   string
		}{
			"the document's spelling refused": {"grelot-self", "2", "enum"},
			"the canonical spelling refused":  {"Grelot-Self", "2", "enum"},
			"another spelling accepted":       {"GRELOT-SELF", "1", ""},
		} {
			t.Run(name, func(t *testing.T) {
				e := s.CheckParam("agents.get", "header", tc.header, tc.value)
				if got := fieldErrorCode(e); got != tc.want {
					t.Errorf("CheckParam = %+v, want code %q", e, tc.want)
				}
			})
		}
	})
}

func fieldErrorCode(e *httpwire.FieldError) string {
	if e == nil {
		return ""
	}
	return e.Code
}
