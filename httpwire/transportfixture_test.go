package httpwire_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// The TS half's TRANSPORT_FIXTURE is held to this file, so every header a
// client sends from it is one this package's parse accepts.
const transportFile = "web/testdata/transport.json"

type transportFixture struct {
	WireVersion string `json:"wireVersion"`
	ErrorCodes  map[string]struct {
		Status      int    `json:"status"`
		Remediation string `json:"remediation"`
	} `json:"errorCodes"`
	Rows []struct {
		Name     string   `json:"name"`
		Method   string   `json:"method"`
		Path     string   `json:"path"`
		Codes    []string `json:"codes"`
		Statuses []int    `json:"statuses"`
	} `json:"rows"`
	Cases []transportCase `json:"cases"`
}

type transportCase struct {
	Name    string  `json:"name"`
	Row     string  `json:"row"`
	Tag     *string `json:"tag"`
	Key     *string `json:"key"`
	Body    any     `json:"body"`
	Request *struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	} `json:"request"`
	Answer *struct {
		Kind    string            `json:"kind"`
		Status  int               `json:"status"`
		Code    string            `json:"code"`
		Headers map[string]string `json:"headers"`
	} `json:"answer"`
	Want struct {
		OK      bool   `json:"ok"`
		Failure string `json:"failure"`
		Status  int    `json:"status"`
		Code    string `json:"code"`
	} `json:"want"`
}

func TestTransportFixtureHeadersParse(t *testing.T) {
	var f transportFixture
	readCorpus(t, transportFile, &f)
	methods := map[string]string{}
	for _, r := range f.Rows {
		methods[r.Name] = r.Method
	}
	for _, c := range f.Cases {
		method, ok := methods[c.Row]
		if !ok {
			t.Errorf("%s: row %q is not in the fixture", c.Name, c.Row)
			continue
		}
		if c.Request == nil {
			if c.Want.Failure != "invalid" || c.Answer != nil {
				t.Errorf("%s: a case that sends nothing must want invalid and register no answer", c.Name)
			}
			switch {
			case c.Tag != nil:
				if _, ok := httpwire.ParseIfMatch(`"` + *c.Tag + `"`); ok {
					t.Errorf("%s: If-Match %q is accepted, so the client must send it", c.Name, *c.Tag)
				}
			case c.Key != nil:
				if _, ok := httpwire.ParseIdempotencyKey(naiveQuote(*c.Key)); ok {
					t.Errorf("%s: key %q is accepted, so the client must send it", c.Name, *c.Key)
				}
			default:
				t.Errorf("%s: sends nothing with no tag or key to refuse", c.Name)
			}
			continue
		}
		h := c.Request.Headers
		if c.Request.Method != method {
			t.Errorf("%s: request method %s, row's is %s", c.Name, c.Request.Method, method)
		}
		write := method != http.MethodGet && method != http.MethodHead
		if v, ok := h["Wire-Version"]; ok != write || (ok && v != f.WireVersion) {
			t.Errorf("%s: Wire-Version %q (sent %v) on a %s; a write carries %q, a read none", c.Name, v, ok, method, f.WireVersion)
		}
		if (c.Tag != nil) != (h["If-Match"] != "") {
			t.Errorf("%s: a tag and an If-Match header go together", c.Name)
		}
		if c.Tag != nil {
			tag, ok := httpwire.ParseIfMatch(h["If-Match"])
			if !ok || tag.String() != *c.Tag {
				t.Errorf("%s: If-Match %q parses to %v %v, want the tag %q", c.Name, h["If-Match"], tag, ok, *c.Tag)
			}
		}
		if (c.Key != nil) != (h["Idempotency-Key"] != "") {
			t.Errorf("%s: a key and an Idempotency-Key header go together", c.Name)
		}
		if c.Key != nil {
			key, ok := httpwire.ParseIdempotencyKey(h["Idempotency-Key"])
			if !ok || key != *c.Key {
				t.Errorf("%s: Idempotency-Key %q parses to %q %v, want the key %q", c.Name, h["Idempotency-Key"], key, ok, *c.Key)
			}
		}
		if v, ok := h["Content-Type"]; ok != (c.Body != nil) || (ok && v != "application/json") {
			t.Errorf("%s: Content-Type %q (sent %v); a body goes as application/json, no body with none", c.Name, v, ok)
		}
		for name := range h {
			if !slices.Contains([]string{"Content-Type", "Wire-Version", "If-Match", "Idempotency-Key"}, name) {
				t.Errorf("%s: header %q is not one the transport writes", c.Name, name)
			}
		}
	}
}

func TestTransportFixtureCodesAreTheCatalogs(t *testing.T) {
	var f transportFixture
	readCorpus(t, transportFile, &f)
	cat := httpwire.NewCatalog()
	for code, spec := range f.ErrorCodes {
		got, ok := cat.Lookup(code)
		if !ok || got.Status != spec.Status || got.Remediation != spec.Remediation {
			t.Errorf("errorCodes[%q] = %d %q, the catalog's is %d %q (%v)", code, spec.Status, spec.Remediation, got.Status, got.Remediation, ok)
		}
	}
	for _, r := range f.Rows {
		for _, code := range r.Codes {
			if _, ok := f.ErrorCodes[code]; !ok {
				t.Errorf("row %s: code %q has no errorCodes entry", r.Name, code)
			}
		}
		if !slices.IsSorted(r.Codes) {
			t.Errorf("row %s: codes %q are not sorted", r.Name, r.Codes)
		}
	}
	for _, c := range f.Cases {
		if c.Answer == nil || c.Answer.Kind != "refuse" {
			continue
		}
		spec, ok := f.ErrorCodes[c.Answer.Code]
		if !ok || c.Want.Failure != "http" || c.Want.Code != c.Answer.Code || c.Want.Status != spec.Status {
			t.Errorf("%s: refusing %q wants %+v, the catalog answers %d", c.Name, c.Answer.Code, c.Want, spec.Status)
		}
	}
	if len(f.ErrorCodes) == 0 {
		t.Fatalf("%s holds no errorCodes", transportFile)
	}
}
