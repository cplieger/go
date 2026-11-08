package filesafe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ProbePhase identifies which step of a writability probe failed, in the
// order a real write walks: a directory can accept the entry yet reject the
// first write, fail only at Sync or Close, or accept everything and deny the
// unlink. It is the probe's counterpart to WritePhase. The probe reports the
// phase and leaves the pass/fail policy to the caller; ProbeResult.Writable
// names the phases that mean only teardown failed.
type ProbePhase int

const (
	// ProbePhaseNone is the zero value: every phase passed.
	ProbePhaseNone ProbePhase = iota
	// ProbePhaseMkdir indicates the directory could not be created, or that
	// under WithMkdirMode a created level did not store the mode (Err matches
	// ErrModeNotStored). Only reachable with WithMkdirMode or WithMkdirAll;
	// without either a missing directory fails at ProbePhaseCreate, exactly
	// as a write to a missing parent does.
	ProbePhaseMkdir
	// ProbePhaseCreate indicates the probe file could not be created: the
	// directory is missing, is not a directory, is read-only, or denies the
	// running UID. It also covers a name that escapes the *os.Root in
	// ProbeWritableInRoot.
	ProbePhaseCreate
	// ProbePhaseWrite indicates the first data write to the probe file failed
	// (a quota or a full filesystem, which the create alone does not reveal).
	ProbePhaseWrite
	// ProbePhaseSync indicates flushing the probe file failed, the delayed
	// error a network filesystem reports at fsync rather than at write.
	ProbePhaseSync
	// ProbePhaseClose indicates closing the probe file failed, the other
	// place a deferred write error surfaces.
	ProbePhaseClose
	// ProbePhaseRemove indicates the probe file could not be unlinked, so the
	// directory accepts writes but refuses cleanup. ProbeResult.Leaked is then
	// true and the leftover is reclaimable by the package's stale-temp sweep.
	ProbePhaseRemove
)

// String names the phase in the words a log line uses.
func (s ProbePhase) String() string {
	switch s {
	case ProbePhaseNone:
		return "no failure"
	case ProbePhaseMkdir:
		return "create directory"
	case ProbePhaseCreate:
		return "create probe file"
	case ProbePhaseWrite:
		return "write probe file"
	case ProbePhaseSync:
		return "sync probe file"
	case ProbePhaseClose:
		return "close probe file"
	case ProbePhaseRemove:
		return "remove probe file"
	default:
		return "unknown phase"
	}
}

// ProbeResult reports the outcome of one directory writability probe.
//
// It is an outcome record, not an error: a phase failure arrives here, never
// as the probe function's error return, so the caller decides which phases
// are fatal. A caller that wants pass/fail asks OK once.
//
// It is only meaningful alongside a nil error: a non-nil error means no
// probe ran, and OK and Writable on the zero ProbeResult returned with it
// are not a verdict.
type ProbeResult struct {
	// Err is the failure from Phase, unwrapped to the underlying filesystem
	// error so errors.Is against fs.ErrPermission, fs.ErrNotExist or
	// syscall.ENOSPC works. It is nil exactly when OK reports true.
	Err error
	// Dir is the directory that was probed: the cleaned argument for
	// ProbeWritable, or root.Name() joined with the cleaned relative name for
	// ProbeWritableInRoot (the Result.Path convention).
	Dir string
	// Name is the probe file's base name once it was created, and "" when the
	// probe never got that far. It always satisfies IsPackageTemp, so a
	// leftover is reclaimable by CleanupStaleTemps.
	Name string
	// Phase is the first phase that failed, or ProbePhaseNone when all passed.
	Phase ProbePhase
	// Leaked reports whether the probe file is still on disk. It can be true
	// only alongside ProbePhaseRemove, or alongside ProbePhaseWrite,
	// ProbePhaseSync or ProbePhaseClose when the follow-up unlink also failed
	// (that secondary error is logged at Debug, since Err holds the first and
	// more informative failure).
	Leaked bool
}

// OK reports whether every phase passed.
func (r ProbeResult) OK() bool { return r.Phase == ProbePhaseNone }

// Writable reports whether the directory accepted a real write — created,
// written and flushed — so at worst only teardown (close, unlink) failed. It
// names those phases, so a phase added later is not writable unless it says
// so here.
func (r ProbeResult) Writable() bool {
	switch r.Phase {
	case ProbePhaseNone, ProbePhaseClose, ProbePhaseRemove:
		return true
	default:
		return false
	}
}

// ProbeWritable proves dir is writable by doing what an atomic write does
// (create a TempName file, write and flush a byte, close, unlink) and reports
// the first failed phase in the ProbeResult; a non-nil error means the probe
// could not start (empty dir, done ctx, a refused mode option). dir may be
// relative; a missing dir fails at ProbePhaseCreate unless WithMkdirMode or
// WithMkdirAll creates it, through the same chain a write uses. WithMode
// makes it prove an owner-only staging file; on Windows either mode option
// with a nonzero mode returns ErrModeUnsupported. ctx is checked once, a
// started probe always cleans up, and CleanupStaleTemps reclaims a leftover.
func ProbeWritable(ctx context.Context, dir string, opts ...WriteOption) (ProbeResult, error) {
	c := buildWriteCfg(opts)
	if dir == "" {
		return ProbeResult{}, ErrEmptyPath
	}
	if modeErr := c.writeOptionsErr(); modeErr != nil {
		return ProbeResult{}, modeErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ProbeResult{}, fmt.Errorf("filesafe: %w", ctxErr)
	}
	clean := filepath.Clean(dir)
	if mode, enforce, create := c.parentDirs(); create {
		if _, mkErr := mkdirAllAbs(clean, mode, enforce, c.logger); mkErr != nil {
			return ProbeResult{Dir: clean, Phase: ProbePhaseMkdir, Err: mkErr}, nil
		}
	}
	root, err := os.OpenRoot(dirOpenName(clean))
	if err != nil {
		return ProbeResult{Dir: clean, Phase: ProbePhaseCreate, Err: dirOpenErr(err, clean)}, nil
	}
	defer root.Close()
	res := probeInRoot(root, ".", c)
	res.Dir = clean // report the caller's own directory, not root.Name()+"/.".
	return res, nil
}

// ProbeWritableInRoot is ProbeWritable confined to root's tree: name is
// relative to root ("." for root itself) and every operation runs through
// the *os.Root, so an escaping name is refused and reported at
// ProbePhaseCreate (ProbePhaseMkdir under WithMkdirMode or WithMkdirAll).
// Everything else, the WithMode rule included, is ProbeWritable's. The
// caller owns root and keeps it open; a nil root returns ErrUnsafePath.
func ProbeWritableInRoot(ctx context.Context, root *os.Root, name string, opts ...WriteOption) (ProbeResult, error) {
	c := buildWriteCfg(opts)
	if root == nil {
		return ProbeResult{}, fmt.Errorf("%w: nil root", ErrUnsafePath)
	}
	clean, err := validateRootName(name)
	if err != nil {
		return ProbeResult{}, err
	}
	if modeErr := c.writeOptionsErr(); modeErr != nil {
		return ProbeResult{}, modeErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ProbeResult{}, fmt.Errorf("filesafe: %w", ctxErr)
	}
	return probeInRoot(root, clean, c), nil
}

// probeInRoot is the probe engine both entry points run: optionally create
// the directory, create a package-shaped temp inside it, write and flush a
// byte, then always close and always unlink. dir is relative to root.
//
// Once the temp exists teardown runs unconditionally, so an earlier phase
// failure never leaks a file the directory would happily remove. Only the
// first failure is reported; a secondary teardown failure goes to Debug.
func probeInRoot(root *os.Root, dir string, c *cfg) ProbeResult {
	if mode, enforce, create := c.parentDirs(); create {
		if _, mkErr := mkdirAllInRoot(root, dir, mode, enforce, false, c.logger); mkErr != nil {
			return ProbeResult{Dir: filepath.Join(root.Name(), dir), Phase: ProbePhaseMkdir, Err: mkErr}
		}
	}
	f, tmpName, err := createTempInRoot(root, dir, c)
	if err != nil {
		return ProbeResult{Dir: filepath.Join(root.Name(), dir), Phase: ProbePhaseCreate, Err: probeCause(err)}
	}
	res := ProbeResult{
		Dir:    filepath.Join(root.Name(), dir),
		Name:   filepath.Base(tmpName),
		Leaked: true,
	}
	if phase, sErr := probeData(f); sErr != nil {
		res.Phase, res.Err = phase, sErr
	}
	res.tearDown(root, f, tmpName, c)
	return res
}

// probeData runs the phases that need the probe file open: the first data
// write and the flush that surfaces a deferred filesystem error. One byte is
// enough to force an allocation.
func probeData(f *os.File) (ProbePhase, error) {
	if _, err := f.Write([]byte{0}); err != nil {
		return ProbePhaseWrite, err
	}
	if err := f.Sync(); err != nil {
		return ProbePhaseSync, err
	}
	return ProbePhaseNone, nil
}

// tearDown closes and unlinks the probe file: records a close or remove
// failure only when no earlier phase failed, clears Leaked once the file is
// gone, and logs any teardown failure it isn't reporting at Debug. A remove
// of an already-gone file is success.
func (r *ProbeResult) tearDown(root *os.Root, f *os.File, tmpName string, c *cfg) {
	if clErr := f.Close(); clErr != nil {
		r.recordTeardown(ProbePhaseClose, clErr, c)
	}
	rmErr := root.Remove(tmpName)
	if rmErr == nil || errors.Is(rmErr, fs.ErrNotExist) {
		r.Leaked = false
		return
	}
	r.recordTeardown(ProbePhaseRemove, rmErr, c)
}

// recordTeardown adopts a teardown failure as the reported outcome only when
// nothing has failed yet; a later one goes to Debug.
func (r *ProbeResult) recordTeardown(phase ProbePhase, err error, c *cfg) {
	if r.OK() {
		r.Phase, r.Err = phase, err
		return
	}
	c.logger.Debug("filesafe: writability probe teardown failed",
		"dir", r.Dir, "name", r.Name, "phase", phase.String(), "error", err)
}

// probeCause unwraps the *WriteError createTempInRoot returns so a probe
// reports the filesystem error itself rather than the write-path wrapper;
// ProbeResult.Phase already carries the phase.
func probeCause(err error) error {
	if we, ok := errors.AsType[*WriteError](err); ok {
		return we.Err
	}
	return err
}
