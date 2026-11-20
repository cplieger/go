// Package ownersock is the listener and client of an admin socket: HTTP over
// a unix socket that only processes of the server's own user can reach.
//
// [Listen] binds the socket at mode 0600 in a private directory, checked
// with filesafe.EnsurePrivateDir: a missing directory is created at 0700,
// and an existing one with group or other access is refused with
// filesafe.ErrModeTooOpen and left unchanged, never chmodded. Give the
// socket a directory of its own (/data/admin/admin.sock,
// /run/myapp-admin/admin.sock), never the app's data directory or a shared
// one such as /tmp. A missing directory's parent must be writable by the
// server's user; only root can write /run, so a server running as another
// user has its image create that directory. A live socket at the path
// refuses start, a socket nothing answers is replaced, and any other file
// at the path is refused and left alone. Starts on one path take turns
// through a filesafe lease on the lock file <path>.lock, which Listen
// creates at 0600 beside the socket and never removes. A path longer than
// the 107 bytes Linux's sun_path holds (unix(7)) is refused before the
// directory is touched.
//
// Every connection the listener accepts is peer-checked: on Linux the peer's
// uid, read with SO_PEERCRED, must equal the server's effective uid, and on
// every other OS every connection is refused. A refused connection is closed
// inside Accept before any byte is read or written, and Accept goes on to the
// next one, so no request from another user reaches an HTTP handler. The
// listener logs one WARN per refused uid a minute. Listen itself fails on
// Windows, which lacks the private directory check, and on AIX and Solaris,
// which lack the lease.
//
// Set [ConnContext] as the http.Server's ConnContext, and [Peer] reports
// whether a request arrived on a connection this package's listener accepted
// and peer-checked. No header or cookie can set that mark.
//
// [Client] returns an *http.Client that dials the socket. Its dial and
// refusal errors are [*Error] values carrying one of [CodePeerRefused],
// [CodeUnavailable] or [CodeDenied] and a hint a command-line tool can print.
// The codes are stable strings a command-line tool may map to its own
// sentences.
package ownersock
