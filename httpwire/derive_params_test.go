package httpwire_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/cplieger/go/httpwire"
)

type (
	plainText   string
	plainInt    int64
	badRefusal  string
	ptrRefusal  string
	codeRefusal string
	// promotedRefusal has Refusal only through its embedded pointer, which
	// its zero value holds nil.
	promotedRefusal struct{ *codeRefusal }
	deepRefusal     struct{ promotedRefusal }
	ptrMaxBytes     string
	pageLike        struct {
		Limit itemNum `json:"limit"`
	}
	embedsPage struct {
		pageLike
		Kind *itemKind `json:"kind"`
	}
	embedsPagePtr struct {
		*pageLike
	}
	badBody struct {
		Inner []struct {
			Code badRefusal `json:"code"`
		} `json:"inner"`
	}
)

func (badRefusal) MaxBytes() int    { return 8 }
func (badRefusal) Refusal() string  { return "Not-A-Code" }
func (ptrRefusal) MaxBytes() int    { return 8 }
func (*ptrRefusal) Refusal() string { return "ptr_code" }
func (*ptrMaxBytes) MaxBytes() int  { return 8 }
func (codeRefusal) Refusal() string { return "some_code" }

func params(t reflect.Type) func(*httpwire.Table) {
	return edit("jobs.get", func(ep *httpwire.Endpoint) { ep.Params = t })
}

func query(t reflect.Type) func(*httpwire.Table) {
	return edit("jobs.list", func(ep *httpwire.Endpoint) { ep.Query = t })
}

func TestDeriveRefusesParamsAndQueryFields(t *testing.T) {
	runRefusals(t, map[string]refusal{
		"a wildcard with no Params field": {params(reflect.TypeFor[struct {
			Other jobID `json:"other"`
		}]()), []string{`row "jobs.get": path wildcard {id} has no Params field`, `Params field "other" names no path wildcard`}},
		"a Params field named by its Go name only": {params(reflect.TypeFor[struct{ ID jobID }]()), []string{`Params field "ID" names no path wildcard`}},
		"a Params field outside the binder set": {params(reflect.TypeFor[struct {
			ID string `json:"id"`
		}]()), []string{`row "jobs.get": Params field "id" is string`, "a TextUnmarshaler"}},
		"an integer with no Range": {params(reflect.TypeFor[struct {
			ID plainInt `json:"id"`
		}]()), []string{`Params field "id" is httpwire_test.plainInt`}},
		"a string type with no MaxBytes": {params(reflect.TypeFor[struct {
			ID plainText `json:"id"`
		}]()), []string{`Params field "id" is httpwire_test.plainText`}},
		"MaxBytes on a pointer receiver": {params(reflect.TypeFor[struct {
			ID ptrMaxBytes `json:"id"`
		}]()), []string{`Params field "id" is httpwire_test.ptrMaxBytes`}},
		"a pointer Params field": {params(reflect.TypeFor[struct {
			ID *jobID `json:"id"`
		}]()), []string{`Params field "id" is optional; a path value is always present`}},
		"an omitempty Params field": {params(reflect.TypeFor[struct {
			ID jobID `json:"id,omitempty"`
		}]()), []string{`Params field "id" is optional; a path value is always present`}},
		"Params that is not a struct": {params(reflect.TypeFor[jobID]()), []string{`row "jobs.get": Params httpwire_test.jobID is not a struct`}},
		"a Query field outside the binder set": {query(reflect.TypeFor[struct {
			Ratio float64 `json:"ratio"`
		}]()), []string{`row "jobs.list": Query field "ratio" is float64`}},
		"a Query field twice": {query(reflect.StructOf([]reflect.StructField{
			{Name: "A", Type: reflect.TypeFor[jobState](), Tag: `json:"state"`},
			{Name: "B", Type: reflect.TypeFor[jobState](), Tag: `json:"state,omitempty"`},
		})), []string{`row "jobs.list": Query field "state" is declared twice`}},
		"a Query pointer to a pointer": {query(reflect.TypeFor[struct {
			S **jobState `json:"state"`
		}]()), []string{`Query field "state" is **httpwire_test.jobState`}},
		"an embedded pointer struct": {query(reflect.TypeFor[embedsPagePtr]()), []string{`row "jobs.list": Query embeds *httpwire_test.pageLike`}},
		"Query that is not a struct": {query(reflect.TypeFor[[]string]()), []string{`row "jobs.list": Query []string is not a struct`}},
		"a Refusal outside the code grammar": {edit("jobs.start", func(ep *httpwire.Endpoint) {
			ep.Request = reflect.TypeFor[badBody]()
		}), []string{`row "jobs.start": type httpwire_test.badRefusal answers Refusal "Not-A-Code", which is not in the code grammar`}},
		"a Refusal on a pointer receiver": {params(reflect.TypeFor[struct {
			ID ptrRefusal `json:"id"`
		}]()), []string{`row "jobs.get": type httpwire_test.ptrRefusal has Refusal only through a pointer`}},
		"a Refusal through an embedded pointer": {edit("jobs.start", func(ep *httpwire.Endpoint) {
			ep.Request = reflect.TypeFor[struct {
				Code promotedRefusal `json:"code"`
			}]()
		}), []string{`row "jobs.start": type httpwire_test.promotedRefusal has Refusal only through a pointer`}},
		"a Refusal through a pointer embedded two levels down": {edit("jobs.start", func(ep *httpwire.Endpoint) {
			ep.Request = reflect.TypeFor[struct {
				Code deepRefusal `json:"code"`
			}]()
		}), []string{`row "jobs.start": type httpwire_test.deepRefusal has Refusal only through a pointer`}},
		"a Cursor outside Page": {query(reflect.TypeFor[struct {
			After httpwire.Cursor `json:"after"`
		}]()), []string{`row "jobs.list": Query field "after" is an httpwire.Cursor, which only an embedded httpwire.Page carries`}},
	})
}

func TestDeriveAcceptsEveryBinderFieldType(t *testing.T) {
	tb := fixtureTable(t)
	row(tb, "jobs.list").Query = reflect.TypeFor[struct {
		Text  jobState  `json:"text"`
		Num   itemNum   `json:"num"`
		Big   uint64    `json:"big"`
		Flag  bool      `json:"flag"`
		Keys  keyList   `json:"keys"`
		Maybe *itemNum  `json:"maybe"`
		Kind  *itemKind `json:"kind,omitempty"`
		skip  int
		Gone  float64 `json:"-"`
	}]()
	row(tb, "jobs.get").Params = reflect.TypeFor[struct {
		ID keyList `json:"id"`
	}]()
	if _, err := httpwire.Derive(tb, guardCodes); err != nil {
		t.Fatalf("Derive: %v", err)
	}
	tb = fixtureTable(t)
	row(tb, "jobs.list").Query = reflect.TypeFor[embedsPage]()
	if _, err := httpwire.Derive(tb, guardCodes); err != nil {
		t.Errorf("Derive refused a Query embedding a struct: %v", err)
	}
}

func TestDeriveLeavesMountRowFieldsToTheMount(t *testing.T) {
	tb := fixtureTable(t)
	tb.Mounts[0].Rows = append(tb.Mounts[0].Rows, httpwire.Endpoint{
		Name: "auth.keyGet", Method: http.MethodGet, Path: "/api/auth/keys/{id}", Summary: "Read a key", Description: "Reads one key.",
		Params: reflect.TypeFor[struct {
			ID string `json:"id"`
		}](), Response: reflect.TypeFor[job](), Access: session,
	})
	if _, err := httpwire.Derive(tb, guardCodes); err != nil {
		t.Fatalf("Derive refused a mount row's own field type: %v", err)
	}
	row(tb, "auth.keyGet").Params = reflect.TypeFor[struct {
		Other string `json:"other"`
	}]()
	if _, err := httpwire.Derive(tb, guardCodes); err == nil {
		t.Error("Derive accepted a mount row whose Params miss its wildcard")
	}
}
