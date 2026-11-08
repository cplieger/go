package filesafe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// OpenParentInRoot opens the parent directory of name as its own *os.Root,
// pinned to the directory that was inspected, and returns that root
// together with name's final element.
//
// *os.Root confines a path but does not pin it: it FOLLOWS an in-root
// symlink component, so a multi-component name that is stat-ed and then
// operated on can address two different files if an ancestor directory is
// swapped for a symlink in between. The descent here closes that window:
// each component is Lstat-ed (a symlink is refused, not followed), required
// to be a real directory, opened as its own root, and confirmed with
// os.SameFile against what was inspected. Naming only the returned base
// through the returned root then removes every ancestor from the
// operation's path.
//
// Reach for it whenever two operations on one path must address the same
// file and the tree is one other writers can modify. A single operation
// through the caller's own root needs none of this.
//
// The returned root is ALWAYS a fresh handle the caller must close, even
// for a name directly under root, so the close never risks closing the
// caller's root. The caller owns root; OpenParentInRoot does not close it.
//
// A vanished component matches fs.ErrNotExist and a denied one
// fs.ErrPermission. A component that is a symlink matches ErrSymlinkTarget,
// one that is any other non-directory matches ErrNotDirectory, and one that
// changed while it was being opened matches ErrRaced, the one refusal a
// retry can cure; each names the component. An empty name is ErrEmptyPath;
// an absolute or NUL-holding name, ".", ".." and a nil root are
// ErrUnsafePath.
func OpenParentInRoot(root *os.Root, name string) (*os.Root, string, error) {
	if root == nil {
		return nil, "", fmt.Errorf("%w: nil root", ErrUnsafePath)
	}
	clean, err := validateRootEntry(name)
	if err != nil {
		return nil, "", err
	}
	parent, err := pinDirInRoot(root, filepath.Dir(clean))
	if err != nil {
		return nil, "", err
	}
	return parent, filepath.Base(clean), nil
}

// RemoveFileInRoot removes the regular file at name inside root, unlinking
// it through a parent directory pinned by OpenParentInRoot so no ancestor
// component can redirect the removal at another file. It is the removal
// counterpart to WriteFileInRoot.
//
// Only a regular file is removed. A directory, symlink, device node, socket
// or named pipe wearing the name is refused with ErrNotRegular and left as
// found — the same rule ReadBoundedInRoot applies to what it reads. The
// refusal is a check before the unlink, and os.Root has no unlink that
// refuses a directory, so an empty directory a racing writer swaps in after
// the check can be removed.
// Removing a symlink itself, or an empty directory, is not this function's
// job; use the pinned root from OpenParentInRoot for that.
//
// A missing file is reported (matching fs.ErrNotExist) rather than
// swallowed. The caller owns root; RemoveFileInRoot does not close it. The
// parent's refusals are OpenParentInRoot's.
func RemoveFileInRoot(root *os.Root, name string) error {
	parent, base, err := OpenParentInRoot(root, name)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()

	// Lstat, not Stat: a symlink must be reported as itself, never resolved.
	fi, err := parent.Lstat(base)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return notRegular(name, fi.Mode())
	}
	return parent.Remove(base)
}

// pinOpenHook, when set by a test, runs between an Lstat and the open it
// guards, the window a racing writer swaps the name in.
var pinOpenHook func(name string)

// racedOpen classifies an open of name that failed after an Lstat of it
// returned before. ENOTDIR, ELOOP and ENXIO can only mean the name changed:
// each caller's Lstat proved a directory or a regular file and no symlink, a
// directory is opened through name/. (so a non-directory there answers
// ENOTDIR), and open(2) answers ENXIO only for a socket or a device with no
// driver. An error os.Root makes itself ("not a directory", "path escapes
// from parent") means the resolution met something the Lstat did not see;
// a closed root is the caller's misuse. Any other failure is a race when
// name, looked up again, is present but is not the file the Lstat saw, or is
// present after an open that found nothing. Those are ErrRaced, the refusal
// a retry can cure; a name that is gone stays fs.ErrNotExist and every other
// failure is returned as it is.
func racedOpen(root *os.Root, name string, before os.FileInfo, err error) error {
	var errno syscall.Errno
	isErrno := errors.As(err, &errno)
	changed := isErrno && (errno == syscall.ENOTDIR || errno == syscall.ELOOP || errno == syscall.ENXIO) ||
		!isErrno && !errors.Is(err, os.ErrClosed)
	if !changed {
		now, lErr := root.Lstat(name)
		switch {
		case lErr != nil:
			// Gone or unstatable now: the open's own error is the answer.
		case errors.Is(err, fs.ErrNotExist):
			changed = true // present before and after, absent at the open
		default:
			changed = !os.SameFile(before, now)
		}
	}
	if changed {
		return fmt.Errorf("%w: %w", ErrRaced, err)
	}
	return err
}

// pinDirInRoot descends dir component by component from root and returns
// the innermost directory as its own root, each component confirmed to be
// the same directory that was inspected. dir is cleaned and relative, "."
// for root itself.
func pinDirInRoot(root *os.Root, dir string) (*os.Root, error) {
	// A fresh handle even for ".": openat(fd, ".") resolves no name, so it
	// cannot be redirected — the open handle IS the pin.
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("filesafe: open root: %w", err)
	}
	if dir == "." {
		return current, nil
	}
	for component := range strings.SplitSeq(dir, string(filepath.Separator)) {
		next, pinErr := pinChildInRoot(current, component)
		// Closed unconditionally: the descent holds one handle at a time.
		_ = current.Close()
		if pinErr != nil {
			return nil, pinErr
		}
		current = next
	}
	return current, nil
}

// pinChildInRoot opens one directory component of parent as its own root,
// refusing anything that is not a real directory and anything whose
// identity changed between the Lstat and the open.
func pinChildInRoot(parent *os.Root, name string) (*os.Root, error) {
	fi, err := parent.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("filesafe: inspect path component %q: %w", name, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		// os.Root would otherwise follow it.
		return nil, fmt.Errorf("%w: path component %q is a symlink (mode %s)",
			ErrSymlinkTarget, name, fi.Mode().String())
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%w: path component %q is not a directory (mode %s)",
			ErrNotDirectory, name, fi.Mode().String())
	}
	if pinOpenHook != nil {
		pinOpenHook(name)
	}
	next, err := parent.OpenRoot(dirOpenName(name))
	if err != nil {
		return nil, fmt.Errorf("filesafe: open path component %q: %w", name, racedOpen(parent, name, fi, dirOpenErr(err, name)))
	}
	opened, err := next.Stat(".")
	if err != nil {
		_ = next.Close()
		return nil, fmt.Errorf("filesafe: confirm path component %q: %w", name, err)
	}
	if !os.SameFile(fi, opened) {
		_ = next.Close()
		return nil, fmt.Errorf("%w: path component %q changed while it was being opened", ErrRaced, name)
	}
	return next, nil
}
