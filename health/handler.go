package health

import (
	"cmp"
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
	// error text: the endpoint answers without authentication. An empty Reason
	// reads as "the app reports itself unhealthy".
	Reason string
}

// body is the JSON a health endpoint writes.
type body struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
}

// CheckHandler returns a handler that answers 200 {"status":"ok"} while every
// check is healthy, and 503 {"status":"unhealthy","reasons":[...]} listing the
// Reason of each failing check in order. It sets Content-Type:
// application/json, Cache-Control: no-store and X-Content-Type-Options:
// nosniff, and reads each Signal on every request. With no checks it answers
// 200. Register it as "GET /healthz": an http.ServeMux then also answers HEAD,
// and 405 to every other method.
func CheckHandler(checks ...Check) http.Handler {
	checks = slices.Clone(checks)
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var reasons []string
		for _, c := range checks {
			if c.Signal == nil || !c.Signal.Healthy() {
				reasons = append(reasons, cmp.Or(c.Reason, reasonUnhealthy))
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

// Handler returns CheckHandler for one signal, s, with the reason "the app
// reports itself unhealthy"; a nil s is unhealthy. With a *Marker it answers
// 503 while the marker is absent, also in degraded mode (a read-only /tmp),
// where the `health` subcommand reports healthy to avoid a restart loop; a
// leftover marker that still stats is healthy on both. So it must not be the
// only liveness probe of a service that may run read-only with no /tmp tmpfs.
func Handler(s Signal) http.Handler {
	return CheckHandler(Check{Signal: s, Reason: reasonUnhealthy})
}
