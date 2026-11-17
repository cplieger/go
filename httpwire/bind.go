package httpwire

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
)

// Call is what a bound handler receives: the request's decoded parts.
type Call[P, Q, B any] struct {
	Params P
	Query  Q
	Body   B
}

// Handler is a JSON row's handler. Its error is answered through the row's
// union: an *Error whose code the union holds, else the first owner
// sentinel it wraps, else internal_error.
type Handler[P, Q, B, R any] func(context.Context, Call[P, Q, B]) (R, error)

// None is a row's type where it declares none: a P, Q or B the row has no
// Params, Query or Request for, and the R of a row with no Response (a 204).
type None struct{}

// Status is the answer of a JSON row that declares Successes: one of the
// row's declared success statuses and its body.
type Status[T any] struct {
	// Body is written as JSON when Code declares a body, and never on a
	// bodiless status.
	Body T
	// Code is the row's Status or one of its Successes; zero is the row's
	// Status. Any other code is answered internal_error.
	Code int
}

// Answer is the status and body a handler of a row with Successes returns.
func Answer[T any](code int, body T) Status[T] { return Status[T]{Code: code, Body: body} }

// statusAnswer is implemented by every Status[T], and by a type embedding
// one, which statusBody tells apart.
type statusAnswer interface {
	statusType() reflect.Type
	bodyType() reflect.Type
	answer() (code int, body any)
}

func (s Status[T]) statusType() reflect.Type     { return reflect.TypeFor[Status[T]]() }
func (Status[T]) bodyType() reflect.Type         { return reflect.TypeFor[T]() }
func (s Status[T]) answer() (code int, body any) { return s.Code, s.Body }

var noneType = reflect.TypeFor[None]()

// statusBody answers T when rt is exactly a Status[T]; a type embedding one
// carries its methods but is not one.
func statusBody(rt reflect.Type) (reflect.Type, bool) {
	if rt.Kind() != reflect.Struct {
		return nil, false
	}
	s, ok := reflect.TypeAssert[statusAnswer](reflect.Zero(rt))
	if !ok || s.statusType() != rt {
		return nil, false
	}
	return s.bodyType(), true
}

type bindKind uint8

const (
	bindJSON bindKind = iota + 1
	bindRaw
	bindStream
)

var bindNames = map[bindKind]string{bindJSON: "Bind", bindRaw: "BindRaw", bindStream: "BindStream"}

// Binding pairs a row, by name, with its handler. New checks it against
// the row.
type Binding struct {
	params, query, body, result reflect.Type
	handler                     http.Handler
	// run answers the handler's status, zero for the row's primary one,
	// its body and its error.
	run        func(context.Context, *decoded) (int, any, error)
	name       string
	kind       bindKind
	nilHandler bool
}

// decoded is a request's decoded parts, each a value of the binding's type
// or nil for its zero value.
type decoded struct {
	params, query, body any
}

// Bind binds a JSON row: P, Q and B are its Params, Query and Request types
// (None where it has none), and R its Response type, None when it has none,
// or Status[T] when it declares Successes.
func Bind[P, Q, B, R any](name string, h Handler[P, Q, B, R]) Binding {
	b := Binding{
		name: name, kind: bindJSON, nilHandler: h == nil,
		params: reflect.TypeFor[P](), query: reflect.TypeFor[Q](), body: reflect.TypeFor[B](), result: reflect.TypeFor[R](),
	}
	_, isStatus := statusBody(b.result)
	b.run = func(ctx context.Context, d *decoded) (code int, body any, err error) {
		var c Call[P, Q, B]
		if v, ok := d.params.(P); ok {
			c.Params = v
		}
		if v, ok := d.query.(Q); ok {
			c.Query = v
		}
		if v, ok := d.body.(B); ok {
			c.Body = v
		}
		r, err := h(ctx, c)
		if s, ok := any(r).(statusAnswer); ok && isStatus {
			code, body := s.answer()
			return code, body, err
		}
		return 0, r, err
	}
	return b
}

// BindRaw binds a Raw or Redirect row to a handler that writes its own
// answer.
func BindRaw(name string, h http.Handler) Binding {
	return Binding{name: name, kind: bindRaw, handler: h, nilHandler: h == nil}
}

// BindStream binds a Stream row to a handler that writes the event stream.
func BindStream(name string, h http.Handler) Binding {
	return Binding{name: name, kind: bindStream, handler: h, nilHandler: h == nil}
}

func kindBinding(k Kind) bindKind {
	switch k {
	case Raw, Redirect:
		return bindRaw
	case Stream:
		return bindStream
	}
	return bindJSON
}

// bodyOf is the one Go type a row's declared bodies share, nil with none.
func bodyOf(ep *Endpoint) reflect.Type {
	if ep.Response != nil {
		return ep.Response
	}
	for _, s := range ep.Successes {
		if s.Response != nil {
			return s.Response
		}
	}
	return nil
}

func typeName(t reflect.Type) string {
	if t == nil {
		return "none (httpwire.None)"
	}
	return t.String()
}

// bindingProblems answers why b cannot serve ep, each naming the row.
func bindingProblems(ep *Endpoint, b *Binding) []string {
	at := fmt.Sprintf("row %q", ep.Name)
	if want := kindBinding(ep.Kind); b.kind != want {
		return []string{fmt.Sprintf("%s is a %s row, bound with %s; bind it with %s", at, kindNames[ep.Kind], bindNames[b.kind], bindNames[want])}
	}
	if b.kind != bindJSON {
		return nil
	}
	var p []string
	for _, part := range []struct {
		got, want     reflect.Type
		letter, field string
	}{
		{b.params, ep.Params, "P", "Params"},
		{b.query, ep.Query, "Q", "Query"},
		{b.body, ep.Request, "B", "Request"},
	} {
		if !declares(part.got, part.want) {
			p = append(p, fmt.Sprintf("%s: the binding's %s is %s, but the row's %s is %s", at, part.letter, part.got, part.field, typeName(part.want)))
		}
	}
	if s := resultProblem(ep, b.result); s != "" {
		p = append(p, at+": "+s)
	}
	return p
}

// declares reports whether a binding's type got serves a row's declared
// type want, where nil is None.
func declares(got, want reflect.Type) bool {
	if want == nil {
		return got == noneType
	}
	return got == want
}

func resultProblem(ep *Endpoint, r reflect.Type) string {
	body := bodyOf(ep)
	inner, isStatus := statusBody(r)
	switch {
	case len(ep.Successes) > 0 && !isStatus:
		return fmt.Sprintf("the row declares Successes, so the binding's R is httpwire.Status[T] with T the row's body %s, not %s", typeName(body), r)
	case len(ep.Successes) > 0 && !declares(inner, body):
		return fmt.Sprintf("the binding's Status body is %s, but the row's body is %s", inner, typeName(body))
	case len(ep.Successes) == 0 && isStatus:
		return fmt.Sprintf("the row declares no Successes, so the binding's R is its Response %s, not a Status", typeName(ep.Response))
	case len(ep.Successes) == 0 && !declares(r, ep.Response):
		return fmt.Sprintf("the binding's R is %s, but the row's Response is %s", r, typeName(ep.Response))
	}
	return ""
}
