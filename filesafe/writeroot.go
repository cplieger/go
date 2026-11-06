package filesafe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// validateRootName checks that name is a non-empty, relative, null-byte-free
// path for use as a key into an *os.Root, returning the cleaned form. It
// allows "..": the root itself refuses a name that escapes its tree. A name
// cleaning to "." is a directory ProbeWritableInRoot takes; an operation
// that needs an entry uses validateRootEntry.
func validateRootName(name string) (string, error) {
	if name == "" {
		return "", ErrEmptyPath
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: contains null byte", ErrUnsafePath)
	}
	// A Windows rooted (\x) or drive-relative (C:x) name is not IsAbs, yet
	// it is not relative to the root either.
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" || os.IsPathSeparator(name[0]) {
		return "", fmt.Errorf("%w: not relative: %q", ErrUnsafePath, name)
	}
	return filepath.Clean(name), nil
}

// validateRootEntry is validateRootName for an operation that must name an
// ENTRY inside root rather than a directory: it additionally refuses a name
// whose final element is ".", ".." or the separator, none of which an
// operation can create, rename onto or unlink.
func validateRootEntry(name string) (string, error) {
	clean, err := validateRootName(name)
	if err != nil {
		return "", err
	}
	switch filepath.Base(clean) {
	case ".", "..", string(filepath.Separator):
		return "", fmt.Errorf("%w: names no entry: %q", ErrUnsafePath, name)
	}
	return clean, nil
}

// randomTempName returns a temp base name of the exact shape CleanupStaleTemps
// recognises (".atomicfile-<digits>.tmp"), drawing the digit run from
// crypto/rand.
//
// It cannot fail: crypto/rand.Read is documented never to return an error.
func randomTempName() string {
	var b [8]byte
	rand.Read(b[:])
	return tempPrefix + strconv.FormatUint(binary.LittleEndian.Uint64(b[:]), 10) + tempSuffix
}

// createTempInRoot creates an exclusive temp in dir (relative to root),
// retrying a name collision as os.CreateTemp does; a failure, an escaping dir
// included, is a PhaseTempCreate WriteError. Under WithMode the temp is
// created 0o600 and proved owner-only before any data goes into it; without
// it the temp is created 0o666 like os.Create and its mode is never checked.
func createTempInRoot(root *os.Root, dir string, c *cfg) (*os.File, string, error) {
	perm := os.FileMode(0o666)
	if c.modeSet {
		perm = 0o600
	}
	for try := 0; ; try++ {
		name := filepath.Join(dir, randomTempName())
		f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			if !c.modeSet {
				return f, name, nil
			}
			if _, mErr := enforceMode(f, 0o600); mErr != nil {
				f.Close()
				_ = root.Remove(name)
				return nil, "", &WriteError{Phase: PhaseTempCreate, Err: mErr}
			}
			return f, name, nil
		}
		if errors.Is(err, fs.ErrExist) && try < 10000 {
			continue
		}
		return nil, "", &WriteError{Phase: PhaseTempCreate, Err: err}
	}
}

// checkWriteTargetInRoot refuses a target an atomic write must not replace:
// a symlink (ErrSymlinkTarget, the rename would replace the link), anything
// else not regular (ErrNotRegular, since rename(2) replaces a FIFO, socket or
// device node), and a target refuseReadOnlyTarget refuses. A missing target
// is fine. It is a check-then-act: rename(2) has no "only if regular" mode.
func checkWriteTargetInRoot(root *os.Root, name string) error {
	fi, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("filesafe: stat target %q: %w", name, err)
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%w: %s", ErrSymlinkTarget, name)
	case !fi.Mode().IsRegular():
		return notRegular(name, fi.Mode())
	}
	return refuseReadOnlyTarget(name, fi.Mode())
}

// fsyncRootDir fsyncs a directory inside root so a prior rename survives a
// crash. It is a package var so tests can inject a failure.
var fsyncRootDir = func(root *os.Root, dir string) error {
	d, err := openDirInRoot(root, dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// mkdirAllInRoot creates dir and every missing ancestor inside root at mode,
// enforcing the mode on each directory it creates and fsyncing that
// directory's parent. os.Root.MkdirAll would not do: an entry never fsynced
// into its parent can vanish in a crash with every descendant. Pre-existing
// directories are left as found. durable is false when a created
// directory's parent could not be fsynced.
func mkdirAllInRoot(root *os.Root, dir string, mode os.FileMode, logger *slog.Logger) (durable bool, err error) {
	if dir == "." {
		return true, nil
	}
	durable = true
	prefix := ""
	for component := range strings.SplitSeq(dir, string(filepath.Separator)) {
		prefix = filepath.Join(prefix, component)
		created, levelErr := mkdirLevelInRoot(root, prefix, mode)
		if levelErr != nil {
			return false, levelErr
		}
		if !created {
			continue
		}
		if syncErr := fsyncRootDir(root, filepath.Dir(prefix)); syncErr != nil {
			logger.Warn("filesafe: fsync of a created directory's parent failed; write is not durable",
				"root", root.Name(), "dir", prefix, "error", syncErr)
			durable = false
		}
	}
	return durable, nil
}

// mkdirLevelInRoot creates one level of the chain inside root and reports
// whether THIS call created it, which licenses the mode enforcement and the
// parent fsync. A level that already exists as a directory is left
// untouched.
func mkdirLevelInRoot(root *os.Root, prefix string, mode os.FileMode) (created bool, err error) {
	mkErr := root.Mkdir(prefix, mode)
	switch {
	case mkErr == nil:
		if enfErr := enforceDirMode(root, prefix, mode); enfErr != nil {
			return false, enfErr
		}
		return true, nil
	case errors.Is(mkErr, fs.ErrExist):
		fi, statErr := root.Stat(prefix)
		if statErr != nil {
			return false, mkErr
		}
		if !fi.IsDir() {
			return false, &fs.PathError{Op: "mkdir", Path: prefix, Err: syscall.ENOTDIR}
		}
		return false, nil
	default:
		return false, mkErr
	}
}

// enforceDirMode opens a directory this process just created inside root
// and proves the filesystem stored the requested mode.
func enforceDirMode(root *os.Root, dir string, mode os.FileMode) error {
	d, err := openDirInRoot(root, dir)
	if err != nil {
		return err
	}
	defer d.Close()
	_, err = enforceMode(d, mode)
	return err
}

// removeTempInRoot deletes a temp file best-effort, logging at Debug when
// removal fails for a reason other than the file already being gone.
func removeTempInRoot(root *os.Root, tmpName string, logger *slog.Logger) {
	if rmErr := root.Remove(tmpName); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		logger.Debug("filesafe: temp file cleanup failed", "path", tmpName, "error", rmErr)
	}
}

// stagedTemp is what the pre-barrier preamble produces: the open temp file
// plus the names and durability state the two barriers need to publish it.
type stagedTemp struct {
	// file is the open temp file (see createTempInRoot for its mode).
	file *os.File
	// name is the cleaned final name, relative to root.
	name string
	// dir is name's parent inside root, the commit-side dir-fsync target.
	dir string
	// tmpName is the temp file's name, relative to root.
	tmpName string
	// dirSyncFailed records that a directory this write CREATED could not
	// be fsynced into its own parent.
	dirSyncFailed bool
}

// openTempForRoot runs the pre-barrier preamble for every write: validate
// the relative name, honor ctx, refuse a target already occupied by
// something a write must not replace, optionally create the parent
// directory chain, and create the temp file inside root. The guard sequence
// (nil-root -> validateRootEntry -> modeOptionsErr -> ctx ->
// checkWriteTargetInRoot -> mkdir -> createTempInRoot) must not be
// reordered.
func openTempForRoot(ctx context.Context, root *os.Root, name string, c *cfg) (stagedTemp, error) {
	if root == nil {
		return stagedTemp{}, fmt.Errorf("%w: nil root", ErrUnsafePath)
	}
	cleanName, err := validateRootEntry(name)
	if err != nil {
		return stagedTemp{}, err
	}
	if modeErr := c.modeOptionsErr(); modeErr != nil {
		return stagedTemp{}, modeErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stagedTemp{}, fmt.Errorf("filesafe: %w", ctxErr)
	}
	if tgtErr := checkWriteTargetInRoot(root, cleanName); tgtErr != nil {
		return stagedTemp{}, tgtErr
	}
	dir := filepath.Dir(cleanName)
	dirSyncFailed := false
	if c.mkdirMode != 0 {
		durable, mkErr := mkdirAllInRoot(root, dir, c.mkdirMode, c.logger)
		if mkErr != nil {
			return stagedTemp{}, fmt.Errorf("filesafe: create parent directory %q: %w", dir, mkErr)
		}
		dirSyncFailed = !durable
	}
	tmp, tmpName, err := createTempInRoot(root, dir, c)
	if err != nil {
		return stagedTemp{}, err
	}
	return stagedTemp{
		file:          tmp,
		name:          cleanName,
		dir:           dir,
		tmpName:       tmpName,
		dirSyncFailed: dirSyncFailed,
	}, nil
}

// writeAtomicInRoot is the write engine: run the pre-barrier preamble
// (openTempForRoot), run the caller's writeData step, then hand off to the
// temp-side barrier (finalizeTempFile) and the commit-side barrier
// (commitTempInRoot). Every filesystem operation runs through the
// *os.Root, so a symlink or ".." component can never cause a write outside
// root's tree.
func writeAtomicInRoot(ctx context.Context, root *os.Root, name string, c *cfg, writeData func(*os.File) error) (Result, error) {
	st, err := openTempForRoot(ctx, root, name, c)
	if err != nil {
		return Result{}, err
	}
	committed := false
	defer func() {
		if !committed {
			removeTempInRoot(root, st.tmpName, c.logger)
		}
	}()
	if wErr := writeData(st.file); wErr != nil {
		st.file.Close()
		return Result{}, &WriteError{Phase: PhaseTempWrite, Err: wErr}
	}
	if fErr := finalizeTempFile(ctx, st.file, c); fErr != nil {
		return Result{}, fErr
	}
	committed = true
	durable, cErr := commitTempInRoot(ctx, root, st.file, st.tmpName, st.name, st.dir, c)
	if cErr != nil {
		return Result{}, cErr
	}
	return Result{Path: filepath.Join(root.Name(), st.name), Durable: durable && !st.dirSyncFailed}, nil
}

// WriteFileInRoot is WriteFile for name, a path relative to root: every
// filesystem operation runs through the *os.Root, so a symlink or ".."
// component in name can never write outside root's tree. File mode: see
// WithMode. A nil error means the data is at name; check Result.Durable for
// crash durability. Result.Path is root's directory joined with the cleaned
// relative name. A nil root returns ErrUnsafePath.
func WriteFileInRoot(ctx context.Context, root *os.Root, name string, data []byte, opts ...Option) (Result, error) {
	c := buildCfg(opts)
	if err := checkWriteCap(int64(len(data)), c.maxBytes); err != nil {
		return Result{}, err
	}
	return writeAtomicInRoot(ctx, root, name, c, writeBytes(data))
}

// WriteReaderInRoot atomically writes the contents of r to name, a path
// relative to root, confined to root's tree (see WriteFileInRoot). If r
// implements io.WriterTo it is used for efficient copying, so cancellation
// is coarse on that path. File mode: see WithMode. A nil root returns
// ErrUnsafePath.
func WriteReaderInRoot(ctx context.Context, root *os.Root, name string, r io.Reader, opts ...Option) (Result, error) {
	if root == nil {
		return Result{}, fmt.Errorf("%w: nil root", ErrUnsafePath)
	}
	if r == nil {
		return Result{}, errors.New("filesafe: nil reader")
	}
	c := buildCfg(opts)
	return writeAtomicInRoot(ctx, root, name, c, copyReader(ctx, r, c.maxBytes))
}

// NewPendingFileInRoot is NewPendingFile for name, a path relative to root,
// with WriteFileInRoot's confinement; the lifecycle is identical. File mode:
// see WithMode. The caller owns root and must keep it open for the
// PendingFile's lifetime; a caller-provided root is never closed.
// Result.Path is root's directory joined with the cleaned relative name. A
// nil root returns ErrUnsafePath.
func NewPendingFileInRoot(ctx context.Context, root *os.Root, name string, opts ...Option) (*PendingFile, error) {
	return newPendingFromRoot(ctx, root, name, false, buildCfg(opts))
}
