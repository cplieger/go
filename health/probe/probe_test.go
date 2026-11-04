package probe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// statusServer returns a test server that always responds with status.
func statusServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestURL_statusTable(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"ok", http.StatusOK, false},
		{"created", http.StatusCreated, false},
		{"no content", http.StatusNoContent, false},
		{"last_2xx_status", 299, false},
		{"not found", http.StatusNotFound, true},
		{"server error", http.StatusInternalServerError, true},
		{"service unavailable", http.StatusServiceUnavailable, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := statusServer(t, tc.status)
			err := URL(t.Context(), srv.URL)
			if (err != nil) != tc.wantErr {
				t.Errorf("URL(status %d) error = %v, wantErr %v", tc.status, err, tc.wantErr)
			}
		})
	}
}

// TestURL_statusErrorNamesCode pins that a non-2xx failure names the
// received status.
func TestURL_statusErrorNamesCode(t *testing.T) {
	srv := statusServer(t, http.StatusServiceUnavailable)
	err := URL(t.Context(), srv.URL)
	if err == nil {
		t.Fatal("URL(503) = nil, want error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error %q does not name the 503 status", err)
	}
}

func TestURL_followsRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/new", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if err := URL(t.Context(), srv.URL+"/old"); err != nil {
		t.Errorf("URL(redirect to 200) = %v, want nil", err)
	}
}

func TestURL_contextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := URL(ctx, srv.URL)
	if err == nil {
		t.Fatal("URL(hung endpoint) = nil, want deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want errors.Is(..., context.DeadlineExceeded)", err)
	}
}

func TestURL_connectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // now nothing listens there

	if err := URL(t.Context(), url); err == nil {
		t.Error("URL(closed server) = nil, want connection error")
	}
}

func TestURL_invalidURL(t *testing.T) {
	if err := URL(t.Context(), "://not-a-url"); err == nil {
		t.Error("URL(invalid URL) = nil, want build-request error")
	}
}

// TestURL_statusBoundary property-checks the 2xx decision: exactly
// [200,299] is healthy, everything else is unhealthy.
func TestURL_statusBoundary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			code = http.StatusInternalServerError
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)

	rapid.Check(t, func(rt *rapid.T) {
		code := rapid.IntRange(200, 599).Draw(rt, "status")
		err := URL(t.Context(), fmt.Sprintf("%s/%d", srv.URL, code))
		healthy := code <= 299
		if healthy != (err == nil) {
			rt.Fatalf("status %d: err = %v, want healthy=%v", code, err, healthy)
		}
	})
}

func TestCheck_allHealthy(t *testing.T) {
	a := statusServer(t, http.StatusOK)
	b := statusServer(t, http.StatusNoContent)

	var out strings.Builder
	code := Check(&out, DefaultTimeout, a.URL, b.URL)
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want empty", out.String())
	}
}

func TestCheck_oneFailureNamesOnlyThatURL(t *testing.T) {
	good := statusServer(t, http.StatusOK)
	bad := statusServer(t, http.StatusInternalServerError)

	var out strings.Builder
	code := Check(&out, DefaultTimeout, good.URL, bad.URL)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if !strings.Contains(out.String(), bad.URL) {
		t.Errorf("output %q does not name the failing URL %s", out.String(), bad.URL)
	}
	if strings.Contains(out.String(), good.URL+":") {
		t.Errorf("output %q names the healthy URL as a failure", out.String())
	}
}

// TestCheck_probesAllURLs pins that the check does not stop at the
// first failure.
func TestCheck_probesAllURLs(t *testing.T) {
	bad1 := statusServer(t, http.StatusInternalServerError)
	bad2 := statusServer(t, http.StatusNotFound)

	var out strings.Builder
	code := Check(&out, DefaultTimeout, bad1.URL, bad2.URL)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	for _, u := range []string{bad1.URL, bad2.URL} {
		if !strings.Contains(out.String(), u) {
			t.Errorf("output %q missing failure line for %s", out.String(), u)
		}
	}
}

func TestCheck_noURLs(t *testing.T) {
	var out strings.Builder
	code := Check(&out, DefaultTimeout)
	if code != 1 {
		t.Errorf("code = %d, want 1 (empty probe must not report healthy)", code)
	}
	if !strings.Contains(out.String(), "no URLs") {
		t.Errorf("output = %q, want a no-URLs message", out.String())
	}
}

func TestCheck_nonPositiveTimeout(t *testing.T) {
	srv := statusServer(t, http.StatusOK)

	var out strings.Builder
	code := Check(&out, 0, srv.URL)
	if code != 1 {
		t.Errorf("code = %d, want 1 (non-positive timeout fails immediately)", code)
	}
	if out.Len() == 0 {
		t.Error("output empty, want a failure line naming the URL")
	}
}
