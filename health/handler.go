package health

import (
	"encoding/json"
	"net/http"
	"slices"
)

// The fleet health body's status values.
const (
	statusOK        = "ok"
	statusUnhealthy = "unhealthy"
)

// reasonUnhealthy is the reason Handler gives for its one signal.
const reasonUnhealthy = "the app reports itself unhealthy"

// Check is one condition a health endpoint reports.
type Check struct {
	// Signal decides the check. A nil Signal is unhealthy.
	Signal Signal
	// Reason is one plain sentence, written where the app wires the check,
	// that "reasons" lists while Signal reports unhealthy. Never build it from
	// error text: the endpoint answers without authentication.
	Reason string
}

// body is the JSON a health endpoint writes.
type body struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
}

// CheckHandler returns a handler that reports checks, in order. It answers 200
// with {"status":"ok"} while every check is healthy, and 503 with
// {"status":"unhealthy","reasons":[...]} otherwise, listing the Reason of each
// failing check. Both carry Content-Type: application/json,
// Cache-Control: no-store and X-Content-Type-Options: nosniff. With no checks
// it always answers 200.
//
// Register it as "GET /healthz" on an http.ServeMux, which also answers HEAD
// and answers 405 to every other method. The handler reads each Signal on
// every request; a *Marker costs one os.Stat.
func CheckHandler(checks ...Check) http.Handler {
	checks = slices.Clone(checks)
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var reasons []string
		for _, c := range checks {
			if c.Signal == nil || !c.Signal.Healthy() {
				reasons = append(reasons, c.Reason)
			}
		}
		b, code := body{Status: statusOK}, http.StatusOK
		if len(reasons) > 0 {
			b, code = body{Status: statusUnhealthy, Reasons: reasons}, http.StatusServiceUnavailable
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(b)
	})
}

// Handler returns CheckHandler for one signal, s, with the reason
// "the app reports itself unhealthy". A nil s is unhealthy.
//
// In degraded mode (an unwritable marker directory), Marker.Healthy reports
// false, so this endpoint answers 503 while the `health` subcommand
// (ProbeCheck) reports healthy to avoid a Docker restart loop. Do not wire this
// endpoint as the only liveness probe of a service that may run with a
// read-only filesystem and no /tmp tmpfs, or it will restart a container that
// is alive.
func Handler(s Signal) http.Handler {
	return CheckHandler(Check{Signal: s, Reason: reasonUnhealthy})
}
