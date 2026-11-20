// Package trigger is a single-owner trigger broker for socket-shaped scheduler
// daemons. One daemon process owns every job execution, and triggers, such as a
// built-in ticker or a client exec, only submit requests.
//
// A bounded FIFO [Queue] feeds the daemon's single executor goroutine, which is
// the mutual exclusion. A [Server] accepts requests on a unix socket bound with
// github.com/cplieger/go/ownersock, which admits only the server's own user,
// and streams lifecycle events back. [Submit] forwards one request and blocks
// until that run's own result. There is no coalescing: every accepted request
// gets its own run and its own result, in arrival order.
//
// The request payload is a type parameter; an argless daemon uses struct{},
// framed as {}. Client and daemon ship in one binary, so the newline-delimited
// JSON wire format (see [Event]) carries no version field. What a job does,
// when shutdown cancels or drains, and the app's log wording stay in the
// consuming app, whose payload the library never logs. Unix-only.
package trigger
