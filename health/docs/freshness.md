# Freshness deadline

This page is for a Go developer whose service runs its own work loop. It covers arming the opt-in freshness deadline, building it with `Lease`, and when to leave it off.

## Arming the deadline

A verdict alone has a blind spot. Once `Set(true)` has run, a process whose work loop has deadlocked still reports healthy. A service whose loop calls `Set(true)` once per cycle can arm a deadline, called a lease, and fail once the loop stops refreshing the verdict.

Build one lease and pass its `Duration` to both readers: `Fresh` for `/healthz`, and `WithMaxAge` for `RunProbe` when the service has a marker.

```go
lease := health.Lease{Interval: interval, Cycles: 3}.Duration()

if len(os.Args) > 1 && os.Args[1] == "health" {
    health.RunProbe(path, health.WithMaxAge(lease)) // exits 0 or 1
}

st := health.NewStatus(health.WithMarker(path))
mux.Handle("GET /healthz", health.CheckHandler(
    health.Check{Signal: st, Reason: "the last run failed"},
    health.Check{Signal: st.Fresh(lease), Reason: "the work loop has stopped"},
))
```

Every healthy `Set` restarts the age, so the writing side needs no change. Pick a lease comfortably above one interval plus the longest normal cycle. Three intervals is a reasonable default. A stale marker fails the probe with a stderr line that gives its age and the lease, and a stale verdict fails `/healthz` with `Fresh`'s reason.

The two ages match while the wall clock does not step. `Fresh` measures with the monotonic clock, and `RunProbe` with the file's modification time. A forward clock step larger than the lease fails the file probe once while `Fresh` still passes, and the next healthy `Set` clears it.

## Building it with `Lease`

`Fresh` and `WithMaxAge` treat a zero or negative deadline as disabled. Multiplying an interval inline can produce one by accident. A `time.Duration` is an int64 count of nanoseconds, so `3*interval` on an operator-supplied interval above about 854,015 hours wraps to a negative value. Both readers then call a stopped loop healthy.

`Lease.Duration` builds the deadline from the service's own cadence and saturates at the largest `time.Duration` instead of wrapping:

- `Interval` is how often the service records a healthy verdict. A zero or negative `Interval` disables the lease, whatever the other fields hold.
- `Cycles` is how many missed refreshes to tolerate.
- `Timeout` and `Attempts` add the longest time one unit of work may take, times the number of attempts one refresh may spend.
- `Floor` is the shortest deadline to arm, applied only when `Interval` is positive.

The deadline is `Cycles` × `Interval` plus `Attempts` × `Timeout`, and at least `Floor`. A lease with a 6-hour interval, 2 cycles, a 1-hour timeout and 1 attempt gives 13 hours. The zero `Lease` is disabled and reports 0, so a service with no interval of its own passes a zero lease and keeps the plain verdict.

## When to leave it off

Arm the lease only where the main process runs its own work cycle at a known interval, so an old verdict means a stuck loop.

Leave it off for a service that something outside it wakes, such as a job scheduler that triggers its work, because such a service is healthy while it waits between triggers and restarting it cannot fix a trigger that stopped firing. Leave it off as well for a service that sets its verdict once at start. Both kinds pass a zero `Lease`, which disables the deadline.
