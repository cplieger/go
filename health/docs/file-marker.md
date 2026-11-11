# The marker file

This page is for a Go developer whose service opens no port by default, so its healthcheck cannot ask `/healthz`. It covers how the marker works, where it lives, a shared `/dev/shm`, the one-writer rule, degraded mode and write failures.

## How it works

The long-running process creates its Status with `WithMarker`, and every write is mirrored to a file: `Set(true)` creates the marker or refreshes its modification time, and `Set(false)` and `BeginDrain` remove it. The healthcheck is the same binary, started by Docker with a `health` argument. It runs `RunProbe` on the same path and exits. Kubernetes can run the same subcommand as an exec liveness probe.

```go
func main() {
    path := health.MarkerPath("myservice")
    if len(os.Args) > 1 && os.Args[1] == "health" {
        health.RunProbe(path) // exits 0 or 1
    }

    st := health.NewStatus(health.WithMarker(path))
    st.Set(true)

    // ... run the service, calling st.Set(false) when it cannot do its work,
    // and st.BeginDrain() when shutdown begins ...
}
```

Unless it starts in degraded mode, `NewStatus` removes any marker an earlier run left, so the file starts as unhealthy as the verdict. `RunProbe` exits 0 while the marker exists. It exits 1 when the marker is absent from a writable folder, or when it cannot read the marker for another reason, and writes one line to stderr naming the cause, such as `unhealthy: marker absent`.

With `WithMaxAge(d)`, `RunProbe` also fails a marker older than `d`. Every healthy `Set` refreshes the file, so the probe reads the same age `Fresh(d)` reads in memory while the wall clock does not step. The [freshness deadline](freshness.md) page explains the exception.

The marker serves only the healthcheck. When the service also serves `/healthz`, for example on an opt-in metrics listener, that endpoint reads the Status in memory and never the file.

## Where the marker lives

`MarkerPath(name)` returns `/dev/shm/<name>.healthy`. `/dev/shm` stays a writable memory filesystem when the root filesystem is read-only, under Docker's `read_only: true`, Kubernetes's `readOnlyRootFilesystem: true` and Podman's `--read-only`, so no extra mount is needed.

Pass the service's own name, the same constant on both sides. `MarkerPath` panics on an empty name, a name that starts with a dot, or one that holds a slash or a NUL byte, so the first test run catches a bad constant. Give each Status its own path.

## When `/dev/shm` is shared

`/dev/shm` is private to a container under Docker's default IPC mode. It is shared by every container in a Kubernetes pod, with the host and every other such container under `ipc: host`, and with any container that joins another's IPC namespace (`ipc: container:<name>`). Two copies of the same service sharing it would read each other's marker.

Any process there can also put something at the marker path first, as another user. If the service cannot open what it finds, or it is not a regular file the service can refresh (a directory, a FIFO, or a symlink, which the marker never follows, so nothing the link names is written), that is degraded mode: `RunProbe` reports healthy however old the file gets, with or without a lease, for as long as it stays. If the service can open the file but not remove it, which is the case for a world-writable file in a sticky folder such as `/dev/shm`, every unhealthy write logs a warning, and `RunProbe` reports healthy until an armed lease expires after refreshing stops, or with no lease for as long as the file stays.

So a neighbour that plants a symlink or a file pins the probe healthy, and a service that must not trust its IPC namespace keeps its marker on its own tmpfs. Where `/dev/shm` is shared, pass a path on a tmpfs the container owns to `WithMarker` and `RunProbe` instead, such as `/tmp/<name>.healthy` with a tmpfs mounted at `/tmp`:

```yaml
read_only: true
tmpfs:
  - /tmp:size=1m,mode=1777,noexec,nosuid,nodev
```

With `ipc: none` there is no `/dev/shm` at all, which is degraded mode with its warning.

## One writer

Only the long-running process writes health. A subcommand run with `docker exec` reports its result through its exit code, or hands its work to the running process. A second writer would race the verdict the endpoint reports.

Create the Status with its marker only in the long-running process, after the `health` subcommand is dispatched. `NewStatus` removes the marker at its path, so a second process that creates one, even one that never calls `Set`, turns the running service unhealthy.

## Degraded mode

Degraded mode is decided when `NewStatus` runs. It starts when the marker's folder cannot be written, or when the marker path holds something a write cannot refresh. The Status then logs one warning, with `effect` saying what each reader reports and `hint` naming the fix:

| Cause | Message | Attributes |
| --- | --- | --- |
| The folder cannot be written | `health marker directory not writable, marker in degraded mode` | `dir`, `error`, `effect`, `hint` |
| The path holds something a write cannot refresh | `health marker not writable, marker in degraded mode` | `path`, `error`, `effect`, `hint` |

In degraded mode `RunProbe` reports healthy while the marker cannot be written. The service still works, and only its marker is broken, which a restart cannot fix while the obstacle stays. The in-memory verdict, `Fresh` and every HTTP endpoint over the Status keep reporting the verdict your code recorded.

The causes:

- `ipc: none`, which leaves no `/dev/shm`.
- A read-only folder, or a path whose parent is not a folder.
- Something at the path a write cannot refresh: another user's file the service cannot open, a directory or a FIFO.
- A symlink at the path when `NewStatus` runs.

A symlink planted after `NewStatus` runs fails the write, and is not followed where the platform has `O_NOFOLLOW`, as every Unix does. `RunProbe` never follows one, and reads it as degraded mode. Pass the real path, never a link, to both `WithMarker` and `RunProbe`. A process that can put a link or a file at the marker path can keep the probe reporting healthy. Where `/dev/shm` is shared, keep the marker on a tmpfs the container owns, such as `/tmp/<name>.healthy`, and pass that path to both `WithMarker` and `RunProbe`.

A service that keeps calling `Set` needs no restart once the cause is fixed: the first `Set` that can write leaves degraded mode, logs `health marker writable again, leaving degraded mode` with how long it lasted in `duration`, and applies its value. `Set(true)` writes the marker, and `Set(false)` or `BeginDrain` leaves it absent. One that sets its verdict only at startup makes no later `Set`, so once the obstacle is gone its probe reads the path as it stands until the service restarts: absent when nothing is there, and present when a regular file already is. Leaving is one-way: a later write failure is logged and does not re-enter degraded mode.

## Write failures

A write can fail outside degraded mode, for example when the process runs out of file descriptors. The Status logs `failed to create health marker` or `failed to remove health marker` at Warn, with `path`, `error` and `effect`, once for each distinct error in a run of failures. The first write that succeeds after one logs `health marker write recovered` at Info, with `path`, `healthy` and `duration`, how long the failures lasted.

A failed write never changes the verdict: memory is the record, and `/healthz` keeps reporting it. Until a write succeeds, the healthcheck may disagree with it. A failed refresh leaves the file stale or absent while the verdict is healthy, so `RunProbe` can fail while `/healthz` passes. A failed removal leaves the file present while the verdict is unhealthy.
