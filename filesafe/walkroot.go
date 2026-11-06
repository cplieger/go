package filesafe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

// walkReadDirBatch bounds one ReadDir call's memory: fs.WalkDir reads and
// sorts a directory's whole inventory before the first callback, so a large
// or hostile directory is resident before the caller can refuse it. Not
// configurable — a caller that wants to stop early returns fs.SkipAll from fn.
const walkReadDirBatch = 256

// WalkDirInRoot hands every entry of root's tree to fn as fs.WalkDir does:
// root-relative, slash-separated on every OS, the root first as ".", then
// pre-order; an unreadable directory is reported for its own path; fs.SkipDir
// and fs.SkipAll as usual. It never descends a symlinked directory, refuses a
// non-directory swapped in for one, and keeps one directory handle open.
// Unlike fs.WalkDir, a directory's entries all arrive before its subdirectories
// are walked, sorted only within a batch of 256, and the walk stops between
// batches once ctx is done. The caller owns root (docs/confinement.md).
func WalkDirInRoot(ctx context.Context, root *os.Root, fn fs.WalkDirFunc) error {
	if root == nil {
		return errors.New("filesafe: nil root")
	}
	if fn == nil {
		return errors.New("filesafe: nil walk function")
	}
	w := &rootWalk{ctx: ctx, root: root, fn: fn}
	err := w.run()
	if errors.Is(err, fs.SkipDir) || errors.Is(err, fs.SkipAll) {
		return nil
	}
	return err
}

// rootWalk carries one WalkDirInRoot traversal.
type rootWalk struct {
	ctx     context.Context
	root    *os.Root
	fn      fs.WalkDirFunc
	stopped bool
}

// walkAction is what one callback answer means for the traversal.
type walkAction int

const (
	walkContinue walkAction = iota
	walkSkipDir
	walkStop
)

// classifyWalkAnswer splits a callback's return into a traversal action and
// a real error. Only the second is ever propagated to WalkDirInRoot's caller.
func classifyWalkAnswer(err error) (walkAction, error) {
	switch {
	case err == nil:
		return walkContinue, nil
	case errors.Is(err, fs.SkipAll):
		return walkStop, nil
	case errors.Is(err, fs.SkipDir):
		return walkSkipDir, nil
	default:
		return walkContinue, err
	}
}

func cmpEntryName(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) }

// run visits the root and then streams every directory reachable from it,
// keeping the pending set as a stack of NAMES so no ancestor handle stays
// open during the descent.
func (w *rootWalk) run() error {
	if err := w.ctx.Err(); err != nil {
		return fmt.Errorf("filesafe: %w", err)
	}
	fi, err := w.root.Stat(".")
	if err != nil {
		return w.fn(".", nil, err)
	}
	act, cbErr := classifyWalkAnswer(w.fn(".", fs.FileInfoToDirEntry(fi), nil))
	if cbErr != nil {
		return cbErr
	}
	if act != walkContinue {
		return nil
	}

	pending := []string{"."}
	for len(pending) > 0 {
		dir := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		subdirs, streamErr := w.streamDir(dir)
		if streamErr != nil {
			return streamErr
		}
		if w.stopped {
			return nil
		}
		// Reversed, so the stack pops subdirectories in the order they were read.
		slices.Reverse(subdirs)
		pending = append(pending, subdirs...)
	}
	return nil
}

// streamDir visits one directory's entries in fixed-size batches and reports
// the subdirectories found under it. The handle is closed before any of
// them is opened.
func (w *rootWalk) streamDir(dir string) ([]string, error) {
	handle, err := openDirInRoot(w.root, dir)
	if err != nil {
		return nil, w.reportDirFailure(dir, err)
	}
	defer func() { _ = handle.Close() }()

	var subdirs []string
	for {
		if ctxErr := w.ctx.Err(); ctxErr != nil {
			return subdirs, fmt.Errorf("filesafe: %w", ctxErr)
		}
		entries, readErr := handle.ReadDir(walkReadDirBatch)
		slices.SortFunc(entries, cmpEntryName)
		batch, endDir, visitErr := w.visitBatch(dir, entries)
		subdirs = append(subdirs, batch...)
		if visitErr != nil {
			return nil, visitErr
		}
		if endDir {
			return subdirs, nil
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return subdirs, nil
			}
			return subdirs, w.reportDirFailure(dir, readErr)
		}
	}
}

// visitBatch hands one batch of directory entries to the callback,
// root-relative, and reports the subdirectories among them. endDir is true
// when nothing further in this directory is to be read: fs.SkipAll,
// fs.SkipDir on a non-directory entry, or a callback error.
func (w *rootWalk) visitBatch(dir string, entries []fs.DirEntry) (subdirs []string, endDir bool, err error) {
	for _, entry := range entries {
		rel := entry.Name()
		if dir != "." {
			rel = path.Join(dir, entry.Name())
		}
		act, cbErr := classifyWalkAnswer(w.fn(rel, entry, nil))
		switch {
		case cbErr != nil:
			return subdirs, true, cbErr
		case act == walkStop:
			w.stopped = true
			return subdirs, true, nil
		case act == walkSkipDir && !entry.IsDir():
			return subdirs, true, nil
		case act == walkSkipDir:
			continue
		case entry.IsDir():
			// Queued on the DIRENT type, so a symlinked directory is never descended.
			subdirs = append(subdirs, rel)
		}
	}
	return subdirs, false, nil
}

// reportDirFailure hands a directory-level failure to the callback for the
// path it belongs to and translates the answer: nil (or fs.SkipDir) leaves
// the walk running, fs.SkipAll ends it, anything else aborts it.
func (w *rootWalk) reportDirFailure(dir string, err error) error {
	act, cbErr := classifyWalkAnswer(w.fn(dir, nil, err))
	if cbErr != nil {
		return cbErr
	}
	if act == walkStop {
		w.stopped = true
	}
	return nil
}
