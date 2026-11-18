package httpwire

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"
)

const (
	defaultMaxBody = 64 << 10
	maxKindName    = 32
	maxSourceName  = 32
	minWindow      = time.Minute
	maxWindow      = 7 * 24 * time.Hour
)

// The codes the union adds for a row's features.
var (
	keyedUnion = []string{
		codeKeyInvalid, codeKeyRequired, codeKeyReused, codeInFlight,
		codeStoreFull, codeTooLarge, "held_limit",
	}
	ifMatchUnion  = []string{codeIfMatchReq, codeIfMatchBad, "stale_version"}
	writeUnion    = []string{codeWire, "cross_origin_refused"}
	everyRowUnion = []string{codeAccounts, codeMaintenance, "host_not_allowed", codeInternal, codeNotFound, "method_not_allowed"}
)

var (
	wholeRowActions = []string{PersonSignInMethods, PersonTrustedProxies, PersonKeyRotate}
	inRowActions    = []string{PersonAdminAccount, PersonSecretRetarget, PersonAccountsArchive}
)

// Derive runs every table check that needs no binding and returns a copy of
// t with each row's Errors filled and MaxBody defaulted. Its error lists
// every refusal at once, each naming its row, owner, source or kind.
// guardCodes answers the guard's codes for an Access; nil is no guard.
func Derive(t *Table, guardCodes func(Access) []string) (*Table, error) {
	if t == nil {
		return nil, errors.New("httpwire: Derive of a nil table")
	}
	d, errs := derive(t, guardCodes)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return d, nil
}

// derive answers the derived copy beside every refusal, so New can go on
// checking the bindings and the document against it.
func derive(t *Table, guardCodes func(Access) []string) (*Table, []error) {
	d := cloneTable(t)
	if d.MaxBody == 0 {
		d.MaxBody = defaultMaxBody
	}
	c := &tableChecker{t: d, guard: guardCodes, owners: map[*Owner]bool{}}
	c.table()
	c.feed()
	refs := c.rowRefs()
	names := c.names(refs)
	for _, r := range refs {
		c.row(r, names)
	}
	c.patterns(refs)
	return d, c.errs
}

// Union is a row's error union, deduplicated in a fixed order: the row's own
// codes and its owners', the codes its features bring, the guard's codes for
// its Access, and the codes every row may answer. A mount row, found in t by
// name, carries its own codes and the binder's wrap's instead. Derive fills
// Errors with it, so the binder and the client generator read one
// derivation.
func Union(t *Table, ep *Endpoint, guardCodes func(Access) []string) []string {
	return unionOf(t, ep, guardCodes, isMountRow(t, ep.Name))
}

func isMountRow(t *Table, name string) bool {
	for _, m := range t.Mounts {
		for i := range m.Rows {
			if m.Rows[i].Name == name {
				return true
			}
		}
	}
	return false
}

type codeSet struct {
	seen map[string]bool
	list []string
}

func (s *codeSet) add(codes ...string) {
	for _, c := range codes {
		if c != "" && !s.seen[c] {
			s.seen[c] = true
			s.list = append(s.list, c)
		}
	}
}

func (s *codeSet) addIf(cond bool, codes ...string) {
	if cond {
		s.add(codes...)
	}
}

func isWrite(method string) bool { return method != http.MethodGet && method != http.MethodHead }

func admitsKey(c Class) bool { return c == Key || c == Scoped }

func unionOf(t *Table, ep *Endpoint, guardCodes func(Access) []string, mount bool) []string {
	u := &codeSet{seen: map[string]bool{}}
	u.add(ep.Codes...)
	write := isWrite(ep.Method)
	if mount {
		u.addIf(t.ClientHeader != "", "upgrade_required")
		u.addIf(write, writeUnion...)
		u.addIf(admitsKey(ep.Access.Class), "rate_limited")
		u.add(everyRowUnion...)
		return u.list
	}
	for _, o := range ep.Owners {
		if o != nil {
			for _, oc := range o.Codes {
				u.add(oc.Code)
			}
		}
	}
	u.addIf(ep.Request != nil, codeBadBody, "body_too_large")
	u.addIf(ep.Kind == Raw && write, "body_too_large")
	u.addIf(ep.Query != nil, "bad_query")
	u.addIf(ep.IfMatch, ifMatchUnion...)
	u.addIf(ep.List, codeCursor)
	u.addIf(ep.Keyed, keyedUnion...)
	u.addIf(!ep.Keyed, "idempotency_key_unsupported")
	u.addIf(write, writeUnion...)
	if len(ep.PersonActions) > 0 {
		u.add(codePerson)
		if guardCodes != nil && slices.ContainsFunc(ep.PersonActions, isInRowAction) {
			u.add(guardCodes(Access{Class: Session, RecentAuth: true})...)
		}
	}
	u.addIf(admitsKey(ep.Access.Class), "rate_limited")
	u.addIf(ep.Kind == Stream || ep.Held, "held_limit")
	u.addIf(t.ClientHeader != "", "upgrade_required")
	u.addIf(!ep.DuringOutage, "store_unavailable")
	if guardCodes != nil {
		u.add(guardCodes(ep.Access)...)
	}
	u.add(everyRowUnion...)
	return u.list
}

func isInRowAction(a string) bool { return slices.Contains(inRowActions, a) }

func cloneTable(t *Table) *Table {
	d := *t
	d.Endpoints = cloneRows(t.Endpoints)
	d.Sources = slices.Clone(t.Sources)
	d.Kinds = slices.Clone(t.Kinds)
	d.Mounts = slices.Clone(t.Mounts)
	for i := range d.Mounts {
		d.Mounts[i].Rows = cloneRows(t.Mounts[i].Rows)
	}
	return &d
}

func cloneRows(rows []Endpoint) []Endpoint {
	out := slices.Clone(rows)
	for i := range out {
		ep := &out[i]
		ep.Successes = slices.Clone(ep.Successes)
		ep.Codes = slices.Clone(ep.Codes)
		ep.Owners = slices.Clone(ep.Owners)
		ep.Changes = slices.Clone(ep.Changes)
		ep.Events = slices.Clone(ep.Events)
		ep.PersonActions = slices.Clone(ep.PersonActions)
		ep.Errors = slices.Clone(ep.Errors)
		if ep.Reasons != nil {
			r := make(map[string]ReasonSet, len(ep.Reasons))
			for code, set := range ep.Reasons {
				r[code] = ReasonSet{Listed: slices.Clone(set.Listed), Open: set.Open}
			}
			ep.Reasons = r
		}
	}
	return out
}

type rowRef struct {
	ep    *Endpoint
	mount bool
}

type tableChecker struct {
	t      *Table
	guard  func(Access) []string
	owners map[*Owner]bool
	kinds  map[string]bool
	errs   []error
}

func (c *tableChecker) fail(format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf("httpwire: "+format, args...))
}

func (c *tableChecker) table() {
	t := c.t
	if t.Catalog == nil {
		c.fail("the table has no Catalog")
	}
	if !validPrefix(t.Prefix) {
		c.fail("Prefix %q is not a path of one or more literal segments with no trailing slash", t.Prefix)
	}
	if t.MaxBody < 0 {
		c.fail("the table's MaxBody is negative: %d", t.MaxBody)
	}
	if t.ClientHeader != "" && !isToken(t.ClientHeader) {
		c.fail("ClientHeader %q is not a header name", t.ClientHeader)
	}
	for i, m := range t.Mounts {
		if m.Handler == nil {
			c.fail("mount %d has no Handler", i)
		}
	}
}

func (c *tableChecker) feed() {
	sources := map[string]bool{}
	for _, s := range c.t.Sources {
		switch {
		case !isSourceName(s.Name):
			c.fail("source %q is not ^[a-z][a-z0-9_]{0,31}$", s.Name)
		case sources[s.Name]:
			c.fail("source %q is declared twice", s.Name)
		}
		sources[s.Name] = true
	}
	c.kinds = map[string]bool{}
	for _, k := range c.t.Kinds {
		switch {
		case !isCode(k.Name) || len(k.Name) > maxKindName:
			c.fail("kind %q is not in the code grammar of at most %d bytes", k.Name, maxKindName)
		case c.kinds[k.Name]:
			c.fail("kind %q is declared twice", k.Name)
		case !sources[k.Source]:
			c.fail("kind %q names source %q, which is not in Sources", k.Name, k.Source)
		}
		c.kinds[k.Name] = true
	}
}

func (c *tableChecker) rowRefs() []rowRef {
	var refs []rowRef
	for i := range c.t.Endpoints {
		refs = append(refs, rowRef{ep: &c.t.Endpoints[i]})
	}
	for m := range c.t.Mounts {
		for i := range c.t.Mounts[m].Rows {
			refs = append(refs, rowRef{ep: &c.t.Mounts[m].Rows[i], mount: true})
		}
	}
	return refs
}

// names checks each name once and answers every row by name.
func (c *tableChecker) names(refs []rowRef) map[string]*Endpoint {
	byName := map[string]*Endpoint{}
	for _, r := range refs {
		name := r.ep.Name
		switch {
		case !isRowName(name):
			c.fail(`row %q: the name is not "<area>.<verb>", dot-separated segments of a lowercase letter then letters and digits`, name)
		case byName[name] != nil:
			c.fail("row %q: the name is used twice", name)
		default:
			byName[name] = r.ep
		}
	}
	return byName
}

func (c *tableChecker) row(r rowRef, names map[string]*Endpoint) {
	ep := r.ep
	at := fmt.Sprintf("row %q", ep.Name)
	c.shape(ep, at)
	c.textParts(ep, at, r.mount)
	c.kindAndStatus(ep, at)
	for _, p := range accessProblems(ep.Access) {
		c.fail("%s: %s", at, p)
	}
	c.keyed(ep, at, names)
	if ep.Held && ep.Kind != JSON {
		c.fail("%s: a Held row is a JSON row", at)
	}
	c.changes(ep, at)
	c.events(ep, at)
	c.personActions(ep, at)
	for _, p := range listProblems(ep) {
		c.fail("%s: %s", at, p)
	}
	if r.mount && len(ep.Owners) > 0 {
		c.fail("%s: a mount row's Owners are never consulted, since its handler answers its own codes", at)
	}
	if !r.mount {
		c.rowOwners(ep, at)
	}
	ep.Errors = unionOf(c.t, ep, c.guard, r.mount)
	c.codes(ep, at)
	c.reasons(ep, at)
}

func (c *tableChecker) shape(ep *Endpoint, at string) {
	if !isRowMethod(ep.Method) {
		c.fail("%s: method %q is not GET, POST, PUT, PATCH or DELETE", at, ep.Method)
	}
	wildcards, err := pathWildcards(c.t.Prefix, ep.Path)
	if err != nil {
		c.fail("%s: %v", at, err)
	} else if (len(wildcards) > 0) != (ep.Params != nil) {
		c.fail("%s: Params is set exactly when the path has wildcards", at)
	}
	for _, f := range []struct {
		t    reflect.Type
		name string
	}{{ep.Params, partParams}, {ep.Query, partQuery}, {ep.Request, "Request"}, {ep.Response, "Response"}} {
		if f.t == noneType {
			c.fail("%s: %s is httpwire.None; leave it nil to declare none", at, f.name)
		}
	}
	if ep.Request != nil && ep.Method == http.MethodGet {
		c.fail("%s: a GET takes no Request body", at)
	}
	if strings.TrimSpace(ep.Summary) == "" {
		c.fail("%s: Summary is empty", at)
	}
	if strings.TrimSpace(ep.Description) == "" {
		c.fail("%s: Description is empty", at)
	}
	if ep.MaxBody < 0 {
		c.fail("%s: MaxBody is negative: %d", at, ep.MaxBody)
	}
}

// textParts holds Params to the path's wildcards and, on a table row, every
// Params and Query field to the types the binder parses from text. A mount
// row's fields are its handler's to parse.
func (c *tableChecker) textParts(ep *Endpoint, at string, mount bool) {
	if ep.Params != nil && ep.Params != noneType {
		c.pathParams(ep, at, mount)
	}
	if mount {
		return
	}
	if ep.Query != nil && ep.Query != noneType {
		_, problems := textFields(ep.Query)
		c.failEach(at+": Query ", problems)
	}
	seen := map[reflect.Type]bool{}
	for _, t := range []reflect.Type{ep.Params, ep.Query, ep.Request} {
		if t != nil {
			c.failEach(at+": ", refusalProblems(t, seen))
		}
	}
}

// pathParams holds ep's Params fields to its path's wildcards, one each.
func (c *tableChecker) pathParams(ep *Endpoint, at string, mount bool) {
	wildcards, _ := pathWildcards(c.t.Prefix, ep.Path)
	fields, problems := textFields(ep.Params)
	for _, name := range wildcards {
		if !slices.ContainsFunc(fields, func(f textField) bool { return f.name == name }) {
			c.fail("%s: path wildcard {%s} has no Params field", at, name)
		}
	}
	for _, f := range fields {
		switch {
		case !slices.Contains(wildcards, f.name):
			c.fail("%s: Params field %q names no path wildcard", at, f.name)
		case f.optional && !mount:
			c.fail("%s: Params field %q is optional; a path value is always present", at, f.name)
		}
	}
	if !mount {
		c.failEach(at+": Params ", problems)
	}
}

func (c *tableChecker) failEach(prefix string, problems []string) {
	for _, p := range problems {
		c.fail("%s%s", prefix, p)
	}
}

var kindNames = map[Kind]string{JSON: "JSON", Raw: "Raw", Stream: "Stream", Redirect: "Redirect"}

func (c *tableChecker) kindAndStatus(ep *Endpoint, at string) {
	kind, ok := kindNames[ep.Kind]
	if !ok {
		c.fail("%s: Kind %d is not JSON, Raw, Stream or Redirect", at, ep.Kind)
		return
	}
	if ep.Kind != JSON && ep.Response != nil {
		c.fail("%s: a %s row sets no Response", at, kind)
	}
	if (ep.Kind == Raw) != (ep.ContentType != "") {
		c.fail("%s: ContentType is set exactly on a Raw row", at)
	}
	if (ep.Kind == Stream || ep.Kind == Redirect) && ep.Method != http.MethodGet {
		c.fail("%s: a %s row is a GET", at, kind)
	}
	if ep.IfMatch && ep.Kind != JSON {
		c.fail("%s: an IfMatch row is a JSON row, whose handler receives the tag in its Call", at)
	}
	primary := primaryStatus(ep)
	if p := statusProblem(ep, primary); p != "" {
		c.fail("%s: status %d %s", at, primary, p)
	}
	seen := map[int]bool{primary: true}
	for _, s := range ep.Successes {
		if p := successProblem(ep, s); p != "" {
			c.fail("%s: extra success %d %s", at, s.Status, p)
		}
		if seen[s.Status] {
			c.fail("%s: success status %d is declared twice", at, s.Status)
		}
		seen[s.Status] = true
	}
	c.bodyTypes(ep, at)
}

func primaryStatus(ep *Endpoint) int {
	switch {
	case ep.Status != 0:
		return ep.Status
	case ep.Kind == Redirect:
		return http.StatusFound
	case ep.Kind == JSON && ep.Response == nil:
		return http.StatusNoContent
	}
	return http.StatusOK
}

func isRedirectStatus(st int) bool {
	switch st {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// isSuccess2xx excludes 205, which asks the user agent to reset its view
// (RFC 9110 section 15.3.6), something no generated client can act on.
func isSuccess2xx(st int) bool { return st >= 200 && st <= 299 && st != http.StatusResetContent }

// statusProblem answers why a primary status does not fit the row's kind, or
// "". An EventSource reads a stream only at 200
// (https://html.spec.whatwg.org/multipage/server-sent-events.html).
func statusProblem(ep *Endpoint, st int) string {
	switch {
	case ep.Kind == Redirect:
		if !isRedirectStatus(st) {
			return "is not a redirect: use 301, 302, 303, 307 or 308"
		}
	case ep.Kind == Stream:
		if st != http.StatusOK {
			return "is not 200, the one status a stream answers"
		}
	case !isSuccess2xx(st):
		return "is not a 2xx other than 205"
	case (ep.Kind == JSON && ep.Response == nil) != (st == http.StatusNoContent):
		return "breaks the rule that 204 is exactly the status of a JSON row with no Response"
	}
	return ""
}

func successProblem(ep *Endpoint, s Success) string {
	switch {
	case s.Response == noneType:
		return "sets Response to httpwire.None; leave it nil to declare none"
	case ep.Kind != JSON && s.Response != nil:
		return "sets a Response, which only a JSON row answers"
	case ep.Kind == Redirect:
		if !isRedirectStatus(s.Status) {
			return "is not a redirect: use 301, 302, 303, 307 or 308"
		}
	case !isSuccess2xx(s.Status):
		return "is not a 2xx other than 205"
	case s.Status == http.StatusNoContent && s.Response != nil:
		return "sets a Response, but a 204 has no body"
	case ep.Kind == JSON && s.Status != http.StatusNoContent && s.Response == nil:
		return "has no Response, but only a 204 has no body"
	case ep.Kind != JSON && s.Status != http.StatusNoContent:
		return "is not 204, the one extra success a Raw or Stream row answers"
	}
	return ""
}

// bodyTypes refuses a row whose declared bodies differ: a handler answers
// one Go type.
func (c *tableChecker) bodyTypes(ep *Endpoint, at string) {
	body := ep.Response
	for _, s := range ep.Successes {
		switch {
		case s.Response == nil:
		case body == nil:
			body = s.Response
		case s.Response != body:
			c.fail("%s: declares two body types, %v and %v; declare one wire type holding both, or two rows", at, body, s.Response)
		}
	}
}

func accessProblems(a Access) []string {
	if a.Class < Public || a.Class > Recovery {
		return []string{fmt.Sprintf("Access Class %d is not Public, Session, Key, Scoped or Recovery", a.Class)}
	}
	var p []string
	if s := scopeProblem(a); s != "" {
		p = append(p, s)
	}
	if a.Class == Public {
		for flag, set := range map[string]bool{"Passive": a.Passive, "HTTPS": a.HTTPS, "RecentAuth": a.RecentAuth} {
			if set {
				p = append(p, flag+" is refused on a Public row")
			}
		}
	}
	if a.PersonOnly && a.Class != Session {
		p = append(p, "PersonOnly is set only on a Session row")
	}
	slices.Sort(p)
	return p
}

func scopeProblem(a Access) string {
	switch {
	case a.Class == Scoped && a.Scope == "":
		return "a Scoped row names its Scope"
	case a.Scope == "":
		return ""
	case !isCode(a.Scope):
		return fmt.Sprintf("Scope %q is not in the code grammar", a.Scope)
	case a.Class == Public || a.Class == Recovery || a.Class == Session && !a.PersonOnly:
		return "a Scope is set only on a Key, Scoped or PersonOnly Session row"
	}
	return ""
}

func (c *tableChecker) keyed(ep *Endpoint, at string, names map[string]*Endpoint) {
	if p := keyedProblem(ep); p != "" {
		c.fail("%s: a Keyed row is not %s", at, p)
	}
	if ep.Window != 0 && (!ep.Keyed || ep.Window < minWindow || ep.Window > maxWindow) {
		c.fail("%s: Window %v is set only on a Keyed row, from 1 minute to 7 days", at, ep.Window)
	}
	if ep.MaxKept < 0 || ep.MaxKept != 0 && !ep.Keyed {
		c.fail("%s: MaxKept %d is set only on a Keyed row, and never negative", at, ep.MaxKept)
	}
	if r := names[ep.Result]; ep.Result != "" && (!ep.Keyed || r == nil || r.Method != http.MethodGet) {
		c.fail("%s: Result %q is set only on a Keyed row and names a GET row", at, ep.Result)
	}
	if ep.Keyed {
		c.fingerprinted(ep, at)
	}
}

func (c *tableChecker) fingerprinted(ep *Endpoint, at string) {
	for _, part := range []reflect.Type{ep.Params, ep.Query, ep.Request} {
		if t, method := oneWay(part, map[reflect.Type]bool{}); t != nil {
			c.fail("%s: a Keyed row's fingerprint marshals the decoded request, but %s decodes itself with no %s", at, t, method)
		}
	}
}

func keyedProblem(ep *Endpoint) string {
	switch {
	case !ep.Keyed:
	case ep.Access.Class == Public:
		return "Public, since its key has no credential to be scoped to"
	case ep.Kind == Stream:
		return "a Stream, since a kept answer cannot hold an event stream"
	case ep.ShownOnce:
		return "ShownOnce, since a replay would show its secret again"
	case !isWrite(ep.Method):
		return "a read, since only a write needs a key"
	}
	return ""
}

var (
	typeJSONUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	typeJSONMarshaler   = reflect.TypeFor[json.Marshaler]()
	typeTextUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
	typeTextMarshaler   = reflect.TypeFor[encoding.TextMarshaler]()
)

// oneWay answers the first type reachable from t that decodes through its
// own UnmarshalJSON or UnmarshalText with no marshaler to match, and the
// method it lacks: the fingerprint's json.Marshal would not see what it
// decoded, so two requests could share one fingerprint.
func oneWay(t reflect.Type, seen map[reflect.Type]bool) (found reflect.Type, missing string) {
	if t == nil || seen[t] {
		return nil, ""
	}
	seen[t] = true
	if t.Kind() == reflect.Pointer {
		return oneWay(t.Elem(), seen)
	}
	if own, missing := ownCodec(t); own {
		if missing == "" {
			return nil, ""
		}
		return t, missing
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return oneWay(t.Elem(), seen)
	case reflect.Map:
		if k := t.Key(); k.Kind() != reflect.String && reflect.PointerTo(k).Implements(typeTextUnmarshaler) && !k.Implements(typeTextMarshaler) {
			return k, "MarshalText on its value"
		}
		return oneWay(t.Elem(), seen)
	case reflect.Struct:
		return oneWayField(t, seen)
	}
	return nil, ""
}

// ownCodec reports whether t decodes through its own method, and the
// marshaler it lacks, "" when it has one.
func ownCodec(t reflect.Type) (own bool, missing string) {
	p := reflect.PointerTo(t)
	switch {
	case p.Implements(typeJSONUnmarshaler):
		if !p.Implements(typeJSONMarshaler) {
			return true, "MarshalJSON"
		}
		return true, ""
	case p.Implements(typeTextUnmarshaler):
		if !p.Implements(typeTextMarshaler) && !p.Implements(typeJSONMarshaler) {
			return true, "MarshalText"
		}
		return true, ""
	}
	return false, ""
}

func oneWayField(t reflect.Type, seen map[reflect.Type]bool) (found reflect.Type, missing string) {
	for f := range t.Fields() {
		if (f.IsExported() || f.Anonymous) && f.Tag.Get("json") != "-" {
			if ot, m := oneWay(f.Type, seen); ot != nil {
				return ot, m
			}
		}
	}
	return nil, ""
}

func (c *tableChecker) changes(ep *Endpoint, at string) {
	if len(ep.Changes) == 0 {
		return
	}
	if isWrite(ep.Method) {
		c.fail("%s: a write names no Changes", at)
	}
	if ep.Personal && !c.t.OnePerson {
		c.fail("%s: a Personal row names Changes only in a OnePerson table, since a kind is shared by every principal of a shape", at)
	}
	seen := map[string]bool{}
	for _, k := range ep.Changes {
		switch {
		case seen[k]:
			c.fail("%s: Changes names %q twice", at, k)
		case !c.kinds[k]:
			c.fail("%s: Changes names %q, which is not in Kinds", at, k)
		}
		seen[k] = true
	}
}

func (c *tableChecker) events(ep *Endpoint, at string) {
	if len(ep.Events) > 0 && ep.Kind != Stream {
		c.fail("%s: only a Stream row declares Events", at)
	}
	seen := map[string]bool{}
	for _, e := range ep.Events {
		ev := fmt.Sprintf("%s: event %q", at, e.Name)
		switch {
		case !isCode(e.Name):
			c.fail("%s: the name is not in the code grammar", ev)
		case seen[e.Name]:
			c.fail("%s: declared twice", ev)
		}
		seen[e.Name] = true
		if e.Data == nil || e.Data == noneType {
			c.fail("%s: no Data type", ev)
		}
		if e.Readers != (Access{}) {
			for _, p := range accessProblems(e.Readers) {
				c.fail("%s: Readers: %s", ev, p)
			}
		}
	}
}

func (c *tableChecker) personActions(ep *Endpoint, at string) {
	if len(ep.PersonActions) > 0 && ep.Access.Class == Public {
		c.fail("%s: a Public row lists no PersonActions", at)
	}
	seen := map[string]bool{}
	for _, a := range ep.PersonActions {
		switch {
		case seen[a]:
			c.fail("%s: PersonActions lists %q twice", at, a)
		case slices.Contains(wholeRowActions, a):
			if !ep.Access.PersonOnly {
				c.fail("%s: action %s is the whole row's purpose, so the row is PersonOnly", at, a)
			}
		case !isInRowAction(a):
			c.fail("%s: PersonActions lists %q, which is not a person-only action", at, a)
		}
		seen[a] = true
	}
}

func (c *tableChecker) rowOwners(ep *Endpoint, at string) {
	for _, o := range ep.Owners {
		if o == nil {
			c.fail("%s: Owners holds a nil owner", at)
			continue
		}
		if !c.owners[o] {
			c.owners[o] = true
			c.owner(o)
		}
	}
}

func sameErr(a, b error) bool {
	ta := reflect.TypeOf(a)
	return ta == reflect.TypeOf(b) && ta.Comparable() && a == b
}

func sentinelName(o *Owner, err error) (string, bool) {
	for _, name := range slices.Sorted(maps.Keys(o.Sentinels)) {
		if sameErr(o.Sentinels[name], err) {
			return name, true
		}
	}
	return "", false
}

func (c *tableChecker) owner(o *Owner) {
	at := fmt.Sprintf("owner %q", o.Name)
	if o.Name == "" {
		c.fail("%s: no Name", at)
	}
	coded := map[string]bool{}
	for i, oc := range o.Codes {
		if oc.Err == nil {
			c.fail("%s: Codes[%d] has a nil Err", at, i)
			continue
		}
		name, ok := sentinelName(o, oc.Err)
		switch {
		case !ok:
			c.fail("%s: Codes[%d] maps %q, which is not one of its Sentinels", at, i, oc.Err.Error())
			continue
		case coded[name]:
			c.fail("%s: sentinel %s is listed twice in Codes, so the second entry never answers", at, name)
		}
		coded[name] = true
		c.ownerCode(at, name, oc.Code)
	}
	for _, name := range slices.Sorted(maps.Keys(o.Sentinels)) {
		_, outside := o.Outside[name]
		switch {
		case o.Sentinels[name] == nil:
			c.fail("%s: sentinel %s is nil", at, name)
		case coded[name] && outside:
			c.fail("%s: sentinel %s is in both Codes and Outside", at, name)
		case !coded[name] && !outside:
			c.fail("%s: sentinel %s is in neither Codes nor Outside", at, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(o.Outside)) {
		if _, ok := o.Sentinels[name]; !ok {
			c.fail("%s: Outside names %s, which is not one of its Sentinels", at, name)
		} else if strings.TrimSpace(o.Outside[name]) == "" {
			c.fail("%s: Outside gives %s no reason", at, name)
		}
	}
}

func (c *tableChecker) ownerCode(at, sentinel, code string) {
	if code == "" {
		c.fail("%s: sentinel %s maps to an empty code", at, sentinel)
		return
	}
	if c.t.Catalog != nil {
		if _, ok := c.t.Catalog.Lookup(code); !ok {
			c.fail("%s: code %q of sentinel %s is not in the catalog", at, code, sentinel)
		}
	}
}

func (c *tableChecker) codes(ep *Endpoint, at string) {
	if slices.Contains(ep.Codes, "") {
		c.fail("%s: Codes holds an empty code", at)
	}
	if c.t.Catalog == nil {
		return
	}
	for _, code := range ep.Errors {
		if _, ok := c.t.Catalog.Lookup(code); !ok {
			c.fail("%s: code %q is not in the catalog", at, code)
		}
	}
}

func (c *tableChecker) reasons(ep *Endpoint, at string) {
	for _, code := range slices.Sorted(maps.Keys(ep.Reasons)) {
		set := ep.Reasons[code]
		if !slices.Contains(ep.Errors, code) {
			c.fail("%s: Reasons names %q, which is not in the row's union", at, code)
		}
		if len(set.Listed) == 0 && !set.Open {
			c.fail("%s: Reasons gives %q no reasons and does not mark it Open", at, code)
		}
		seen := map[string]bool{}
		for _, r := range set.Listed {
			switch {
			case !isCode(r) || len(r) > maxReason:
				c.fail("%s: reason %q of %q is not in the code grammar of at most %d bytes", at, r, code, maxReason)
			case seen[r]:
				c.fail("%s: reason %q of %q is listed twice", at, r, code)
			}
			seen[r] = true
		}
	}
}

// patterns registers every row whose method and path passed on one
// ServeMux, the router New builds, so two rows the mux would refuse
// together (one method and pattern, or two patterns neither more specific)
// are refused here, naming both.
func (c *tableChecker) patterns(refs []rowRef) {
	mux := http.NewServeMux()
	var done []*Endpoint
	for _, r := range refs {
		ep := r.ep
		if _, err := pathWildcards(c.t.Prefix, ep.Path); err != nil || !isRowMethod(ep.Method) {
			continue
		}
		if err := register(mux, ep); err != nil {
			c.overlap(done, ep, err)
			continue
		}
		done = append(done, ep)
	}
}

// overlap names each registered row ep's pattern conflicts with.
func (c *tableChecker) overlap(done []*Endpoint, ep *Endpoint, err error) {
	found := false
	for _, other := range done {
		m := http.NewServeMux()
		_ = register(m, other)
		if register(m, ep) != nil {
			found = true
			c.fail("rows %q and %q: %s %s and %s %s overlap, so one request would match both", other.Name, ep.Name, other.Method, other.Path, ep.Method, ep.Path)
		}
	}
	if !found {
		c.fail("row %q: %v", ep.Name, err)
	}
}

func register(mux *http.ServeMux, ep *Endpoint) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	mux.Handle(ep.Method+" "+ep.Path, http.NotFoundHandler())
	return nil
}

func isRowMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// isRowName reports whether s is "<area>.<verb>": two or more segments, each
// a lowercase ASCII letter then letters and digits.
func isRowName(s string) bool {
	segs := strings.Split(s, ".")
	if len(segs) < 2 {
		return false
	}
	for _, seg := range segs {
		if seg == "" || seg[0] < 'a' || seg[0] > 'z' {
			return false
		}
		for i := range len(seg) {
			if !isAlnum(seg[i]) {
				return false
			}
		}
	}
	return true
}

func isSourceName(s string) bool {
	return s != "" && len(s) <= maxSourceName && s[0] >= 'a' && s[0] <= 'z' && isCode(s)
}

func isAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func isIdentByte(b byte, first bool) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || !first && b >= '0' && b <= '9'
}

// isToken reports whether s is an RFC 9110 section 5.6.2 token.
func isToken(s string) bool {
	for i := range len(s) {
		if b := s[i]; !isAlnum(b) && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(b)) {
			return false
		}
	}
	return s != ""
}

func validPrefix(p string) bool {
	rest, ok := strings.CutPrefix(p, "/")
	if !ok {
		return false
	}
	for seg := range strings.SplitSeq(rest, "/") {
		if !isLiteralSegment(seg) {
			return false
		}
	}
	return true
}

// isLiteralSegment reports whether seg is a non-empty run of RFC 3986 pchar:
// unreserved, sub-delims, ":" and "@", or a complete percent-encoding.
func isLiteralSegment(seg string) bool {
	if seg == "" {
		return false
	}
	for i := 0; i < len(seg); i++ {
		switch b := seg[i]; {
		case b == '%':
			if i+2 >= len(seg) || !isHex(seg[i+1]) || !isHex(seg[i+2]) {
				return false
			}
			i += 2
		case !isAlnum(b) && !strings.ContainsRune("-._~!$&'()*+,;=:@", rune(b)):
			return false
		}
	}
	return true
}

func isHex(b byte) bool { return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F' }

// pathWildcards checks that path is the prefix then one or more segments,
// each a literal or a whole {name}, names unique, and answers the wildcard
// names in path order.
func pathWildcards(prefix, path string) ([]string, error) {
	rest, ok := strings.CutPrefix(path, prefix+"/")
	if !ok || prefix == "" {
		return nil, fmt.Errorf("path %q is not under Prefix %q", path, prefix)
	}
	var names []string
	for seg := range strings.SplitSeq(rest, "/") {
		name, wild := strings.CutPrefix(seg, "{")
		if !wild {
			if !isLiteralSegment(seg) {
				return nil, fmt.Errorf("path %q has segment %q, which is empty or not a literal of URL path characters", path, seg)
			}
			continue
		}
		name, closed := strings.CutSuffix(name, "}")
		if !closed || !isIdent(name) || slices.Contains(names, name) {
			return nil, fmt.Errorf("path %q has segment %q, which is not a whole {name} wildcard used once", path, seg)
		}
		names = append(names, name)
	}
	return names, nil
}

func isIdent(s string) bool {
	for i := range len(s) {
		if !isIdentByte(s[i], i == 0) {
			return false
		}
	}
	return s != ""
}
