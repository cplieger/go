# ownersock

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/ownersock.svg)](https://pkg.go.dev/github.com/cplieger/go/ownersock) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=ownersock%2Fgo.mod)](https://github.com/cplieger/go/blob/main/ownersock/go.mod)

ownersock gives your Go app an admin socket that only processes running as the app's own user can reach: HTTP over a unix socket, checked by the kernel's peer credentials on every connection.

It replaces the unix-socket listener, stale-socket cleanup and permission checks you would otherwise write for a local admin command such as `docker exec app app reset-password`. It runs on Linux, containers included. It uses the standard library plus this repository's `filesafe` and `textsafe`, and is licensed under Apache-2.0.

## Why use it

ownersock is built for Go servers that take admin commands from a command-line tool on the same host or in the same container.

- Every accepted connection's peer uid, read with `SO_PEERCRED`, must equal the server's effective uid. A connection from another user is closed inside `Accept` before any byte is read or written, so no request from it reaches your HTTP handlers.
- A second start beside a live server at the same path is refused, a socket left behind by a crashed process is replaced, and a file at the path that is not a socket is refused and left alone. Starts racing on one path bind one at a time, so one of them wins.
- The socket's directory must be private: `Listen` creates a missing one at mode 0700, refuses an existing one that grants group or other access, is owned by another user or is a symlink, and never changes its mode. The socket is bound at mode 0600.
- A path longer than the 107 bytes Linux's `sun_path` holds is refused before the directory is touched, with an error naming the path and the limit, instead of failing at bind with a bare `invalid argument`.
- `Client` reports a refused connection, a missing server and a permission error as three stable codes, each with a hint your command-line tool can print as it is.
- `Peer` marks a request that arrived on a peer-checked connection. No header or cookie can set the mark, so a handler can tell a socket request from a TCP one.

Every operating system other than Linux refuses every connection, because the peer check reads Linux's `SO_PEERCRED`. On Windows `Listen` fails, because the private directory check is not available there.

Consider a plain [`net.Listen("unix", path)`](https://pkg.go.dev/net#Listen) in a directory only your user can open if the directory's permissions are the only boundary you need.

## Install

```sh
go get github.com/cplieger/go/ownersock@latest
```

## Usage

The server binds the socket and serves an HTTP handler on it, here through this repository's `httpserver`, which sets the timeouts and recovers a handler's panic as a response:

```go
ln, err := ownersock.Listen(ownersock.Config{Path: "/run/myapp-admin/admin.sock", Logger: logger})
if err != nil {
    return err
}
srv := httpserver.New(httpserver.Chain(adminMux, httpserver.Recoverer()))
srv.ConnContext = ownersock.ConnContext
return httpserver.Run(ctx, srv, ln)
```

Give the socket a directory of its own, such as `/data/admin/admin.sock` or `/run/myapp-admin/admin.sock`, never the app's data directory or a shared folder such as `/tmp` itself. `Listen` creates a missing directory at mode 0700; its parent must already exist and be writable by the server's user. Only root can create a directory in `/run`, so a server that runs as any other user either keeps its socket under a directory it owns, such as `/data/admin/admin.sock`, or has its image create the socket's directory at build time, owned by the server's user at mode 0700. An existing directory that grants group or other access is refused with `filesafe.ErrModeTooOpen` and left unchanged: `Listen` never runs chmod on it.

A handler checks the mark before it acts:

```go
if _, ok := ownersock.Peer(r.Context()); !ok {
    http.Error(w, "admin socket only", http.StatusForbidden)
    return
}
```

The command-line tool calls the server through `Client` and prints the hint on failure. The host part of the URL is ignored:

```go
resp, err := ownersock.Client("/run/myapp-admin/admin.sock").Get("http://admin/status")
var oerr *ownersock.Error
if errors.As(err, &oerr) {
    fmt.Fprintln(os.Stderr, oerr.Code+": "+oerr.Hint)
    os.Exit(1)
}
```

`Client` reports a connection the server closes before any answer as `admin_peer_refused`, so serve handlers that always answer, a panic included.

Closing the listener removes the socket file.

## API

- `Listen` binds the socket described by a `Config`: the socket path and the `*slog.Logger` that receives the refusal warnings (`slog.Default()` when nil).
- `ConnContext` is the `http.Server.ConnContext` that marks a peer-checked connection, and `Peer` reads the mark and the peer's uid.
- `Client` returns an `*http.Client` that dials the socket. Its errors match `*Error`, whose `Code` is `CodePeerRefused` (`admin_peer_refused`), `CodeUnavailable` (`admin_unavailable`) or `CodeDenied` (`admin_denied`), and whose `Hint` says what to do. The three codes are stable strings, so a command-line tool may map them to its own sentences instead of printing the hint.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/ownersock).

## Contributing

Issues and pull requests are welcome. The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) apply.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
