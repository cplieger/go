# File modes and private directories

This page explains how filesafe sets file and directory modes, and how `EnsurePrivateDir` sets up a directory only one user may enter, for a developer writing credentials, configuration or backups.

None of it applies on Windows. There Go's chmod only sets or clears the read-only attribute, and access is governed by the ACL a new file inherits from its directory. `EnforceMode`, `EnsurePrivateDir`, `CheckPrivateDir`, `CheckPrivateDirInRoot`, and any write or probe given a nonzero `WithMode` or `WithMkdirMode` return `ErrModeUnsupported` before anything is created or changed. A zero mode is `ErrInvalidArgument` on every platform. A write without them creates the file with the directory's inherited ACL, and `WithMkdirAll(true)` creates missing parent directories the same way.

## Ordinary and enforced modes

Pass `WithMode` for any file whose readers must be restricted, such as credentials, configuration and backups. The staging file is created 0600 and proven owner-only before any data lands in it, so the payload is never staged in a file others can open. The final file is set to the requested mode and proven to hold it. A filesystem that stores anything else fails the write with `ErrModeNotStored` rather than publish a file wider than asked.

Omit `WithMode` for a file in a shared tree whose permissions an operator manages through the umask or inherited ACLs, such as a media library. Such a file is created like `os.Create`, at 0666 through the umask, and is never chmod'ed, so the tree's own policy applies. Replacing an existing file without `WithMode` gives the new file those creation defaults, not the old file's mode.

`WithMkdirMode(mode)` creates missing parent directories and proves `mode` on each one it creates. A directory that already exists is never chmod'ed. `WithMkdirAll(true)` creates them as `os.MkdirAll` does, at 0777 through the umask, and proves nothing, for a tree whose permissions the umask or inherited ACLs manage.

## A mode argument is a request

A mode passed to `mkdir(2)` or `open(2)` is a request, not a result. The umask narrows it, and a filesystem with an inheritable ACL can widen it whatever was asked. Measured on a ZFS `nfs4acl` dataset, an inheritable `group@:rwx` entry stores 0770 for a 0700 `mkdir`. A child of a parent that is already 0700 comes back 0770 too, so a narrower parent does not cover it.

`EnforceMode(f, want)` sets the mode on the open handle and then reads the mode back from the same handle. It returns the mode the filesystem stored. A mismatch is `ErrModeNotStored`, naming both modes, with the stored mode returned beside the error. Both calls use the descriptor, not the pathname, so a rename in between cannot make it check one file and certify another. It works for a file or a directory, since both are an `*os.File`. It compares only the bits `chmod(2)` can set, the permission bits plus setuid, setgid and sticky.

## Private directories

`EnsurePrivateDir(dir)` sets up one directory level that only the effective user may enter. It is for a private directory inside a parent other users can write, such as a state directory under `/tmp` or a directory for an admin socket. It runs these steps:

- It calls `mkdir` at 0700 and records whether this call created the level. Every later decision turns on that, and `os.MkdirAll` cannot report it.
- It opens the result with `O_DIRECTORY|O_NOFOLLOW|O_RDONLY`. The kernel refuses a planted symlink instead of following it, which returns `ErrSymlinkTarget`, and anything that is not a directory, which returns `ErrNotDirectory`. A planted FIFO cannot stall the call.
- It reads the owner from the open handle and requires the effective uid, or returns `ErrNotOwned`. A 0700 directory owned by another user passes every mode check, and its owner can still replace it after the verdict returns. When that owner's mode refuses the open itself, the owner is read with `lstat` instead, and the error is `ErrNotOwned` wrapping the open's `EACCES`, so it matches `fs.ErrPermission` too. A parent the effective user cannot search returns an error matching only `fs.ErrPermission`.
- When this call created the directory, it runs `EnforceMode` to 0700. That repairs an ACL that widened the mode, and it is safe because no other writer has ever held that name.
- When the directory already existed, its mode is left unchanged by default, and the call returns `ErrModeTooOpen` if any group or other bit is set. Repairing a directory another user made would take over their name and hand them whatever is written under it.
- Once every check has passed, it fsyncs the parent of `dir`, whether this call created `dir` or found it, because another process may have created `dir` a moment earlier and not synced it yet. Without that sync, `dir` and every file in it can vanish in a power loss. Opening the parent for the sync needs read access to it, so a parent the effective user can only traverse fails here, with an error matching `fs.ErrPermission` that a retry does not cure. The directory stays in place.

`WithRepairOwnedDir(true)` repairs a pre-existing directory instead of refusing it, because the ownership check has already proved it belongs to the caller. It is not the default, because a wide mode on a directory you own can be deliberate.

```go
type PrivateDirResult struct {
	Mode     os.FileMode // the mode the filesystem stored, read back from the handle
	Created  bool        // this call created the directory
	Repaired bool        // the directory's mode had to be corrected
}
```

The result is meaningful only beside a nil error. A non-nil error can follow a successful `mkdir`, so the directory may exist while its ownership is unproven. Treat that as fatal rather than retrying into it, except `ErrParentNotSynced`: the parent sync failed after every check passed. The directory stays where it is, whether the call created it or found it, so a retry finds it, reports `Created` false, and syncs again.

`WithLogger` sets the logger for the one `Warn` a repair emits, since a filesystem that ignores mode requests affects every other `mkdir` in the program too. `EnsurePrivateDir` takes only these two options, so a write option passed to it does not compile, and the mode is not a parameter.

### Checking without creating

`CheckPrivateDir(dir)` reaches the same verdict on a directory that must already exist, and creates, changes and syncs nothing. It opens `dir` the same way, then requires a directory owned by the effective uid with no group or other bit set. A missing `dir` matches `fs.ErrNotExist`. The refusals are `EnsurePrivateDir`'s: `ErrSymlinkTarget`, `ErrNotDirectory`, `ErrNotOwned` and `ErrModeTooOpen`, which is never repaired here. Use it where a missing or widened directory means something is wrong, such as a key directory a separate setup step created.

`CheckPrivateDirInRoot(root, name)` runs the same check on `name` inside an `*os.Root` you hold, and `"."` checks the root's own directory as it is now, so a chmod after the root was opened is seen. A root whose directory another user has since taken over returns `ErrNotOwned`. An `*os.Root` exposes no descriptor to inspect, and a directory closed to you blocks every lookup through it, so for `"."` that owner is read from the path the root was opened at. A symlink at `name` is refused, and a name swapped for another directory during the check returns `ErrRaced`, which a retry can cure. Re-run it before each use of a long-held root.

## One level per call

`EnsurePrivateDir` and `CheckPrivateDir` do not create, inspect or vouch for the parent of `dir`. `EnsurePrivateDir` opens the parent only to sync it, and `CheckPrivateDir` does not open it at all. For a path of several levels, loop over the levels yourself, outermost first, because only the caller knows which levels are its own.

The verdict holds at one point in time. It proves what was true of one directory while a handle was open on it, and the handle is closed before the call returns. A later `ReadDir`, `Remove`, `Open` or `Mkdir` through the pathname resolves every component again and reopens the window the check closed. To keep that window closed, hold the directory open as an `*os.Root` for its whole lifetime and act on the names inside it.
