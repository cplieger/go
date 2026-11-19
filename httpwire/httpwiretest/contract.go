// Package httpwiretest checks an app's httpwire API from its handler tests:
// every answer against the row's declared shapes in the document the API
// runs with, and each owner package's sentinels against its code table.
package httpwiretest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
	"github.com/cplieger/go/httpwire/internal/wirelink"
)

const codeOutcomeUnknown = "outcome_unknown"

// Case is one request Contract serves.
type Case struct {
	// Request is built by the test, carrying its own principal.
	Request *http.Request
	// Row is a row name of the API's table, mount rows included.
	Row string
}

// SameShape names two principals of one shape (two users of one role, two
// keys of one scope) that own different records in the fixture store.
type SameShape struct {
	// A and B each return a copy of the request carrying their principal.
	A, B func(*http.Request) *http.Request
}

// Option configures Contract.
type Option func(*options)

type options struct {
	pairs []SameShape
}

// WithSameShape serves every case of a read row with Changes and no Personal
// as both principals, and fails the row when the guard gives them two shapes
// or their answers differ as JSON values (a List answer's cursors left out).
// Every such row then needs a case. It may be passed more than once; each
// pair runs over every such case.
func WithSameShape(s SameShape) Option {
	return func(o *options) { o.pairs = append(o.pairs, s) }
}

// Contract serves each case through api and fails t, naming the row, on any
// answer outside the row's shapes in the document api was built with (a
// status, body, media type, envelope, Wire-Version or X-Request-Id; a HEAD
// answer's body is not read). A case naming no row, a Stream row, or a
// request routed to another row fails, as does every write row, mount rows
// included, whose union holds outcome_unknown and that is not Keyed.
// docs/testing.md lists each check.
func Contract(t testing.TB, api *httpwire.API, cases []Case, opts ...Option) {
	t.Helper()
	if api == nil {
		t.Fatal("httpwiretest: Contract of a nil API")
		return
	}
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	c := newChecker(t, api)
	c.unkeyedOutcomes()
	covered := map[string]bool{}
	for _, cs := range cases {
		ep, body, ok := c.admit(cs)
		if !ok {
			continue
		}
		c.serve(ep, withBody(cs.Request, body))
		if len(o.pairs) == 0 || !sameShapeRow(ep) {
			continue
		}
		covered[ep.Name] = true
		for _, p := range o.pairs {
			c.sameShape(ep, cs.Request, body, p)
		}
	}
	if len(o.pairs) > 0 {
		c.uncovered(covered)
	}
}

// sameShapeRow reports whether WithSameShape covers ep.
func sameShapeRow(ep *httpwire.Endpoint) bool {
	return len(ep.Changes) > 0 && !ep.Personal
}

type checker struct {
	t       testing.TB
	api     *httpwire.API
	schema  *httpwire.Schema
	guard   httpwire.Guard
	catalog *httpwire.Catalog
	rows    map[string]*httpwire.Endpoint
	mux     *http.ServeMux
	version string
	prefix  string
	order   []*httpwire.Endpoint
}

func newChecker(t testing.TB, api *httpwire.API) *checker {
	b := wirelink.Inspect(api)
	tb := api.Table()
	c := &checker{t: t, api: api, version: b.Version, catalog: tb.Catalog, rows: map[string]*httpwire.Endpoint{}, mux: http.NewServeMux(), prefix: tb.Prefix}
	c.schema, _ = b.Schema.(*httpwire.Schema)
	c.guard, _ = b.Guard.(httpwire.Guard)
	add := func(ep *httpwire.Endpoint) {
		c.rows[ep.Name] = ep
		c.order = append(c.order, ep)
		c.mux.Handle(pattern(tb.Prefix, ep), http.NotFoundHandler())
	}
	for i := range tb.Endpoints {
		add(&tb.Endpoints[i])
	}
	for _, m := range tb.Mounts {
		for i := range m.Rows {
			add(&m.Rows[i])
		}
	}
	return c
}

// unkeyedOutcomes fails every write row that can answer outcome_unknown
// and is not Keyed.
func (c *checker) unkeyedOutcomes() {
	for _, ep := range c.order {
		if ep.Method != http.MethodGet && ep.Method != http.MethodHead && !ep.Keyed && slices.Contains(ep.Errors, codeOutcomeUnknown) {
			c.t.Errorf("httpwiretest: row %q is a write whose union holds %s, so a resend may run it twice; make the row Keyed", ep.Name, codeOutcomeUnknown)
		}
	}
}

// uncovered fails every row WithSameShape covers that no case served.
func (c *checker) uncovered(covered map[string]bool) {
	for _, ep := range c.order {
		if sameShapeRow(ep) && !covered[ep.Name] {
			c.t.Errorf("httpwiretest: row %q has Changes and no Personal, so WithSameShape needs a case of it; it has no case", ep.Name)
		}
	}
}

// admit finds a case's row and reads its request body once, so the request
// can be served more than once.
func (c *checker) admit(cs Case) (ep *httpwire.Endpoint, body []byte, ok bool) {
	ep, ok = c.rows[cs.Row]
	switch {
	case !ok:
		c.t.Errorf("httpwiretest: case for %q: the API's table has no row of that name", cs.Row)
		return nil, nil, false
	case cs.Request == nil:
		c.t.Errorf("httpwiretest: row %q: the case has no Request", cs.Row)
		return nil, nil, false
	case ep.Kind == httpwire.Stream:
		c.t.Errorf("httpwiretest: row %q is a Stream row, whose answer Contract cannot read", cs.Row)
		return nil, nil, false
	}
	if !c.reaches(ep, cs.Request) {
		c.t.Errorf("httpwiretest: row %q: the case's %s %s does not reach the row (%s %s)", cs.Row, cs.Request.Method, cs.Request.URL.Path, ep.Method, ep.Path)
		return nil, nil, false
	}
	if cs.Request.Body != nil {
		var err error
		if body, err = io.ReadAll(cs.Request.Body); err != nil {
			c.t.Errorf("httpwiretest: row %q: reading the case's body: %v", cs.Row, err)
			return nil, nil, false
		}
	}
	return ep, body, true
}

// reaches reports whether the API's routing sends r to ep, so a literal
// row's path never reaches a parameter row beside it.
func (c *checker) reaches(ep *httpwire.Endpoint, r *http.Request) bool {
	_, got := c.mux.Handler(r)
	return got == pattern(c.prefix, ep)
}

func pattern(prefix string, ep *httpwire.Endpoint) string { return ep.Method + " " + prefix + ep.Path }

// withBody is a copy of r reading body.
func withBody(r *http.Request, body []byte) *http.Request {
	c := r.Clone(r.Context())
	if r.Body != nil {
		c.Body = io.NopCloser(bytes.NewReader(body))
		c.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	return c
}

// answer is one served answer and the failure reporter naming it.
type answer struct {
	rec  *httptest.ResponseRecorder
	fail func(format string, args ...any)
}

func (c *checker) serve(ep *httpwire.Endpoint, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c.api.ServeHTTP(rec, r)
	a := answer{rec: rec, fail: func(format string, args ...any) {
		c.t.Errorf("httpwiretest: row %q, %s %s answered %d: %s", ep.Name, r.Method, r.URL.RequestURI(), rec.Code, fmt.Sprintf(format, args...))
	}}
	c.check(ep, r, a)
	return rec
}

func (c *checker) check(ep *httpwire.Endpoint, r *http.Request, a answer) {
	h := a.rec.Header()
	switch v := h.Get("Wire-Version"); {
	case v == "":
		a.fail("no Wire-Version header")
	case v != c.version:
		a.fail("Wire-Version %q; the API's is %q", v, c.version)
	}
	switch id := h.Get(httpserver.HeaderRequestID); {
	case id == "":
		a.fail("no X-Request-Id header")
	case !httpserver.ValidRequestID(id):
		a.fail("X-Request-Id %q is not a valid request id", id)
	}
	statuses := successes(ep)
	st := a.rec.Code
	resp, declared := statuses[st]
	switch {
	case declared && r.Method != http.MethodHead:
		c.success(ep, st, resp, a)
	case declared:
	case st >= 200 && st < 300, ep.Kind == httpwire.Redirect && st >= 300 && st < 400:
		a.fail("success status %d is not declared; the row declares %v", st, sortedKeys(statuses))
	case r.Method != http.MethodHead:
		c.refusal(ep, h.Get(httpserver.HeaderRequestID), a)
	}
}

// successes are ep's success statuses, each to its Response type (nil for
// none).
func successes(ep *httpwire.Endpoint) map[int]reflect.Type {
	out := map[int]reflect.Type{wirelink.Primary(ep): ep.Response}
	for _, s := range ep.Successes {
		out[s.Status] = s.Response
	}
	return out
}

func sortedKeys(m map[int]reflect.Type) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (c *checker) success(ep *httpwire.Endpoint, status int, resp reflect.Type, a answer) {
	body := a.rec.Body.Bytes()
	switch {
	case ep.Kind == httpwire.Redirect:
		if a.rec.Header().Get("Location") == "" {
			a.fail("a redirect with no Location")
		}
	case ep.Kind == httpwire.Raw && status == wirelink.Primary(ep):
		mediaIs(a, ep.ContentTypes[0], "the row declares")
	case ep.Kind == httpwire.Raw || resp == nil:
		if len(body) != 0 {
			a.fail("a body of %d bytes on a status the row declares none for", len(body))
		}
	default:
		if mediaIs(a, mediaJSON, "the row declares") {
			strict(a, body, resp)
			for _, fe := range c.schema.CheckBody(ep.Name, status, body) {
				a.fail("the body breaks the document at %q: %s (%s)", fe.Path, fe.Message, fe.Code)
			}
		}
	}
}

const mediaJSON = "application/json"

// mediaIs reports whether the answer's media type is want, failing it when
// not.
func mediaIs(a answer, want, whose string) bool {
	ct := a.rec.Header().Get("Content-Type")
	media, _, err := mime.ParseMediaType(ct)
	if err != nil || media != want {
		a.fail("Content-Type %q; %s %s", ct, whose, want)
		return false
	}
	return true
}

// strict decodes body into a new value of rt, refusing a member rt lacks;
// the schema check refuses data after the value.
func strict(a answer, body []byte, rt reflect.Type) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(reflect.New(rt).Interface()); err != nil {
		a.fail("the body does not decode into %s: %v", rt, err)
		return
	}
}

func (c *checker) refusal(ep *httpwire.Endpoint, id string, a answer) {
	if !mediaIs(a, mediaJSON, "an error envelope is") {
		return
	}
	env, err := httpwire.DecodeEnvelope(a.rec.Body.Bytes())
	if err != nil {
		a.fail("the body is not an error envelope: %v", err)
		return
	}
	if !slices.Contains(ep.Errors, env.Code) {
		a.fail("code %q is outside the row's union", env.Code)
	}
	if spec, ok := c.catalog.Lookup(env.Code); ok && spec.Status != a.rec.Code {
		a.fail("code %q is answered at %d by the catalog", env.Code, spec.Status)
	}
	if env.RequestID != id {
		a.fail("request_id %q is not the X-Request-Id %q", env.RequestID, id)
	}
}

// sameShape serves r as both principals of p with one request id and fails
// the row when the guard tells their shapes apart or their answers differ.
func (c *checker) sameShape(ep *httpwire.Endpoint, r *http.Request, body []byte, p SameShape) {
	id := httpserver.NewRequestID()
	as := func(f func(*http.Request) *http.Request) *http.Request {
		if f == nil {
			return nil
		}
		out := f(withBody(r, body))
		if out == nil {
			return nil
		}
		return out.WithContext(httpserver.WithRequestID(out.Context(), id))
	}
	ra, rb := as(p.A), as(p.B)
	if ra == nil || rb == nil {
		c.t.Errorf("httpwiretest: row %q: a SameShape principal function is nil or answered a nil request", ep.Name)
		return
	}
	if c.guard != nil {
		if sa, sb := c.guard.Shape(ra), c.guard.Shape(rb); sa.Principal != sb.Principal || !slices.Equal(sa.Scopes, sb.Scopes) {
			c.t.Errorf("httpwiretest: row %q: the guard gives the two SameShape principals different shapes, %+v and %+v", ep.Name, sa, sb)
			return
		}
	}
	a, b := c.serve(ep, ra), c.serve(ep, rb)
	if a.Code != b.Code || !sameBody(ep.List, a.Body.Bytes(), b.Body.Bytes()) {
		c.t.Errorf("httpwiretest: row %q: the two SameShape principals' answers differ, so the row's reads differ per principal and it must be Personal: %d %s and %d %s",
			ep.Name, a.Code, clip(a.Body.Bytes()), b.Code, clip(b.Body.Bytes()))
	}
}

// sameBody compares two bodies as JSON values, a List answer's sealed
// cursors left out, and as bytes when either is not JSON.
func sameBody(list bool, a, b []byte) bool {
	va, errA := jsonValue(a, list)
	vb, errB := jsonValue(b, list)
	if errA != nil || errB != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(va, vb)
}

func jsonValue(body []byte, list bool) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if m, ok := v.(map[string]any); ok && list {
		delete(m, "next_cursor")
		delete(m, "prev_cursor")
	}
	return v, nil
}

func clip(b []byte) string {
	const most = 256
	if len(b) > most {
		return fmt.Sprintf("%q…", b[:most])
	}
	return fmt.Sprintf("%q", b)
}
