package httpwire

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/go/httpserver/v2"
)

// API serves one checked table under its Prefix. It is safe for concurrent
// use.
type API struct {
	table   *Table
	mux     *http.ServeMux
	logger  *slog.Logger
	schema  *Schema
	sealer  *sealer
	idem    *keyedRuns
	version string
	quiet   quietState
	o       options
}

// New checks t against its bindings and options and builds its API. Its
// error lists every refusal at once, Derive's among them, so an app fails
// at start and never at a request.
func New(t *Table, bs []Binding, opts ...Option) (*API, error) {
	if t == nil {
		return nil, errors.New("httpwire: New of a nil table")
	}
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	var guardCodes func(Access) []string
	if o.guard != nil {
		guardCodes = o.guard.Codes
	}
	d, errs := derive(t, guardCodes)
	c := &newChecker{t: d, o: &o, errs: errs}
	bound := c.bindings(bs)
	c.options()
	schema := c.document()
	s := c.sealer()
	lim, problems := o.idem.limits()
	for _, p := range problems {
		c.fail("WithIdempotency: %s", p)
	}
	if len(c.errs) > 0 {
		return nil, errors.Join(c.errs...)
	}
	if o.spoolDir != "" {
		if err := cleanSpool(o.spoolDir); err != nil {
			return nil, fmt.Errorf("httpwire: WithSpoolDir: %w", err)
		}
	}
	a := &API{
		table: d, logger: o.logger, schema: schema, sealer: s, version: WireVersion(o.schema), o: o, mux: http.NewServeMux(),
		idem: newKeyedRuns(o.idem, lim, d),
	}
	for i := range d.Endpoints {
		ep := &d.Endpoints[i]
		a.mux.Handle(ep.Method+" "+ep.Path, &route{a.rowHandler(ep, bound[ep.Name])})
	}
	for _, m := range d.Mounts {
		for i := range m.Rows {
			a.mux.Handle(m.Rows[i].Method+" "+m.Rows[i].Path, &route{a.mountHandler(&m.Rows[i], m.Handler)})
		}
	}
	return a, nil
}

// Table answers a copy of the checked table, each row's Errors filled.
func (a *API) Table() *Table { return cloneTable(a.table) }

// route marks a handler the API registered, so ServeHTTP can tell a row
// from the mux's own not-found, redirect and 405 answers.
type route struct{ http.Handler }

// ServeHTTP answers a row, or method_not_allowed with Allow when the path
// is a row's under another method, or not_found. Every answer carries
// Wire-Version and the request's X-Request-Id.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerWireVersion, a.version)
	r = withRequestID(w, r)
	if h, _ := a.mux.Handler(r); isRoute(h) {
		a.mux.ServeHTTP(w, r)
		return
	}
	if allow := a.allowed(r); allow != "" {
		w.Header().Set("Allow", allow)
		a.table.Catalog.WriteError(w, r, &Error{Code: "method_not_allowed", Message: "the path does not take this method"})
		return
	}
	a.table.Catalog.WriteError(w, r, &Error{Code: codeNotFound, Message: "no route matches the path"})
}

// withRequestID gives the answer and r's context one request id: the
// context's, else a valid inbound X-Request-Id, else a fresh one.
func withRequestID(w http.ResponseWriter, r *http.Request) *http.Request {
	id := httpserver.RequestIDFromContext(r.Context())
	if httpserver.ValidRequestID(id) {
		w.Header().Set(httpserver.HeaderRequestID, id)
		return r
	}
	if id = r.Header.Get(httpserver.HeaderRequestID); !httpserver.ValidRequestID(id) {
		id = httpserver.NewRequestID()
	}
	w.Header().Set(httpserver.HeaderRequestID, id)
	return r.WithContext(httpserver.WithRequestID(r.Context(), id))
}

func isRoute(h http.Handler) bool {
	_, ok := h.(*route)
	return ok
}

// allowMethods is sorted, as Allow lists them.
var allowMethods = []string{http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodPatch, http.MethodPost, http.MethodPut}

// allowed lists the methods some row answers on r's path; the mux matches
// a GET pattern for HEAD too.
func (a *API) allowed(r *http.Request) string {
	var allow []string
	for _, m := range allowMethods {
		probe := r.WithContext(r.Context())
		probe.Method = m
		if h, _ := a.mux.Handler(probe); isRoute(h) {
			allow = append(allow, m)
		}
	}
	return strings.Join(allow, ", ")
}

// rowHandler is ep's handler under the binder's order: the cross-origin
// check on a write, the client floor, maintenance, the outage, the guard, the principal's
// credential, the key rate, the held-request cap, then the wire version on
// a write, decoding and the handler. So a refused principal is never
// decoded and a key over its rate costs no decode.
func (a *API) rowHandler(ep *Endpoint, b *Binding) http.Handler {
	h := a.decoded(ep, b)
	if isWrite(ep.Method) {
		h = a.wireCheck(h)
	}
	if ep.Held {
		h = a.o.hold(h)
	}
	if admitsKey(ep.Access.Class) {
		h = a.keyRate(h)
	}
	h = a.scoped(ep, h)
	if a.o.guard != nil {
		h = a.o.guard.Wrap(ep, h)
	}
	if a.o.outage != nil && !ep.DuringOutage {
		h = a.outage(false, h)
	}
	h = a.quieted(ep, h)
	if a.o.floor != nil {
		h = a.clientFloor(h)
	}
	if isWrite(ep.Method) {
		h = httpserver.CrossOrigin(h, a.table.Catalog.Responder())
	}
	return h
}

// decoded decodes ep's parts and runs its binding, reading the feed
// position before the handler so a change it commits is seen as newer. A
// Raw or Stream handler reads the request itself, so its parts are checked,
// its JSON body, if any, handed back unread, and a Raw write's other body
// held to the row's cap.
func (a *API) decoded(ep *Endpoint, b *Binding) http.Handler {
	own := b.kind != bindJSON
	capped := own && ep.Kind == Raw && isWrite(ep.Method) && ep.Request == nil
	limit := cmp.Or(ep.MaxBody, a.table.MaxBody)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, body, e := a.decode(r, ep)
		if e == nil && capped && r.ContentLength > int64(limit) {
			e = tooLarge(limit)
		}
		if e != nil {
			a.table.Catalog.WriteError(w, r, e)
			return
		}
		aw := &answerWriter{ResponseWriter: w, a: a, r: r, ep: ep, own: own}
		if len(ep.Changes) > 0 {
			aw.position = a.o.feed.Position(r, ep.Changes)
		}
		if !own {
			a.runJSON(aw, r, ep, b, d)
			return
		}
		switch {
		case body != nil:
			r.Body = io.NopCloser(bytes.NewReader(body))
		case capped && ep.Keyed:
			a.runSpooled(w, aw, r, b, d, limit)
			return
		case capped:
			aw.body = &cappedBody{ReadCloser: http.MaxBytesReader(w, r.Body, int64(limit)), limit: limit}
			r.Body = aw.body
		}
		a.runOwn(aw, r, b, d)
	})
}

// runSpooled runs a Keyed Raw write over its spooled body, which the
// fingerprint covers through its digest. The spool file is removed when
// the handler returns, a panic included.
func (a *API) runSpooled(w http.ResponseWriter, aw *answerWriter, r *http.Request, b *Binding, d *decoded, limit int) {
	f, digest, e := a.spool(w, r, aw.ep, limit)
	if f != nil {
		defer a.unspool(r, f)
	}
	if e != nil {
		a.table.Catalog.WriteError(w, r, e)
		return
	}
	d.rawBody, r.Body = digest, io.NopCloser(f)
	a.runOwn(aw, r, b, d)
}

// runOwn runs a Raw or Stream handler, once per Idempotency-Key on a Keyed
// row.
func (a *API) runOwn(aw *answerWriter, r *http.Request, b *Binding, d *decoded) {
	if aw.ep.Keyed {
		a.keyed(aw, r, aw.ep, d, b.handler.ServeHTTP)
	} else {
		b.handler.ServeHTTP(aw, r)
	}
	aw.finish()
}

// runJSON runs a JSON row's handler and writes its answer, once per
// Idempotency-Key on a Keyed row.
func (a *API) runJSON(w http.ResponseWriter, r *http.Request, ep *Endpoint, b *Binding, d *decoded) {
	run := func(w http.ResponseWriter, r *http.Request) {
		code, answer, err := b.run(r.Context(), d)
		if err != nil {
			a.writeError(w, r, ep, err)
			return
		}
		a.writeAnswer(w, r, ep, code, answer)
	}
	if ep.Keyed {
		a.keyed(w, r, ep, d, run)
		return
	}
	run(w, r)
}

// wireCheck refuses a write built for another wire version; a request that
// names none passes.
func (a *API) wireCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, v := range r.Header.Values(headerWireVersion) {
			if v != a.version {
				a.table.Catalog.WriteError(w, r, &Error{Code: codeWire, Message: "the request was built for another version of this API"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// mountHandler is a mount row's wrap, outside the mount's own guard: the
// client floor, maintenance, the accounts outage, and the wire version on
// a write. Each
// refusal runs nothing and names nothing about the principal.
func (a *API) mountHandler(ep *Endpoint, h http.Handler) http.Handler {
	if isWrite(ep.Method) {
		h = a.wireCheck(h)
	}
	if a.o.outage != nil {
		h = a.outage(true, h)
	}
	h = a.quieted(ep, h)
	if a.o.floor != nil {
		h = a.clientFloor(h)
	}
	return h
}

// outage answers for the file WithOutage names; a mount serves sign-in
// while only the store is out.
func (a *API) outage(mount bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch file := a.o.outage(); file {
		case "":
		case "accounts":
			a.table.Catalog.WriteError(w, r, &Error{Code: codeAccounts, Message: "the account store cannot open"})
			return
		case "store":
			if !mount {
				a.table.Catalog.WriteError(w, r, &Error{Code: "store_unavailable", Message: "the data store cannot open"})
				return
			}
		default:
			a.log().ErrorContext(r.Context(), `httpwire: WithOutage answered a file other than "accounts" or "store"`, "file", file)
			a.table.Catalog.WriteError(w, r, &Error{Code: codeInternal})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// decode answers r's Params, Query and Request as ep declares them, and the
// body bytes it read.
func (a *API) decode(r *http.Request, ep *Endpoint) (*decoded, []byte, *Error) {
	d := &decoded{}
	if ep.Params != nil {
		v, ok := a.decodePath(r, ep)
		if !ok {
			return nil, nil, &Error{Code: codeNotFound, Message: "no resource matches the path"}
		}
		d.params = v
	}
	if ep.Query != nil {
		v, fields := a.decodeQuery(r, ep)
		if len(fields) > 0 {
			return nil, nil, &Error{Code: "bad_query", Message: "the query does not match this request", Fields: fields}
		}
		d.query = v
	}
	var b []byte
	if ep.Request != nil {
		e := jsonMediaType(r)
		if e != nil {
			return nil, nil, e
		}
		if b, e = readBody(r, cmp.Or(ep.MaxBody, a.table.MaxBody)); e != nil {
			return nil, nil, e
		}
		if d.body, e = a.decodeBody(r.Context(), b, ep); e != nil {
			return nil, nil, e
		}
	}
	if e := a.decodeLast(r, ep, d); e != nil {
		return nil, nil, e
	}
	return d, b, nil
}

// decodeLast reads what follows the body: If-Match, the Idempotency-Key,
// then the cursor.
func (a *API) decodeLast(r *http.Request, ep *Endpoint, d *decoded) *Error {
	var e *Error
	if ep.IfMatch {
		if d.ifMatch, e = ifMatch(r); e != nil {
			return e
		}
	}
	if d.key, e = idempotencyKeyOf(r, ep); e != nil {
		return e
	}
	if ep.List {
		return a.openPage(r, ep, d)
	}
	return nil
}

// clientFloor refuses a request whose client header names a version below
// the floor; a request without the header passes.
func (a *API) clientFloor(next http.Handler) http.Handler {
	f := a.o.floor
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, v := range r.Header.Values(a.table.ClientHeader) {
			if f.Below(v) {
				a.table.Catalog.WriteError(w, r, &Error{Code: "upgrade_required", Message: f.Message(v)})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// scoped carries the admitted request's credential and cursor owner in its
// context, refusing, as the wiring bug it is, a row whose class is not
// Public where either answers "", so nothing is ever bound to "".
func (a *API) scoped(ep *Endpoint, next http.Handler) http.Handler {
	public := ep.Access.Class == Public
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := &scope{api: a, ep: ep, public: public}
		if !public {
			s.credential, s.owner = a.o.credential(r), a.o.cursorOwner(r)
			for _, got := range []struct{ value, name string }{{s.credential, "WithCredential"}, {s.owner, "WithCursorOwner"}} {
				if got.value == "" {
					a.log().ErrorContext(r.Context(), "httpwire: "+got.name+" answered \"\" for an admitted request on a row whose class is not Public", "row", ep.Name)
					a.table.Catalog.WriteError(w, r, &Error{Code: codeInternal})
					return
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), scopeKey{}, s)))
	})
}

// keyRate spends one token of the admitted key's bucket; a session or an
// anonymous request is never counted.
func (a *API) keyRate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, isKey := a.o.keyOf(r); isKey {
			if wait, ok := a.o.keyRate.Take(id); !ok {
				a.table.Catalog.WriteError(w, r, &Error{Code: "rate_limited", Message: "this API key is over its request rate", RetryAfter: max(wait, time.Second)})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) log() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

type newChecker struct {
	t    *Table
	o    *options
	errs []error
}

func (c *newChecker) fail(format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf("httpwire: "+format, args...))
}

// bindings pairs each table row with its binding, refusing every miss.
func (c *newChecker) bindings(bs []Binding) map[string]*Binding {
	rows := map[string]*Endpoint{}
	for i := range c.t.Endpoints {
		rows[c.t.Endpoints[i].Name] = &c.t.Endpoints[i]
	}
	bound := map[string]*Binding{}
	for _, b := range bs {
		switch {
		case isMountRow(c.t, b.name):
			c.fail("binding %q names a mount row, which its mount's Handler serves", b.name)
		case rows[b.name] == nil:
			c.fail("binding %q names no row", b.name)
		case bound[b.name] != nil:
			c.fail("row %q is bound twice", b.name)
		case b.nilHandler:
			c.fail("binding %q has a nil handler", b.name)
		default:
			bound[b.name] = &b
		}
	}
	for i := range c.t.Endpoints {
		ep := &c.t.Endpoints[i]
		b := bound[ep.Name]
		if b == nil {
			if !slices.ContainsFunc(bs, func(b Binding) bool { return b.name == ep.Name }) {
				c.fail("row %q has no binding", ep.Name)
			}
			continue
		}
		for _, p := range bindingProblems(ep, b) {
			c.fail("%s", p)
		}
	}
	return bound
}

func (c *newChecker) firstRow(pred func(*Endpoint) bool) *Endpoint {
	for i := range c.t.Endpoints {
		if pred(&c.t.Endpoints[i]) {
			return &c.t.Endpoints[i]
		}
	}
	return nil
}

// options refuses a feature the table uses with no option to serve it, and
// an option set with nothing to serve or half its parts.
func (c *newChecker) options() {
	c.principal()
	c.keyRate()
	c.floor()
	for i := range c.t.Endpoints {
		c.rowOptions(&c.t.Endpoints[i])
	}
}

// principal names the first row that needs a principal option missing.
func (c *newChecker) principal() {
	ep := c.firstRow(func(ep *Endpoint) bool { return ep.Access.Class != Public })
	if ep == nil {
		return
	}
	for _, need := range []struct {
		name    string
		missing bool
	}{{"WithGuard", c.o.guard == nil}, {"WithCredential", c.o.credential == nil}, {"WithCursorOwner", c.o.cursorOwner == nil}} {
		if need.missing {
			c.fail("row %q: a row whose class is not Public needs %s", ep.Name, need.name)
		}
	}
}

func (c *newChecker) keyRate() {
	switch o := c.o; {
	case o.keyRateSet && (o.keyRate == nil || o.keyOf == nil):
		c.fail("WithKeyRate needs a limiter and a key function")
	case !o.keyRateSet:
		if ep := c.firstRow(func(ep *Endpoint) bool { return admitsKey(ep.Access.Class) }); ep != nil {
			c.fail("row %q: a row whose class admits a key needs WithKeyRate", ep.Name)
		}
	}
}

func (c *newChecker) rowOptions(ep *Endpoint) {
	o := c.o
	if ep.Held && o.hold == nil {
		c.fail("row %q: a Held row needs WithHold", ep.Name)
	}
	if ep.Keyed && o.hold == nil {
		c.fail("row %q: a Keyed row needs WithHold, which holds its join waiters", ep.Name)
	}
	if ep.Keyed && ep.Kind == Raw && o.spoolDir == "" {
		c.fail("row %q: a Keyed Raw row needs WithSpoolDir, where its body is spooled", ep.Name)
	}
	if len(ep.Changes) > 0 && o.feed == nil {
		c.fail("row %q: a row with Changes needs WithFeed, which seeds its Feed-Position", ep.Name)
	}
}

func (c *newChecker) floor() {
	f, header := c.o.floor, c.t.ClientHeader
	switch {
	case f == nil && header != "":
		c.fail("ClientHeader %q needs WithClientFloor", header)
	case f != nil && header == "":
		c.fail("WithClientFloor is set, but the table has no ClientHeader")
	case f != nil && (f.Below == nil || f.Message == nil):
		c.fail("WithClientFloor needs both Below and Message")
	}
}

// sealer builds the cursor sealer under WithCursorKey's key, else a random
// one.
func (c *newChecker) sealer() *sealer {
	var key [32]byte
	if c.o.cursorKey != nil {
		key = *c.o.cursorKey
	} else {
		_, _ = rand.Read(key[:])
	}
	s, err := newSealer(key)
	if err != nil {
		c.fail("the cursor key: %w", err)
	}
	return s
}

// document parses WithSchema's document and matches its operations with
// the table's rows, mount rows included, one to one.
func (c *newChecker) document() *Schema {
	if !c.o.schemaSet {
		c.fail("New needs WithSchema: the table's committed OpenAPI document")
		return nil
	}
	s, err := ParseSchema(c.o.schema)
	if err != nil {
		c.fail("WithSchema: %w", err)
		return nil
	}
	names := map[string]bool{}
	for _, r := range (&tableChecker{t: c.t}).rowRefs() {
		ep := r.ep
		names[ep.Name] = true
		op := s.ops[ep.Name]
		switch {
		case op == nil:
			c.fail("row %q has no operation of that operationId in the document", ep.Name)
		case op.method != ep.Method || op.path != ep.Path:
			c.fail("row %q is %s %s, but the document's operation is %s %s", ep.Name, ep.Method, ep.Path, op.method, op.path)
		case !r.mount:
			c.decodedParts(ep, op)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.ops)) {
		if !names[id] {
			c.fail("the document's operation %q (%s %s) has no row", id, s.ops[id].method, s.ops[id].path)
		}
	}
	return s
}

// decodedParts matches what the binder decodes on ep with what op declares:
// each Params and Query field a parameter, required as the field is, no
// other path or query parameter, and a JSON request body exactly when ep
// has a Request. A mismatch would refuse every request at run time.
func (c *newChecker) decodedParts(ep *Endpoint, op *operation) {
	at := fmt.Sprintf("row %q", ep.Name)
	declared := map[paramKey]bool{}
	c.fieldParams(at, ep.Params, inPath, op, declared)
	c.fieldParams(at, ep.Query, inQuery, op, declared)
	for _, key := range slices.SortedFunc(maps.Keys(op.params), func(x, y paramKey) int {
		return cmp.Or(cmp.Compare(x.in, y.in), cmp.Compare(x.name, y.name))
	}) {
		if (key.in == inPath || key.in == inQuery) && !declared[key] {
			c.fail("%s: the document's %s parameter %q is no %s field", at, key.in, key.name, partOf[key.in])
		}
	}
	c.ifMatchParam(at, ep, op)
	hasBody := op.request != nil && op.request.schema != nil
	switch {
	case ep.Request != nil && !hasBody:
		c.fail("%s: the row decodes a Request, but the document's operation has no JSON request body", at)
	case ep.Request != nil && !op.request.required:
		c.fail("%s: the document's request body is not required, but the row always decodes one", at)
	case ep.Request == nil && hasBody:
		c.fail("%s: the document's operation has a JSON request body, but the row has no Request", at)
	}
}

var partOf = map[string]string{inPath: partParams, inQuery: partQuery}

// ifMatchParam matches an IfMatch row with a required If-Match header
// parameter in op, and any other row with none; header names are
// case-insensitive (RFC 9110 section 5.1).
func (c *newChecker) ifMatchParam(at string, ep *Endpoint, op *operation) {
	var found []*param
	for key, p := range op.params {
		if key.in == inHeader && strings.EqualFold(key.name, headerIfMatch) {
			found = append(found, p)
		}
	}
	switch {
	case !ep.IfMatch && len(found) > 0:
		c.fail("%s: the document's operation takes an If-Match header, but the row is not IfMatch", at)
	case !ep.IfMatch:
	case len(found) != 1:
		c.fail("%s: an IfMatch row's operation declares one If-Match header parameter, not %d", at, len(found))
	case !found[0].required:
		c.fail("%s: the document's If-Match header parameter is not required, but the row always reads one", at)
	}
}

// fieldParams matches each field of t, decoded from the in parameters, with
// op's parameter of its name, marking it in declared.
func (c *newChecker) fieldParams(at string, t reflect.Type, in string, op *operation, declared map[paramKey]bool) {
	if t == nil {
		return
	}
	fields, _ := textFields(t)
	for _, f := range fields {
		key := paramKey{in, f.name}
		declared[key] = true
		switch p := op.params[key]; {
		case p == nil:
			c.fail("%s: %s field %q has no %s parameter in the document", at, partOf[in], f.name, in)
		case f.optional && p.required:
			c.fail("%s: %s field %q is optional, but the document's parameter is required", at, partOf[in], f.name)
		case !f.optional && !p.required:
			c.fail("%s: %s field %q is required, but the document's parameter is optional", at, partOf[in], f.name)
		}
	}
}
