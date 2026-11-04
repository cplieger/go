# Freshness deadline

This page is for a Go developer whose service runs its own work loop. It covers arming the opt-in freshness deadline, building it with `Lease`, when to leave it off, and reading a marker in-process with `Inspect`.

## Arming the deadline

By default the probe checks that the marker exists, and Docker's `--interval` decides how often. That check has a blind spot. Once `Set(true)` has run, a process whose work loop has deadlocked still passes every probe.

A service whose loop already calls `Set(true)` once per cycle can arm a deadline. A marker older than the deadline then probes unhealthy, and Docker marks the container unhealthy after the configured retries:

```go
if len(os.Args) > 1 && os.Args[1] == "health" {
    health.RunProbe(health.DefaultPath, health.WithMaxAge(
        health.Lease{Interval: interval, Cycles: 3}.Duration()))
}
```

Every `Set(true)` refreshes the marker's modification time, so the writing side needs no change. Pick a deadline comfortably above one interval plus the longest normal cycle. Three intervals is a reasonable default. A stale marker fails with a stderr line that gives its age and the deadline.

`CheckHealthy`, `Healthy` and `Handler` check existence only, whether or not the probe has a deadline.

## Building it with `Lease`

`WithMaxAge` treats a zero or negative deadline as disabled. Multiplying an interval inline can produce one by accident. A `time.Duration` is an int64 count of nanoseconds, so `3*interval` on an operator-supplied interval above about 854,015 hours wraps to a negative value. The probe then calls a stopped loop healthy for as long as the marker exists.

`Lease.Duration` builds the deadline from the service's own cadence and saturates at the largest `time.Duration` instead of wrapping:

- `Interval` is how often the service refreshes the marker. A zero or negative `Interval` disables the lease, whatever the other fields hold.
- `Cycles` is how many missed refreshes to tolerate.
- `Timeout` and `Attempts` add the longest time one unit of work may take, times the number of attempts one refresh may spend.
- `Floor` is the shortest deadline to arm, applied only when `Interval` is positive.

The deadline is `Cycles` × `Interval` plus `Attempts` × `Timeout`, and at least `Floor`. A lease with a 6-hour interval, 2 cycles, a 1-hour timeout and 1 attempt gives 13 hours. The zero `Lease` is disabled and reports 0, so a service with no interval of its own passes a zero lease and keeps the existence check.

## When to leave it off

Arm the deadline only where the main process runs its own work cycle at a known interval, so a stale marker means a stuck loop.

Leave it off for a service that a separate process triggers, for example a `docker exec` from a job scheduler that writes the marker. Such a service is healthy while it waits between triggers, and restarting it cannot fix a trigger that stopped firing.

## Reading a marker in-process with `Inspect`

`RunProbe` and `ProbeCheck` answer a healthcheck with 0 or 1. A long-running process that watches its own marker, for example to spot its own stuck loop, needs two things an exit code cannot carry. It needs the marker's age, to report it, and it needs to tell a stale marker from an absent one, because they call for opposite responses. A stale marker means the loop is stuck and a restart may clear it. An absent marker means nothing has written it yet, after a cold start or a wiped volume, and a restart changes nothing.

```go
switch f := health.Inspect(path, health.WithMaxAge(lease)); f.State {
case health.MarkerStale:
    log.Warn("work loop overdue", "age", f.Age, "lease", f.MaxAge)
    // act: nudge, restart, or surface it
case health.MarkerAbsent, health.MarkerUnreadable, health.MarkerDirUnavailable:
    // not evidence of a wedge; the probe already owns these
}
```

`Inspect` returns a `Freshness` with the `State`, the `Age`, the armed `MaxAge` and the stat error in `Err`. Its `Healthy` method gives the probe's verdict, and `Reason` gives the line `RunProbe` would write to stderr. The states are:

| State | Meaning |
| --- | --- |
| `MarkerFresh` | The marker exists and, with a deadline armed, is inside it |
| `MarkerStale` | The marker is older than the armed deadline. Only reachable with `WithMaxAge` |
| `MarkerAbsent` | No marker in a writable folder |
| `MarkerUnreadable` | The stat failed for another reason, such as permissions or an I/O error |
| `MarkerDirUnavailable` | Degraded mode. The folder cannot be written, and the probe treats this as healthy |

`RunProbe` and `ProbeCheck` are built on `Inspect`, so a process acting on `Inspect` and the container healthcheck reach the same verdict. Without `WithMaxAge`, a present marker is always `MarkerFresh`.
