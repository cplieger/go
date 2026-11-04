package health

import (
	"encoding/json"
	"net/http"
	"time"
)

const (
	statusOK          = "OK"
	statusUnavailable = "Unavailable"
)

// Status is the JSON response emitted by Handler.
type Status struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// Handler returns an http.Handler that reports the health of the given
// Signal as a JSON object. Returns 200 with {"status":"OK"} when healthy,
// 503 with {"status":"Unavailable"} otherwise (including a nil Signal).
// This mirrors the response shape of hellofresh/health-go and satisfies
// K8s HTTP probe expectations.
//
// In degraded mode (unwritable marker directory), Marker.Healthy()
// returns false, so this endpoint reports 503 — unlike the `health`
// subcommand probe (ProbeCheck), which reports healthy to avoid a
// Docker restart loop. Do not wire this endpoint as the sole liveness
// probe on a service that may run with a read-only filesystem and no
// /tmp tmpfs, or it will restart-loop a container that is actually alive.
//
// With a *Marker signal, every request performs one os.Stat (see
// Marker.CheckHealthy); keep this endpoint behind the platform's probe
// cadence, not a request-per-second hot path.
func Handler(s Signal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		st := Status{Timestamp: time.Now().UTC().Format(time.RFC3339)}
		code := http.StatusOK
		if s != nil && s.Healthy() {
			st.Status = statusOK
		} else {
			st.Status = statusUnavailable
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(st)
	})
}
