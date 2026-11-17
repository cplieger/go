package httpwire

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// API serves one checked table under its Prefix. It is safe for concurrent
// use.
type API struct {
	table  *Table
	mux    *http.ServeMux
	logger *slog.Logger
	schema *Schema
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
	if len(c.errs) > 0 {
		return nil, errors.Join(c.errs...)
	}
	a := &API{table: d, logger: o.logger, schema: schema, mux: http.NewServeMux()}
	for i := range d.Endpoints {
		ep := &d.Endpoints[i]
		a.mux.Handle(ep.Method+" "+ep.Path, &route{a.rowHandler(ep, bound[ep.Name])})
	}
	for _, m := range d.Mounts {
		for i := range m.Rows {
			a.mux.Handle(m.Rows[i].Method+" "+m.Rows[i].Path, &route{m.Handler})
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
// is a row's under another method, or not_found.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h, _ := a.mux.Handler(r); isRoute(h) {
		a.mux.ServeHTTP(w, r)
		return
	}
	if allow := a.allowed(r); allow != "" {
		w.Header().Set("Allow", allow)
		a.table.Catalog.WriteError(w, r, &Error{Code: "method_not_allowed", Message: "the path does not take this method"})
		return
	}
	a.table.Catalog.WriteError(w, r, &Error{Code: "not_found", Message: "no route matches the path"})
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

func (a *API) rowHandler(ep *Endpoint, b *Binding) http.Handler {
	if b.kind != bindJSON {
		return b.handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, body, err := b.run(r.Context(), &decoded{})
		if err != nil {
			a.writeError(w, r, ep, err)
			return
		}
		a.writeAnswer(w, r, ep, code, body)
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
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.ops)) {
		if !names[id] {
			c.fail("the document's operation %q (%s %s) has no row", id, s.ops[id].method, s.ops[id].path)
		}
	}
	return s
}
