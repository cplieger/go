package trigger_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cplieger/go/scheduler/trigger"
)

// payload is an app's request type: the arguments one triggered run carries.
// An argless daemon uses struct{} instead.
type payload struct {
	Repos []string `json:"repos,omitempty"`
}

// Example wires the whole broker: a daemon-side queue served by one executor
// goroutine and exposed on a unix socket, and a client that submits one run
// and waits for its result — the single-owner scheduler shape.
func Example() {
	dir, err := os.MkdirTemp("/tmp", "trigger-example-")
	if err != nil {
		fmt.Println("tempdir:", err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socketPath := filepath.Join(dir, "trigger.sock")

	// Daemon side: one bounded queue, one executor goroutine (the single
	// owner of execution), one socket server bridging requests in.
	queue := trigger.NewQueue[payload](16)
	executorDone := make(chan struct{})
	go func() {
		defer close(executorDone)
		for job := range queue.Jobs() {
			job.Start()
			// The app's real work runs here, with the job's exact payload.
			job.Finish(trigger.Result{OK: true, Duration: 42 * time.Millisecond})
		}
	}()

	ln, err := trigger.Listen(socketPath)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	ctx, stop := context.WithCancel(context.Background()) // in an app, the signal context
	defer stop()
	srv := &trigger.Server[payload]{Queue: queue}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, ln) }()

	// Client side (the `run` subcommand): submit one request, block until
	// its own result, map it to an exit code.
	final, err := trigger.Submit(context.Background(), socketPath, payload{Repos: []string{"owner/repo"}}, nil)
	if err != nil {
		fmt.Println("submit:", err)
		return
	}
	fmt.Println("ok:", final.OK)

	// Daemon shutdown: ending ctx stops admission and closes the socket,
	// closing the queue drains it, and Serve returns once every handler has
	// written its final event.
	stop()
	queue.Close()
	<-executorDone
	<-served

	// Output:
	// ok: true
}
