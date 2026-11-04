package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubSignal struct{ healthy bool }

func (s stubSignal) Healthy() bool { return s.healthy }

func TestHandler_Healthy(t *testing.T) {
	h := Handler(stubSignal{healthy: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if xcto := rec.Header().Get("X-Content-Type-Options"); xcto != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", xcto)
	}
	var st Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Status != "OK" {
		t.Fatalf("status = %q, want OK", st.Status)
	}
	if st.Timestamp == "" {
		t.Fatal("timestamp is empty")
	}
}

func TestHandler_NilSignal(t *testing.T) {
	h := Handler(nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var st Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Status != "Unavailable" {
		t.Fatalf("status = %q, want Unavailable", st.Status)
	}
	if st.Timestamp == "" {
		t.Fatal("timestamp is empty")
	}
}

func TestHandler_Unhealthy(t *testing.T) {
	h := Handler(stubSignal{healthy: false})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var st Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Status != "Unavailable" {
		t.Fatalf("status = %q, want Unavailable", st.Status)
	}
}

// TestHandler_emitsDocumentedWireShape pins the on-the-wire JSON contract
// ({"status":"OK","timestamp":...}). The other handler tests decode into
// Status, so a json-tag rename would round-trip clean and pass them; this
// inspects the raw body bytes and the RFC3339 timestamp instead.
func TestHandler_emitsDocumentedWireShape(t *testing.T) {
	okRec := httptest.NewRecorder()
	Handler(stubSignal{healthy: true}).ServeHTTP(okRec, httptest.NewRequest(http.MethodGet, "/health", nil))
	okBody := strings.TrimSpace(okRec.Body.String())
	if !strings.Contains(okBody, `"status":"OK"`) {
		t.Errorf("healthy body = %q, want it to contain the literal \"status\":\"OK\"", okBody)
	}
	if !strings.Contains(okBody, `"timestamp":`) {
		t.Errorf("healthy body = %q, want a \"timestamp\" key", okBody)
	}

	badRec := httptest.NewRecorder()
	Handler(stubSignal{healthy: false}).ServeHTTP(badRec, httptest.NewRequest(http.MethodGet, "/health", nil))
	badBody := strings.TrimSpace(badRec.Body.String())
	if !strings.Contains(badBody, `"status":"Unavailable"`) {
		t.Errorf("unhealthy body = %q, want the literal \"status\":\"Unavailable\"", badBody)
	}

	var st Status
	if err := json.Unmarshal([]byte(okBody), &st); err != nil {
		t.Fatalf("decode healthy body: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, st.Timestamp); err != nil {
		t.Errorf("Timestamp = %q, not parseable as RFC3339: %v", st.Timestamp, err)
	}
}
