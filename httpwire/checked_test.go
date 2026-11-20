package httpwire_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/go/httpwire"
)

// checkedFile is the open check's answers, written by this test and read by
// the TS half's checkChecked test, so a rule changed on one side fails both.
const (
	checkedFile  = "web/testdata/checked.json"
	checkedRegen = "regenerate with UPDATE_GOLDEN=1 go test . -run TestCheckedAnswers in httpwire"
)

type checkedItem struct {
	Name string `json:"name"`
}

// checkedPlus is an answer embedding Checked beside a field of its own.
type checkedPlus struct {
	ReadAt string `json:"read_at"`
	httpwire.Checked[checkedItem]
}

type caseName string

func (caseName) MaxBytes() int { return 64 }

type checkQuery struct {
	Case caseName `json:"case"`
}

type checkCase struct {
	value httpwire.Checked[checkedItem]
	name  string
	// want is the body the binder writes, a JSON value; "" when the answer
	// breaks a rule and the binder answers internal_error instead.
	want string
	plus bool // served by the row whose answer embeds Checked
}

func sources(s ...httpwire.SourceCheck[checkedItem]) httpwire.Checked[checkedItem] {
	return httpwire.Checked[checkedItem]{Sources: s}
}

// storePos is a Feed-Position value as the change feed writes it (sse
// docs/feed.md "Feed-Position"); posQ is it inside a JSON string.
const (
	storePos = `store=1234;epoch="9f2c4e1a7b3d5f60819a2b3c4d5e6f70"`
	posQ     = `"position":"store=1234;epoch=\"9f2c4e1a7b3d5f60819a2b3c4d5e6f70\""`
)

var (
	episode     = &checkedItem{Name: "Frieren S01E01"}
	sameSonarr  = httpwire.SourceCheck[checkedItem]{Source: "sonarr", Answer: httpwire.CheckSame}
	held        = &httpwire.Upstream{Service: "sonarr", Status: http.StatusServiceUnavailable, Message: "Service Unavailable"}
	updatedRow  = httpwire.SourceCheck[checkedItem]{Source: "sonarr", Answer: httpwire.CheckUpdated, Row: episode, Stored: true, Position: storePos}
	goneStored  = httpwire.SourceCheck[checkedItem]{Source: "sonarr", Answer: httpwire.CheckGone, Stored: true, Position: storePos}
	errorSonarr = httpwire.SourceCheck[checkedItem]{Source: "sonarr", Answer: httpwire.CheckError, Code: "upstream_held", Upstream: held}
)

func with(s httpwire.SourceCheck[checkedItem], edit func(*httpwire.SourceCheck[checkedItem])) httpwire.SourceCheck[checkedItem] {
	edit(&s)
	return s
}

// checkCases are one answer per rule SourceCheck's doc states, each valid
// answer with the body its rules give.
func checkCases() []checkCase {
	notStored := func(r httpwire.NotStored) func(*httpwire.SourceCheck[checkedItem]) {
		return func(s *httpwire.SourceCheck[checkedItem]) { s.Stored, s.NotStored, s.Position = false, r, "" }
	}
	return []checkCase{
		{name: "no-sources", value: httpwire.Checked[checkedItem]{}, want: `{"sources":[]}`},
		{name: "same", value: sources(sameSonarr), want: `{"sources":[{"source":"sonarr","answer":"same","stored":false}]}`},
		{name: "updated-stored", value: sources(updatedRow), want: `{"sources":[{"row":{"name":"Frieren S01E01"},"source":"sonarr","answer":"updated",` + posQ + `,"stored":true}]}`},
		{
			name: "updated-superseded", value: sources(with(updatedRow, notStored(httpwire.NotStoredSuperseded))),
			want: `{"sources":[{"row":{"name":"Frieren S01E01"},"source":"sonarr","answer":"updated","not_stored":"superseded","stored":false}]}`,
		},
		{
			name: "updated-store-unwritable", value: sources(with(updatedRow, notStored(httpwire.NotStoredUnwritable))),
			want: `{"sources":[{"row":{"name":"Frieren S01E01"},"source":"sonarr","answer":"updated","not_stored":"store_unwritable","stored":false}]}`,
		},
		{name: "gone-stored", value: sources(goneStored), want: `{"sources":[{"source":"sonarr","answer":"gone",` + posQ + `,"stored":true}]}`},
		{
			name: "gone-superseded", value: sources(with(goneStored, notStored(httpwire.NotStoredSuperseded))),
			want: `{"sources":[{"source":"sonarr","answer":"gone","not_stored":"superseded","stored":false}]}`,
		},
		{
			name: "error", value: sources(errorSonarr),
			want: `{"sources":[{"upstream":{"service":"sonarr","status":503,"message":"Service Unavailable"},"source":"sonarr","answer":"error","code":"upstream_held","stored":false}]}`,
		},
		{
			name: "error-no-upstream", value: sources(with(errorSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Upstream, s.Code = nil, "upstream_timeout" })),
			want: `{"sources":[{"source":"sonarr","answer":"error","code":"upstream_timeout","stored":false}]}`,
		},
		{
			name: "error-upstream-cleaned",
			value: sources(with(errorSonarr, func(s *httpwire.SourceCheck[checkedItem]) {
				s.Upstream = &httpwire.Upstream{Service: "sonarr", Status: 700, Message: "Bad\x1b[31m key\nretry\u202e"}
			})),
			want: `{"sources":[{"upstream":{"service":"sonarr","message":"Bad [31m key retry "},"source":"sonarr","answer":"error","code":"upstream_held","stored":false}]}`,
		},
		{
			name: "error-upstream-service-outside-grammar",
			value: sources(with(errorSonarr, func(s *httpwire.SourceCheck[checkedItem]) {
				s.Upstream = &httpwire.Upstream{Service: "Sonarr 4", Message: "x"}
			})),
			want: `{"sources":[{"source":"sonarr","answer":"error","code":"upstream_held","stored":false}]}`,
		},
		{
			name: "several-sources",
			value: sources(
				with(sameSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Source = "radarr" }),
				updatedRow,
				with(errorSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Source, s.Upstream = "anidb", nil }),
			),
			want: `{"sources":[{"source":"radarr","answer":"same","stored":false},{"row":{"name":"Frieren S01E01"},"source":"sonarr","answer":"updated",` + posQ + `,"stored":true},{"source":"anidb","answer":"error","code":"upstream_held","stored":false}]}`,
		},
		{
			name: "stored-two-sources-position", value: sources(with(goneStored, func(s *httpwire.SourceCheck[checkedItem]) {
				s.Position = `store=12;epoch="9f2c4e1a7b3d5f60819a2b3c4d5e6f70", live=7;epoch="41ab0c9d8e7f6a5b4c3d2e1f00112233"`
			})),
			want: `{"sources":[{"source":"sonarr","answer":"gone","position":"store=12;epoch=\"9f2c4e1a7b3d5f60819a2b3c4d5e6f70\", live=7;epoch=\"41ab0c9d8e7f6a5b4c3d2e1f00112233\"","stored":true}]}`,
		},
		{
			name: "stored-position-at-bound", value: sources(with(goneStored, func(s *httpwire.SourceCheck[checkedItem]) { s.Position = strings.Repeat("a", 4096) })),
			want: `{"sources":[{"source":"sonarr","answer":"gone","position":"` + strings.Repeat("a", 4096) + `","stored":true}]}`,
		},
		{name: "embedded-no-sources", plus: true, want: `{"read_at":"2026-11-17T10:00:05.1Z","sources":[]}`},
		{name: "embedded-source-twice", plus: true, value: sources(sameSonarr, goneStored)},

		{name: "source-empty", value: sources(with(sameSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Source = "" }))},
		{name: "source-twice", value: sources(sameSonarr, goneStored)},
		{name: "answer-unknown", value: sources(with(sameSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Answer = "maybe" }))},
		{name: "answer-empty", value: sources(with(sameSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Answer = "" }))},
		{name: "same-with-row", value: sources(with(sameSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Row = episode }))},
		{name: "same-stored", value: sources(with(sameSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Stored = true }))},
		{name: "same-not-stored", value: sources(with(sameSonarr, notStored(httpwire.NotStoredSuperseded)))},
		{name: "same-code", value: sources(with(sameSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Code = "upstream_held" }))},
		{name: "same-upstream", value: sources(with(sameSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Upstream = held }))},
		{name: "updated-without-row", value: sources(with(updatedRow, func(s *httpwire.SourceCheck[checkedItem]) { s.Row = nil }))},
		{name: "updated-neither-stored-nor-not-stored", value: sources(with(updatedRow, func(s *httpwire.SourceCheck[checkedItem]) { s.Stored = false }))},
		{name: "updated-stored-and-not-stored", value: sources(with(updatedRow, func(s *httpwire.SourceCheck[checkedItem]) { s.NotStored = httpwire.NotStoredSuperseded }))},
		{name: "updated-not-stored-unknown", value: sources(with(updatedRow, notStored("lost")))},
		{name: "updated-code", value: sources(with(updatedRow, func(s *httpwire.SourceCheck[checkedItem]) { s.Code = "upstream_held" }))},
		{name: "updated-upstream", value: sources(with(updatedRow, func(s *httpwire.SourceCheck[checkedItem]) { s.Upstream = held }))},
		{name: "gone-with-row", value: sources(with(goneStored, func(s *httpwire.SourceCheck[checkedItem]) { s.Row = episode }))},
		{name: "gone-neither-stored-nor-not-stored", value: sources(with(goneStored, func(s *httpwire.SourceCheck[checkedItem]) { s.Stored = false }))},
		{name: "gone-stored-and-not-stored", value: sources(with(goneStored, func(s *httpwire.SourceCheck[checkedItem]) { s.NotStored = httpwire.NotStoredUnwritable }))},
		{name: "gone-code", value: sources(with(goneStored, func(s *httpwire.SourceCheck[checkedItem]) { s.Code = "upstream_held" }))},
		{name: "error-without-code", value: sources(with(errorSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Code = "" }))},
		{name: "error-code-outside-grammar", value: sources(with(errorSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Code = "Upstream-Held" }))},
		{name: "error-with-row", value: sources(with(errorSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Row = episode }))},
		{name: "error-stored", value: sources(with(errorSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Stored = true }))},
		{name: "updated-stored-without-position", value: sources(with(updatedRow, func(s *httpwire.SourceCheck[checkedItem]) { s.Position = "" }))},
		{name: "gone-stored-without-position", value: sources(with(goneStored, func(s *httpwire.SourceCheck[checkedItem]) { s.Position = "" }))},
		{name: "updated-not-stored-with-position", value: sources(with(with(updatedRow, notStored(httpwire.NotStoredSuperseded)), func(s *httpwire.SourceCheck[checkedItem]) { s.Position = storePos }))},
		{name: "gone-not-stored-with-position", value: sources(with(with(goneStored, notStored(httpwire.NotStoredUnwritable)), func(s *httpwire.SourceCheck[checkedItem]) { s.Position = storePos }))},
		{name: "same-with-position", value: sources(with(sameSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Position = storePos }))},
		{name: "error-with-position", value: sources(with(errorSonarr, func(s *httpwire.SourceCheck[checkedItem]) { s.Position = storePos }))},
		{name: "stored-position-control-character", value: sources(with(goneStored, func(s *httpwire.SourceCheck[checkedItem]) { s.Position = "store=1\n" }))},
		{name: "stored-position-not-ascii", value: sources(with(goneStored, func(s *httpwire.SourceCheck[checkedItem]) { s.Position = "store=1;epoch=\"é\"" }))},
		{name: "stored-position-past-bound", value: sources(with(goneStored, func(s *httpwire.SourceCheck[checkedItem]) { s.Position = strings.Repeat("a", 4097) }))},
		{name: "error-not-stored", value: sources(with(errorSonarr, notStored(httpwire.NotStoredUnwritable)))},
	}
}

// checkedEntry is one answer of checkedFile.
type checkedEntry struct {
	Name  string          `json:"name"`
	Body  json.RawMessage `json:"body"`
	Valid bool            `json:"valid"`
}

func checkedAPI(t *testing.T, cases []checkCase, log *bytes.Buffer) *httpwire.API {
	t.Helper()
	doc, err := os.ReadFile("testdata/openapi/checked.json")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]checkCase{}
	for _, c := range cases {
		byName[c.name] = c
	}
	public := httpwire.Access{Class: httpwire.Public}
	row := func(name, path, summary string, rt reflect.Type) httpwire.Endpoint {
		return httpwire.Endpoint{
			Name: name, Method: http.MethodGet, Path: path, Summary: summary, Description: summary + ".",
			Access: public, Query: reflect.TypeFor[checkQuery](), Response: rt,
		}
	}
	tb := &httpwire.Table{Catalog: httpwire.NewCatalog(), Prefix: "/api", Endpoints: []httpwire.Endpoint{
		row("items.check", "/items/check", "Check an item", reflect.TypeFor[httpwire.Checked[checkedItem]]()),
		row("items.checkplus", "/items/checkplus", "Check an item with its read time", reflect.TypeFor[checkedPlus]()),
	}}
	api, err := httpwire.New(tb, []httpwire.Binding{
		httpwire.Bind("items.check", func(_ context.Context, c httpwire.Call[noP, checkQuery, noB, noH]) (httpwire.Checked[checkedItem], error) {
			return byName[string(c.Query.Case)].value, nil
		}),
		httpwire.Bind("items.checkplus", func(_ context.Context, c httpwire.Call[noP, checkQuery, noB, noH]) (checkedPlus, error) {
			return checkedPlus{ReadAt: "2026-11-17T10:00:05.1Z", Checked: byName[string(c.Query.Case)].value}, nil
		}),
	}, httpwire.WithSchema(doc), httpwire.WithLogger(slog.New(slog.NewJSONHandler(log, nil))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return api
}

// TestCheckedAnswers serves every case through the binder: a valid answer
// is written as its rules give, and one that breaks a rule answers
// internal_error with an ERROR naming the row. It writes checkedFile from
// the same cases.
func TestCheckedAnswers(t *testing.T) {
	cases := checkCases()
	var log bytes.Buffer
	api := checkedAPI(t, cases, &log)
	entries := make([]checkedEntry, 0, len(cases))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log.Reset()
			path := "/api/items/check?case="
			if c.plus {
				path = "/api/items/checkplus?case="
			}
			rec := serve(api, http.MethodGet, path+c.name, "")
			if c.want == "" {
				if rec.Code != http.StatusInternalServerError || envelopeOf(t, rec).Code != "internal_error" {
					t.Errorf("= %d %s, want internal_error for an answer that breaks its rules", rec.Code, rec.Body)
				}
				var logged struct{ Level, Row, Problem string }
				if err := json.Unmarshal(log.Bytes(), &logged); err != nil || logged.Level != "ERROR" || logged.Row == "" || logged.Problem == "" {
					t.Errorf("logged %q, want one ERROR naming the row and the problem", log.String())
				}
				return
			}
			if rec.Code != http.StatusOK || !jsonSame(t, rec.Body.Bytes(), []byte(c.want)) {
				t.Errorf("= %d %s, want 200 %s", rec.Code, rec.Body, c.want)
			}
		})
		body := []byte(c.want)
		if c.want == "" {
			var err error
			if body, err = json.Marshal(c.value); err != nil {
				t.Fatal(err)
			}
		}
		entries = append(entries, checkedEntry{Name: c.name, Body: body, Valid: c.want != ""})
	}
	want, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(checkedFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := os.ReadFile(checkedFile); err != nil || !bytes.Equal(got, want) {
		t.Errorf("%s is missing or stale (%v); %s, and the TS test reads it", checkedFile, err, checkedRegen)
	}
}

// jsonSame reports whether a and b are one JSON value.
func jsonSame(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("a want that is no JSON: %s", b)
	}
	return reflect.DeepEqual(x, y)
}
