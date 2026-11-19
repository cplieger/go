package httpwire_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpwire"
)

// The golden envelope fixtures live in the TS half's package, whose own test
// gives each file's body the same verdict through decodeEnvelope.
const (
	envelopeDir        = "web/testdata/envelope"
	envelopeModuleFile = "web/src/testing/envelope-fixtures.ts"
	envelopeRegen      = "regenerate with UPDATE_GOLDEN=1 go test . -run TestEnvelopeFixtures in httpwire, then run the TS half's envelope test"
)

type envelopeFixture struct {
	Body  string `json:"body"`
	Valid bool   `json:"valid"`
}

// writerCase is an answer written by a production writer, with the status and
// decoded envelope the wire rules give it.
type writerCase struct {
	write  func(w http.ResponseWriter, r *http.Request)
	want   httpwire.Envelope
	name   string
	status int
}

func catalogWrites(t *testing.T, e *httpwire.Error) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	c := httpwire.NewCatalog()
	if err := c.Add("arr_refused", httpserver.CodeSpec{Status: http.StatusBadGateway, Remediation: "Check the Sonarr connection in Settings."}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRemediation("not_found", "Check the row id in the list."); err != nil {
		t.Fatal(err)
	}
	return func(w http.ResponseWriter, r *http.Request) { c.WriteError(w, r, e) }
}

func writerCases(t *testing.T) []writerCase {
	t.Helper()
	server := httpserver.CodeSpecs()
	library := httpwire.NewCatalog()
	words := func(code string) string {
		s, _ := library.Lookup(code)
		return s.Remediation
	}
	const sonarr = "Check the Sonarr connection in Settings."
	return []writerCase{
		{
			name: "catalog-stale-version", status: 412,
			write: catalogWrites(t, &httpwire.Error{Code: "stale_version", Message: "Another tab saved this row first.", Reason: "row_changed"}),
			want:  httpwire.Envelope{Code: "stale_version", Message: "Another tab saved this row first.", Remediation: words("stale_version"), Reason: "row_changed", RequestID: "req-1"},
		},
		{
			name: "catalog-bad-body-fields", status: 400,
			write: catalogWrites(t, &httpwire.Error{Code: "bad_body", Message: "Fix the marked fields.", Fields: []httpwire.FieldError{
				{Path: "retention.days", Code: "range", Message: "Use 1 to 400."},
				{Path: "items[1].key", Code: "required", Message: "Name the key."},
			}}),
			want: httpwire.Envelope{Code: "bad_body", Message: "Fix the marked fields.", Remediation: words("bad_body"), RequestID: "req-1", Fields: []httpwire.FieldError{
				{Path: "retention.days", Code: "range", Message: "Use 1 to 400."},
				{Path: "items[1].key", Code: "required", Message: "Name the key."},
			}},
		},
		{
			name: "catalog-retry-after", status: 429,
			write: catalogWrites(t, &httpwire.Error{Code: "rate_limited", Message: "Too many requests.", RetryAfter: 1500 * time.Millisecond}),
			want:  httpwire.Envelope{Code: "rate_limited", Message: "Too many requests.", Remediation: server["rate_limited"].Remediation, RequestID: "req-1", RetryAfterMS: 1500},
		},
		{
			name: "catalog-upstream", status: 502,
			write: catalogWrites(t, &httpwire.Error{Code: "arr_refused", Message: "Sonarr refused the search.", Upstream: &httpwire.Upstream{Service: "sonarr", Status: 401, Message: "Unauthorized"}}),
			want:  httpwire.Envelope{Code: "arr_refused", Message: "Sonarr refused the search.", Remediation: sonarr, RequestID: "req-1", Upstream: &httpwire.Upstream{Service: "sonarr", Status: 401, Message: "Unauthorized"}},
		},
		{
			name: "catalog-upstream-no-status", status: 502,
			write: catalogWrites(t, &httpwire.Error{Code: "arr_refused", Message: "Sonarr did not answer.", Upstream: &httpwire.Upstream{Service: "sonarr", Message: "connection refused"}}),
			want:  httpwire.Envelope{Code: "arr_refused", Message: "Sonarr did not answer.", Remediation: sonarr, RequestID: "req-1", Upstream: &httpwire.Upstream{Service: "sonarr", Message: "connection refused"}},
		},
		{
			name: "catalog-upstream-status-outside-range", status: 502,
			write: catalogWrites(t, &httpwire.Error{Code: "arr_refused", Message: "Sonarr answered oddly.", Upstream: &httpwire.Upstream{Service: "sonarr", Status: 42, Message: "odd"}}),
			want:  httpwire.Envelope{Code: "arr_refused", Message: "Sonarr answered oddly.", Remediation: sonarr, RequestID: "req-1", Upstream: &httpwire.Upstream{Service: "sonarr", Message: "odd"}},
		},
		{
			name: "catalog-upstream-message-long", status: 502,
			write: catalogWrites(t, &httpwire.Error{Code: "arr_refused", Message: "Sonarr refused the search.", Upstream: &httpwire.Upstream{Service: "sonarr", Status: 500, Message: strings.Repeat("a", 600)}}),
			want:  httpwire.Envelope{Code: "arr_refused", Message: "Sonarr refused the search.", Remediation: sonarr, RequestID: "req-1", Upstream: &httpwire.Upstream{Service: "sonarr", Status: 500, Message: strings.Repeat("a", 509) + "…"}},
		},
		{
			name: "catalog-upstream-message-control", status: 502,
			write: catalogWrites(t, &httpwire.Error{Code: "arr_refused", Message: "Sonarr refused the search.", Upstream: &httpwire.Upstream{Service: "sonarr", Status: 400, Message: "Bad\x1b[31m key\nretry\u202e"}}),
			want:  httpwire.Envelope{Code: "arr_refused", Message: "Sonarr refused the search.", Remediation: sonarr, RequestID: "req-1", Upstream: &httpwire.Upstream{Service: "sonarr", Status: 400, Message: "Bad [31m key retry "}},
		},
		{
			name: "catalog-internal-error", status: 500,
			write: catalogWrites(t, &httpwire.Error{Code: "internal_error", Message: "open /var/lib/app/store.db: permission denied"}),
			want:  httpwire.Envelope{Code: "internal_error", Message: "internal error", Remediation: server["internal_error"].Remediation, RequestID: "req-1"},
		},
		{
			name: "catalog-unlisted-code", status: 500,
			write: catalogWrites(t, &httpwire.Error{Code: "no_such_code", Message: "This never crosses."}),
			want:  httpwire.Envelope{Code: "internal_error", Message: "internal error", Remediation: server["internal_error"].Remediation, RequestID: "req-1"},
		},
		{
			name: "catalog-remediation-replaced", status: 404,
			write: catalogWrites(t, &httpwire.Error{Code: "not_found", Message: "No such row."}),
			want:  httpwire.Envelope{Code: "not_found", Message: "No such row.", Remediation: "Check the row id in the list.", RequestID: "req-1"},
		},
		{
			name: "server-default-not-found", status: 404,
			write: func(w http.ResponseWriter, r *http.Request) {
				httpserver.WriteError(w, r, http.StatusNotFound, "not_found", "Nothing is served at this path.")
			},
			want: httpwire.Envelope{Code: "not_found", Message: "Nothing is served at this path.", Remediation: server["not_found"].Remediation, RequestID: "req-1"},
		},
		{
			name: "server-default-outside-specs", status: 400,
			write: func(w http.ResponseWriter, r *http.Request) {
				httpserver.WriteError(w, r, http.StatusBadRequest, "app_code", "Refused.")
			},
			want: httpwire.Envelope{Code: "app_code", Message: "Refused.", Remediation: "", RequestID: "req-1"},
		},
	}
}

// member is one raw JSON member of a hand-written envelope body.
type member struct{ key, raw string }

func quoted(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// envelopeBody writes a valid minimal envelope, with each override replacing
// or adding a member and each member whose raw value is "" left out.
func envelopeBody(overrides ...member) string {
	ms := []member{{"code", `"not_found"`}, {"message", `"No such row."`}, {"remediation", `"Check the id."`}, {"request_id", `"req-1"`}}
	for _, o := range overrides {
		if i := slices.IndexFunc(ms, func(m member) bool { return m.key == o.key }); i >= 0 {
			ms[i] = o
		} else {
			ms = append(ms, o)
		}
	}
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		if m.raw != "" {
			parts = append(parts, quoted(m.key)+":"+m.raw)
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func fieldsOf(n int) string {
	f := make([]string, n)
	for i := range f {
		f[i] = `{"path":"items[` + strconv.Itoa(i) + `]","code":"required","message":"Name it."}`
	}
	return "[" + strings.Join(f, ",") + "]"
}

func upstreamOf(service, status, message string) string {
	return envelopeBody(member{"upstream", "{" + strings.Join(slices.DeleteFunc([]string{service, status, message}, func(s string) bool { return s == "" }), ",") + "}"})
}

// handCases are bodies no production writer produces, each with the verdict
// the decoding rules give it.
func handCases() map[string]envelopeFixture {
	at4KiB := strings.Repeat("é", 2048)
	at512 := strings.Repeat("é", 256)
	valid := map[string]string{
		"valid-minimal":                 envelopeBody(),
		"valid-empty-strings":           envelopeBody(member{"message", `""`}, member{"remediation", `""`}, member{"request_id", `""`}),
		"valid-unknown-member":          envelopeBody(member{"extra", `{"a":[1,null]}`}),
		"valid-string-at-cap":           envelopeBody(member{"message", quoted(at4KiB)}),
		"valid-reason-at-cap":           envelopeBody(member{"reason", quoted(strings.Repeat("a", 64))}),
		"valid-fields-at-cap":           envelopeBody(member{"fields", fieldsOf(100)}),
		"valid-fields-empty":            envelopeBody(member{"fields", `[]`}),
		"valid-retry-after-zero":        envelopeBody(member{"retry_after_ms", `0`}),
		"valid-retry-after-exponent":    envelopeBody(member{"retry_after_ms", `1.5e3`}),
		"valid-retry-after-max-safe":    envelopeBody(member{"retry_after_ms", `9007199254740991`}),
		"valid-upstream-status-100":     upstreamOf(`"service":"github"`, `"status":100`, `"message":"Continue"`),
		"valid-upstream-status-599":     upstreamOf(`"service":"github"`, `"status":599`, `"message":"x"`),
		"valid-upstream-status-decimal": upstreamOf(`"service":"github"`, `"status":4.01e2`, `"message":"x"`),
		"valid-upstream-message-at-cap": upstreamOf(`"service":"github"`, "", `"message":`+quoted(at512)),
	}
	refused := map[string]string{
		"refused-not-json":                    `{"code":`,
		"refused-not-object":                  `[]`,
		"refused-null":                        `null`,
		"refused-legacy-error-key":            `{"error":"No such row.","code":"not_found","request_id":"req-1"}`,
		"refused-missing-code":                envelopeBody(member{"code", ""}),
		"refused-missing-message":             envelopeBody(member{"message", ""}),
		"refused-missing-remediation":         envelopeBody(member{"remediation", ""}),
		"refused-missing-request-id":          envelopeBody(member{"request_id", ""}),
		"refused-code-empty":                  envelopeBody(member{"code", `""`}),
		"refused-code-not-grammar":            envelopeBody(member{"code", `"Not_Found"`}),
		"refused-code-null":                   envelopeBody(member{"code", `null`}),
		"refused-message-not-string":          envelopeBody(member{"message", `5`}),
		"refused-message-null":                envelopeBody(member{"message", `null`}),
		"refused-string-over-cap":             envelopeBody(member{"message", quoted(at4KiB + "a")}),
		"refused-request-id-over-cap":         envelopeBody(member{"request_id", quoted(strings.Repeat("r", 4097))}),
		"refused-reason-null":                 envelopeBody(member{"reason", `null`}),
		"refused-reason-over-cap":             envelopeBody(member{"reason", quoted(strings.Repeat("a", 65))}),
		"refused-reason-not-grammar":          envelopeBody(member{"reason", `"row changed"`}),
		"refused-fields-over-cap":             envelopeBody(member{"fields", fieldsOf(101)}),
		"refused-fields-not-array":            envelopeBody(member{"fields", `{}`}),
		"refused-fields-null":                 envelopeBody(member{"fields", `null`}),
		"refused-field-not-object":            envelopeBody(member{"fields", `["items"]`}),
		"refused-field-missing-message":       envelopeBody(member{"fields", `[{"path":"a","code":"required"}]`}),
		"refused-field-code-not-string":       envelopeBody(member{"fields", `[{"path":"a","code":1,"message":"x"}]`}),
		"refused-field-path-over-cap":         envelopeBody(member{"fields", `[{"path":` + quoted(at4KiB+"a") + `,"code":"required","message":"x"}]`}),
		"refused-retry-after-negative":        envelopeBody(member{"retry_after_ms", `-1`}),
		"refused-retry-after-fraction":        envelopeBody(member{"retry_after_ms", `1.5`}),
		"refused-retry-after-unsafe":          envelopeBody(member{"retry_after_ms", `9007199254740993`}),
		"refused-retry-after-string":          envelopeBody(member{"retry_after_ms", `"5"`}),
		"refused-retry-after-null":            envelopeBody(member{"retry_after_ms", `null`}),
		"refused-upstream-not-object":         envelopeBody(member{"upstream", `"sonarr"`}),
		"refused-upstream-null":               envelopeBody(member{"upstream", `null`}),
		"refused-upstream-missing-service":    upstreamOf("", `"status":401`, `"message":"x"`),
		"refused-upstream-missing-message":    upstreamOf(`"service":"sonarr"`, `"status":401`, ""),
		"refused-upstream-service-grammar":    upstreamOf(`"service":"Sonarr"`, `"status":401`, `"message":"x"`),
		"refused-upstream-status-below-range": upstreamOf(`"service":"sonarr"`, `"status":99`, `"message":"x"`),
		"refused-upstream-status-above-range": upstreamOf(`"service":"sonarr"`, `"status":600`, `"message":"x"`),
		"refused-upstream-status-zero":        upstreamOf(`"service":"sonarr"`, `"status":0`, `"message":"x"`),
		"refused-upstream-status-fraction":    upstreamOf(`"service":"sonarr"`, `"status":401.5`, `"message":"x"`),
		"refused-upstream-status-null":        upstreamOf(`"service":"sonarr"`, `"status":null`, `"message":"x"`),
		"refused-upstream-status-string":      upstreamOf(`"service":"sonarr"`, `"status":"401"`, `"message":"x"`),
		"refused-upstream-message-over-cap":   upstreamOf(`"service":"sonarr"`, "", `"message":`+quoted(at512+"a")),
	}
	out := make(map[string]envelopeFixture, len(valid)+len(refused))
	for name, body := range valid {
		out[name] = envelopeFixture{Body: body, Valid: true}
	}
	for name, body := range refused {
		out[name] = envelopeFixture{Body: body}
	}
	return out
}

func encodeFixture(t *testing.T, f envelopeFixture) []byte {
	t.Helper()
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// envelopeModule is the ENVELOPE_FIXTURES module @cplieger/httpwire/testing
// exports, since the package ships src/ and not testdata/. Each body is
// written in the quote prettier picks (single when it holds more double
// quotes than single ones), so prettier --check leaves the file as written.
func envelopeModule(t *testing.T, want map[string]envelopeFixture) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString("// Code generated by httpwire's TestEnvelopeFixtures from testdata/envelope; DO NOT EDIT.\n\n" +
		"export const ENVELOPE_FIXTURES: readonly {\n" +
		"  readonly name: string;\n  readonly body: string;\n  readonly valid: boolean;\n}[] = [\n")
	for _, name := range slices.Sorted(maps.Keys(want)) {
		f := want[name]
		fmt.Fprintf(&b, "  {\n    name: %s,\n    body: %s,\n    valid: %t,\n  },\n", tsString(t, name), tsString(t, f.Body), f.Valid)
	}
	b.WriteString("];\n")
	return []byte(b.String())
}

func tsString(t *testing.T, s string) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		t.Fatal(err)
	}
	quoted := strings.TrimSuffix(buf.String(), "\n")
	if strings.Count(s, `"`) <= strings.Count(s, `'`) {
		return quoted
	}
	inner := quoted[1 : len(quoted)-1]
	inner = strings.ReplaceAll(inner, `\"`, `"`)
	inner = strings.ReplaceAll(inner, `'`, `\'`)
	return "'" + inner + "'"
}

func TestEnvelopeFixtures(t *testing.T) {
	want := handCases()
	cases := writerCases(t)
	for _, wc := range cases {
		rec := httptest.NewRecorder()
		wc.write(rec, requestWithID(t, "req-1"))
		if rec.Code != wc.status {
			t.Errorf("%s: status %d; want %d", wc.name, rec.Code, wc.status)
		}
		want[wc.name] = envelopeFixture{Body: rec.Body.String(), Valid: true}
	}

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.RemoveAll(envelopeDir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(envelopeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, f := range want {
			if err := os.WriteFile(filepath.Join(envelopeDir, name+".json"), encodeFixture(t, f), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(envelopeModuleFile, envelopeModule(t, want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := os.ReadFile(envelopeModuleFile); err != nil || !bytes.Equal(got, envelopeModule(t, want)) {
		t.Errorf("%s is missing or stale (%v); %s", envelopeModuleFile, err, envelopeRegen)
	}

	files, err := filepath.Glob(filepath.Join(envelopeDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures under %s (%v); %s", envelopeDir, err, envelopeRegen)
	}
	for _, path := range files {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		t.Run(name, func(t *testing.T) {
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f, ok := want[name]
			if !ok {
				t.Fatalf("%s has no case in this test; %s", path, envelopeRegen)
			}
			if !bytes.Equal(got, encodeFixture(t, f)) {
				t.Errorf("%s is stale; %s", path, envelopeRegen)
			}
			var committed envelopeFixture
			if err := json.Unmarshal(got, &committed); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			_, err = httpwire.DecodeEnvelope([]byte(committed.Body))
			if (err == nil) != committed.Valid {
				t.Errorf("DecodeEnvelope = %v; the fixture says valid %v", err, committed.Valid)
			}
		})
	}
	for name := range want {
		if _, err := os.Stat(filepath.Join(envelopeDir, name+".json")); errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s.json is missing; %s", name, envelopeRegen)
		}
	}

	for _, wc := range cases {
		t.Run("decoded_"+wc.name, func(t *testing.T) {
			got, err := httpwire.DecodeEnvelope([]byte(want[wc.name].Body))
			if err != nil {
				t.Fatal(err)
			}
			if !envelopeEqual(got, wc.want) {
				t.Errorf("decoded %+v; want %+v", got, wc.want)
			}
		})
	}
}
