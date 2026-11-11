package sse

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// FuzzParseScope_admitsOnlyTheGrammar holds three invariants over any body: a
// scope it accepts names at most MaxScopeTopics topics, each passing
// ValidTopic; re-encoding an accepted set and parsing it again yields the same
// set; and JSON escapes nothing in it (with HTML escaping off, as a browser's
// JSON.stringify), so the header and the body forms are byte-identical.
func FuzzParseScope_admitsOnlyTheGrammar(f *testing.F) {
	for _, seed := range []string{
		`null`, `[]`, `["run:a","run:b"]`, `["run:a","run:a"]`, `[""]`, `["run a"]`,
		`["run:\u00e9"]`, "[\"run:\xc3\xa9\"]", `["run:日本"]`, `["run:😀"]`, `["a\"b"]`, `["a\\b"]`,
		`{"topics":[]}`, `"run:a"`, `[1]`, ` null `, `[null]`, `["&"]`, `["<>"]`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		s, err := parseScope(raw)
		if err != nil || !s.scoped {
			return
		}
		if len(s.topics) > MaxScopeTopics {
			t.Fatalf("accepted %d topics, above MaxScopeTopics", len(s.topics))
		}
		topics := make([]string, 0, len(s.topics))
		for topic := range s.topics {
			if !ValidTopic(topic) {
				t.Fatalf("accepted %q outside the grammar", topic)
			}
			topics = append(topics, topic)
		}
		slices.Sort(topics)
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(topics); err != nil {
			t.Fatal(err)
		}
		encoded := bytes.TrimSpace(buf.Bytes())
		if bytes.ContainsRune(encoded, '\\') {
			t.Fatalf("an accepted set encodes with an escape: %s", encoded)
		}
		again, err := parseScope(encoded)
		if err != nil || len(again.topics) != len(s.topics) {
			t.Fatalf("re-parsing %s = (%d topics, %v), want %d", encoded, len(again.topics), err, len(s.topics))
		}
		for _, topic := range topics {
			if !again.matches(topic) {
				t.Fatalf("re-parsing %s lost %q", encoded, topic)
			}
		}
	})
}

// FuzzScopeHandler_neverFailsOpen sends any body to a handler whose hub holds
// one rescopable connection: the handler never answers 5xx, and it answers 200
// only for a body naming that connection with a positive seq and a scope in
// the grammar.
func FuzzScopeHandler_neverFailsOpen(f *testing.F) {
	h := mustNew(f, withEpoch(testEpoch))
	sub, err := h.subscribe(subRequest{scope: All(), owner: "p", rescopable: true}, Cursor{}, false)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { h.unsubscribe(sub.c) })
	handler, err := h.ScopeHandler(func(*http.Request, any) error { return nil })
	if err != nil {
		f.Fatal(err)
	}
	conn := sub.hello.Conn
	for _, seed := range []string{
		`{"conn":"` + conn + `","seq":1,"topics":["run:a"]}`,
		`{"conn":"` + conn + `","seq":2,"topics":null}`,
		`{"conn":"` + conn + `","seq":3}`,
		`{"conn":"` + conn + `","seq":-1,"topics":[]}`,
		`{"conn":"` + conn + `","seq":9007199254740993,"topics":[]}`,
		`{"conn":"x","seq":1,"topics":[]}`,
		`{"conn":"` + conn + `","seq":4,"topics":["run:\u00e9"]}`,
		`{"conn":"` + conn + `","seq":5,"topics":[""]}`,
		`[]`, `null`, `{`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		r := httptest.NewRequest(http.MethodPost, "/scope", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code >= 500 {
			t.Fatalf("answered %d for %q", w.Code, body)
		}
		if w.Code != http.StatusOK {
			return
		}
		var req scopeRequest
		if err := json.Unmarshal(body, &req); err != nil || req.Conn != conn || req.Seq <= 0 {
			t.Fatalf("answered 200 for %q", body)
		}
		if _, err := parseScope(req.Topics); err != nil {
			t.Fatalf("answered 200 for a scope outside the grammar: %q", body)
		}
	})
}
