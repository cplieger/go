# Trigger broker

This page covers the `trigger` package, for a daemon that runs every job itself. It is for developers whose triggers must each get their own run and their own result.

## How it works

`Exclusive` coordinates runs across processes. The `trigger` package is the alternative for a daemon that owns every run. One daemon owns every job execution, and triggers only submit requests. Triggers include the built-in ticker and each subcommand started with `docker exec`.

- One bounded FIFO `trigger.Queue` feeds one executor goroutine, and that single goroutine is what keeps runs from overlapping.
- A `trigger.Server` accepts requests on a unix socket inside the container and streams `queued`, `started` and `done` events back. Your daemon binds the socket with [ownersock](https://github.com/cplieger/go/tree/main/ownersock), so only processes running as its own user can trigger a run.
- `trigger.Submit` is the synchronous client a trigger subcommand wraps.

There is no coalescing. Every accepted request gets its own run, its own arguments and its own result, in arrival order.

The request payload is a type parameter. A daemon whose runs take arguments declares a struct, such as repository names plus a forwarded environment. A daemon without arguments uses `struct{}`, which is sent as `{}` on the wire.

```go
// Daemon side: one queue, one executor goroutine, one socket server.
// trigger.Execute owns the Start/Finish lifecycle, so every job gets exactly
// one result by construction: the callback just returns the outcome.
queue := trigger.NewQueue[payload](16)
executed := make(chan struct{})
go func() {
	defer close(executed)
	trigger.Execute(ctx, queue, func(ctx context.Context, trig string, p payload) trigger.Result {
		ok, elapsed := runPass(ctx, p) // the app's real work
		return trigger.Result{OK: ok, Duration: elapsed}
	})
}()
// ownersock creates /run/myapp at 0700 when this user can write /run, and admits
// only this process's own user.
ln, err := ownersock.Listen(ownersock.Config{Path: "/run/myapp/trigger.sock"})
srv := &trigger.Server[payload]{Queue: queue}
served := make(chan error, 1)
go func() { served <- srv.Serve(ctx, ln) }() // blocks until ctx ends, then closes ln and waits for the handlers
// shutdown: when ctx ends, queue.Close(); <-executed; then err := <-served (nil after ctx ended)

// Trigger subcommand: submit one run, wait for its own result.
// Pass signal.NotifyContext so Ctrl-C unwinds the wait instead of killing
// the process with the connection half-open.
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
final, err := trigger.Submit(ctx, "/run/myapp/trigger.sock", payload{Repos: repos}, nil)
// map final.OK / errors.Is(err, trigger.ErrUnreachable) to the exit code
```

## Guarantees

- The queue rejects a request at once when it is full or closing, with `ErrFull` or `ErrClosed`. Their messages travel the wire as the rejection reason.
- An accepted job gets exactly one result. `Execute` finishes jobs received after shutdown with `CancelledReason` instead of dropping them. It also delivers a panicking run's failure result before the panic continues, so a waiting client is never stranded.
- `Execute` is optional. A daemon whose executor policy differs writes its own loop of about 7 lines. Examples are running jobs outside the shutdown context, halting admission on an app state, or using its own cancellation wording.
- The server never logs payload contents, because a forwarded environment can carry secrets. The `OnAccepted` and `OnRejected` hooks let your app log acceptance and rejection in its own words.
- What a job does, how its result maps to health and the wording of lifecycle log lines stay in your app.

## The socket

Bind the socket with [ownersock](https://github.com/cplieger/go/tree/main/ownersock) and pass the listener to `Server.Serve`. `Serve` trusts every connection the listener accepts, so the listener decides who may trigger a run. `ownersock.Listen` checks each connection's peer credentials and closes one from any other user before reading it. It also refuses a second start beside a live daemon and replaces a socket a killed daemon left behind.

Give the socket a private directory of its own, such as `/run/myapp` for `/run/myapp/trigger.sock`. A daemon that does not run as root needs a parent directory it can write, or its image creates the socket directory at build time, owned by the daemon's user at mode 0700. ownersock refuses a path that breaks these rules:

- The path is absolute and clean, and at most 107 bytes, the length a Linux unix socket path holds.
- Its directory is owned by the server's user and grants no group or other access. `ownersock.Listen` creates a missing directory at mode 0700. It refuses an existing directory that grants group or other access, is owned by another user or is a symlink, and never changes its mode.
- The directory is never `/tmp` itself, which every user can open.

ownersock runs on Linux. A trigger subcommand must run as the daemon's user: `docker exec` runs as the container's user (the image's `USER`, or the `--user` the container was started with) unless you pass `--user`.

A request line is capped at 8 MiB, and a client has 30 seconds after connecting to send it. A connection that closes without sending a request gets no event and is logged at DEBUG.

## Client errors

`Submit` blocks for the whole queue wait plus the run, and has no read deadline. Cancel its context to stop waiting. A non-nil error wraps one of these values, or is the context's own error:

- `ErrUnreachable` means no daemon accepted within 5 seconds. The container may be down, or the exec user cannot open the socket's private directory.
- `ErrSend` means the request could not be written.
- `ErrConnectionLost` means the event stream ended before the final `done` event, because the daemon died or was stopped mid-run.

A client running as a user other than the daemon's, root included, is refused: run `docker exec` (or the client) as the daemon's user. Root can open the socket's private directory, so its connection is closed before the daemon reads it, and `Submit` reports `ErrSend` or `ErrConnectionLost`.
