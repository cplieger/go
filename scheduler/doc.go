// Package scheduler runs an app's work: small primitives for a containerized
// job runner, and one executor for an app with many kinds of job.
//
// # Container job runners
//
// ParseInterval reads a *_INTERVAL value as a cadence and a mode, RunLoop
// drives a startup-plus-ticker loop, Stamp records a restart-surviving
// last-run time, TryLock guards overlap with an advisory flock, Exclusive
// coalesces whole cycles across processes, and NewCommandRunner builds
// subprocesses that shut down gracefully. The trigger subpackage is the
// in-process alternative for a single-owner daemon. The flock-based
// primitives are Unix-only.
//
// # The executor
//
// An Executor runs every job an app starts, whoever asked: its own timer per
// scheduled scope, a person or a key through the API, or the CLI. Each
// KindSpec names a slot, phases, a retention and the Run func; the executor
// coalesces and covers requests, orders a slot's waiting jobs by Class,
// cancels with a protected commit step, records batch lines and proposals,
// and watches each scheduled scope's deadline through Fresh and Deadline. A
// Records implementation keeps one record per job.
//
// A job's upstream calls read as background work, with priority for an
// Interactive job: an app using callgate passes callgate.Background, adding
// callgate.WithPriority when Run.Class answers Interactive. The package does
// not import callgate.
package scheduler
