package health

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// MarkerPath returns the marker path for the service app: /dev/shm/<app>.healthy.
// /dev/shm stays writable when a container's root filesystem is read-only, so
// no extra mount is needed. MarkerPath panics if app is empty, starts with a
// dot, or holds a slash or a NUL byte.
func MarkerPath(app string) string {
	if app == "" || app[0] == '.' || strings.ContainsAny(app, "/\x00") {
		panic(fmt.Sprintf("health: MarkerPath(%q): want a name that is not empty, does not start with a dot and holds no slash or NUL byte", app))
	}
	return "/dev/shm/" + app + ".healthy"
}

const (
	degradedEffect = "RunProbe reports healthy while the marker cannot be written; " +
		"the in-memory verdict and every HTTP endpoint over it report the real verdict"
	dirHint = "create the marker's folder or make it writable, for example with a tmpfs mount; " +
		"/dev/shm is one in a container unless IPC is disabled. " +
		"The next Set applies its value, so restart a service that sets it only at startup"
	markerHint = "remove what is at the marker path, such as a file another user created, " +
		"a symlink, a directory or a FIFO; " +
		"the next Set applies its value, so restart a service that sets it only at startup"
	writeFailEffect = "the health subcommand may disagree with the in-memory verdict until a marker write succeeds"
)

// marker mirrors a Status's verdict to a file. Only the Status's writers use
// it, under the Status's mu, so it has no lock of its own.
type marker struct {
	degradedSince  time.Time
	failSince      time.Time
	log            func() *slog.Logger
	path           string
	loggedFailSigs []string
	failed         bool
	degraded       bool
}

// open checks that the marker can be written, entering degraded mode when it
// cannot, and removes a marker an earlier run left: in a folder that outlives
// the process it would report the service healthy before any Set.
func (m *marker) open() {
	if err := probeHealthDir(m.path); err != nil {
		m.degrade()
		m.log().Warn("health marker directory not writable, marker in degraded mode",
			"dir", filepath.Dir(m.path), "error", err, "effect", degradedEffect, "hint", dirHint)
		return
	}
	if err := markerWritable(m.path); err != nil {
		m.degrade()
		m.log().Warn("health marker not writable, marker in degraded mode",
			"path", m.path, "error", err, "effect", degradedEffect, "hint", markerHint)
		return
	}
	if err := removeMarker(m.path); err != nil {
		m.fail("failed to remove health marker", err)
	}
}

func (m *marker) degrade() {
	m.degraded = true
	m.degradedSince = now()
}

// write creates or refreshes the marker for ok and removes it otherwise. While
// degraded it first checks again whether the marker can be written, because
// RunProbe decides degraded mode from the filesystem on every run: until it
// can, write does nothing, and leaving is one-way.
func (m *marker) write(ok bool) {
	if m.degraded {
		if cannotSignal(m.path) != nil {
			return
		}
		m.degraded = false
		m.log().Info("health marker writable again, leaving degraded mode",
			"path", m.path, "duration", since(m.degradedSince))
	}
	if msg, err := applyMarker(m.path, ok); err != nil {
		m.fail(msg, err)
		return
	}
	if m.failed {
		m.log().Info("health marker write recovered",
			"path", m.path, "healthy", ok, "duration", since(m.failSince))
		m.failed = false
	}
}

// applyMarker writes ok to the marker at path, returning the warning to log
// with the error when the write fails.
func applyMarker(path string, ok bool) (string, error) {
	if ok {
		return "failed to create health marker", writeMarker(path)
	}
	return "failed to remove health marker", removeMarker(path)
}

// fail logs a write failure once per distinct (message, error) pair per
// streak, so a lasting failure stays one line while a new one still shows.
func (m *marker) fail(msg string, err error) {
	if !m.failed {
		m.failed = true
		m.failSince = now()
		m.loggedFailSigs = m.loggedFailSigs[:0]
	}
	sig := msg + "\x00" + err.Error()
	if slices.Contains(m.loggedFailSigs, sig) {
		return
	}
	m.loggedFailSigs = append(m.loggedFailSigs, sig)
	m.log().Warn(msg, "path", m.path, "error", err, "effect", writeFailEffect)
}

// errSymlink is the error for a symlink at the marker path.
var errSymlink = errors.New("is a symlink, which the marker never follows")

// markerWritable returns why a write could not refresh the marker at path, or
// nil when it can. It opens the marker as writeMarker does, without O_TRUNC,
// so the marker's content and time are unchanged. A path os.Lstat cannot read
// is nil here: inspect classifies it from the stat error, so the Status and
// the probe agree on degraded mode.
func markerWritable(path string) error {
	info, err := os.Lstat(path) // #nosec G703 -- trusted caller-supplied marker path, existence check only
	if err != nil {
		return nil
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return &fs.PathError{Op: "open", Path: path, Err: errSymlink}
	}
	if err := openMarker(path, 0); !errors.Is(err, fs.ErrNotExist) && !transient(err) {
		return err
	}
	return nil
}

// transient reports an open error that can clear on its own, which a write
// retries rather than degraded mode giving up on: EAGAIN from a lease another
// process holds (a non-blocking open does not wait for it to break), EINTR,
// and running out of file descriptors.
func transient(err error) bool {
	var t interface{ Temporary() bool }
	return errors.As(err, &t) && t.Temporary()
}

// cannotSignal returns why a write cannot change the marker at path (its
// folder or the marker itself cannot be written), or nil.
func cannotSignal(path string) error {
	if err := probeHealthDir(path); err != nil {
		return err
	}
	return markerWritable(path)
}

// removeMarker removes the marker, or a symlink at its path but never what the
// link names. An absent marker is not an error.
func removeMarker(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeMarker refreshes the marker at path, or creates it. An existing
// marker opens without O_CREATE, so one another user left in a sticky folder
// opens on its mode alone (fs.protected_regular refuses only O_CREAT opens of
// such a file), and with O_TRUNC, which refreshes its modification time, the
// age WithMaxAge reads.
func writeMarker(path string) error {
	err := openMarker(path, os.O_TRUNC)
	if errors.Is(err, fs.ErrNotExist) {
		err = openMarker(path, os.O_CREATE|os.O_TRUNC)
	}
	return err
}

// openMarker opens the marker write-only with flag added, and never blocks:
// a FIFO with no reader fails with ENXIO instead of hanging. It never follows
// a symlink where the platform has O_NOFOLLOW, refuses anything but a regular
// file, whose time an open with O_TRUNC refreshes, and creates one with
// os.Create's mode when flag holds O_CREATE.
func openMarker(path string, flag int) error {
	f, err := os.OpenFile(path, os.O_WRONLY|oNonblock|oNoFollow|flag, 0o666) // #nosec G302 G304 G703 -- trusted caller-supplied marker path, os.Create's mode
	if err != nil {
		return err
	}
	info, statErr := f.Stat()
	closeErr := f.Close()
	switch {
	case statErr != nil:
		return statErr
	case !info.Mode().IsRegular():
		return &fs.PathError{Op: "open", Path: path, Err: errNotRegular}
	case closeErr != nil:
		return fmt.Errorf("close: %w", closeErr)
	}
	return nil
}

// errNotRegular is openMarker's error for a marker path that holds something
// other than a regular file, such as a FIFO a reader holds open.
var errNotRegular = errors.New("not a regular file")

// probeHealthDir verifies the marker's folder is writable by creating and
// deleting a temp file in it.
func probeHealthDir(path string) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".health-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	removed := false
	defer func() {
		if !removed {
			_ = os.Remove(name) // #nosec G703 -- name generated by os.CreateTemp above, not external input
		}
	}()

	if closeErr := f.Close(); closeErr != nil {
		return fmt.Errorf("close probe: %w", closeErr)
	}
	if rmErr := os.Remove(name); rmErr != nil { // #nosec G703 -- name generated by os.CreateTemp above, not external input
		return fmt.Errorf("remove probe: %w", rmErr)
	}
	removed = true
	return nil
}
