# Durability and errors

This page describes the steps every filesafe write takes, what its `Result` reports and every error it can return, for a developer deciding how to handle each outcome.

## The write sequence

Every write, from `WriteFile` to `PendingFile.Commit`, follows the same steps:

1. Create a temp file in the target's own directory, on the same mount, so the rename is atomic.
2. Write the data.
3. Under `WithMode`, set the mode on the open file and read it back.
4. Sync the temp file.
5. Close it.
6. Rename it over the target.
7. Sync the parent directory.

After a crash the target holds the complete old content or the complete new content, never a partial write. The directory sync in step 7 makes the rename itself survive a power loss right after the call returns.

### On Windows

Windows has no directory sync, and `WithMode` is refused before step 1, so the steps differ after the write:

1. Create a temp file in the target's own directory.
2. Write the data.
3. Sync the temp file, and keep it open.
4. Rename it over the target with one `FileRenameInformationEx` call that uses POSIX semantics. A refusal caused by another process's handle is retried 7 times over about 0.6 seconds.
5. Flush the published file through the handle still open on it, then close it.

NTFS journals the rename but writes its journal lazily, so a power loss right after the rename could still roll it back to the old content. The flush in step 5 closes that window. A flush of a file also persists the directory structure ([MS-FSA 2.1.5.7](https://learn.microsoft.com/openspecs/windows_protocols/ms-fsa/0de7dc40-9627-437e-a4df-c4696cdc3d02)), and the specification's product notes say only NTFS implements that, while other filesystems report success and do nothing.

## What Result reports

```go
type Result struct {
	Path    string // the cleaned final path
	Durable bool   // true only when the file and every directory its path depends on were synced
}
```

`Path` is absolute for the package-level writes. For the `*InRoot` writes it is the root's directory joined with the cleaned relative name.

A nil error means the data reached its final path. The write either fully succeeded, or its rename completed while a directory sync failed, for example with an `EIO` from a failing disk. In that case the data is at the path, and the write logs the sync failure at `Warn`. `Result.Durable` tells the two outcomes apart, so a caller that cares about crash durability reads it rather than decoding error types.

On Windows `Durable` is true only on a local NTFS volume, when both the temp sync and the flush after the rename succeeded. A failed flush is logged at `Warn`. On ReFS, FAT, exFAT and network shares every write reports `Durable == false`, because nothing documents that a flush persists a rename there. A share or mapped drive reports the server's filesystem name, often NTFS, so the package asks whether the volume is remote rather than trusting the name. The package logs that once per volume, at `Info`, rather than warning on every write.

A caller that needs strict durability treats `Durable == false` as a failure to act on, by retrying or alerting. A caller that only needs atomicity can ignore it. A non-nil error always means the data did not reach its final path.

`WithMkdirMode` and `WithMkdirAll` are covered by the same guarantee. Each directory the write creates has its own parent synced as it is made, except on Windows, which has no directory sync: there the write's flush of the published file stands for it on local NTFS, and `Result.Durable` is false on the volumes listed above. A directory entry that was never synced into its parent can vanish in a crash and take the whole subtree with it, including a file that was itself synced and renamed. On Linux and macOS a write that creates three directory levels therefore syncs four directories, and `Result.Durable` is false if any of those syncs failed. A level another writer created between the write's check and its own `mkdir` is synced too on the absolute-path writes; the `*InRoot` writes sync only the levels they create.

`EnsurePrivateDir` syncs the parent of its directory on every call, whether it created the directory or found it, so a file a write reports as durable inside that directory does not vanish with it. A failed sync returns `ErrParentNotSynced`, which is safe to retry. A parent the process can traverse but not read fails the sync on every call, so it returns an error matching `fs.ErrPermission` instead, which a retry does not cure.

## Concurrent writers

Each write stages its own temp file under a random name, created exclusively. Two writers on one path each publish a whole file, and the last rename wins. A write takes no lock, so a caller that must not lose an update holds a lease around each read and write, as [Leases](leases.md) explains.

## Errors

Every error matches with `errors.Is` or `errors.As`.

| Error | Meaning |
| --- | --- |
| `ErrEmptyPath` | The path or name argument was empty |
| `ErrUnsafePath` | The path is not absolute, holds a NUL byte or names no entry (`.`, `..`), or a root-relative name is absolute. On Windows a rooted path (`\data`) or a drive-relative one (`C:data`) is not absolute. See [Confinement](confinement.md). A nil `*os.Root` or `*os.File` is refused with it too |
| `ErrInvalidArgument` | An argument no call could mean: a non-positive sweep `maxAge`, a negative read `maxBytes`, `WithMode(0)` or `WithMkdirMode(0)`, or a nil reader or walk function. Nothing is created, read or removed first |
| `ErrFileTooLarge` | A file exceeded the read limit, or content exceeded a `WithMaxBytes` cap. On a write it may arrive inside a `*WriteError` |
| `ErrSymlinkTarget` | The target of a write is a symlink, `OpenRegularNoFollow`, `OpenRegularInRootNoFollow`, `EnsurePrivateDir`, a `CheckPrivateDir` call or a lease refused one, or `OpenParentInRoot` found a symlinked ancestor |
| `ErrRaced` | `OpenRegularInRootNoFollow` saw the name change identity between its symlink check and its open, `CheckPrivateDirInRoot` saw it change between its check and its open, `TryLease` or `TrySharedLease` found the lock file replaced between its open and its lock, `AcquireLease` or `AcquireSharedLease` saw the context end while it kept being replaced (wrapping the context's error), or `OpenParentInRoot` saw an ancestor directory do so. A retry is the right response |
| `ErrNotRegular` | The name resolved to a directory, FIFO, device node or socket. Refused on read, on remove, on write and as a lock file |
| `ErrNotDirectory` | `EnsurePrivateDir` or a `CheckPrivateDir` call found a file, FIFO, device node or socket at the name, or `OpenParentInRoot` found one in an ancestor's place |
| `ErrNotOwned` | `EnsurePrivateDir` or a `CheckPrivateDir` call found a directory, or a lease a lock file, not owned by the effective user, or could not tell. A directory or lock file whose owner's mode refused the lookup or open is this error too, wrapping that refusal, so it also matches `fs.ErrPermission`. A parent the process cannot search or read is not this error: it matches `fs.ErrPermission` alone |
| `ErrModeTooOpen` | `EnsurePrivateDir` found a pre-existing directory with group or other access, and no repair was asked for, a `CheckPrivateDir` call found one, which it never repairs, or a lease found a lock file with such access |
| `ErrLeaseHeld` | `TryLease` or `TrySharedLease` found the lease held by another holder, or `AcquireLease` or `AcquireSharedLease` saw the context end while it was, wrapping the context's error |
| `ErrParentNotSynced` | `EnsurePrivateDir` could not open or fsync the parent of the directory after every check passed. The directory stays where it is, whether the call created it or found it, so a retry finds it and syncs again. Safe to retry, unlike the other `EnsurePrivateDir` errors. A parent the process cannot read is not this error: it matches `fs.ErrPermission` and fails every retry |
| `ErrModeNotStored` | `EnforceMode`, a write or probe under `WithMode` or `WithMkdirMode`, or a lease creating its lock file at 0600, found the filesystem stored a different mode than asked. A directory level refused this way is removed again while it is still an empty directory, so a retry fails the same way; one another writer has already filled stays, and the error also carries the removal failure. A lock file refused this way is removed before it reaches its path |
| `ErrAborted` | `PendingFile.Commit` was called after `Cleanup` or `Close` aborted the write |
| `ErrFileBusy` | Windows only. Another handle refused the replace on every retry, or the context ended while waiting to retry. It arrives inside a `*WriteError` with `PhaseRename`, wrapping the last `syscall.Errno`: a sharing or lock violation means a handle held the file, and access denied is either such a handle or an ACL refusal |
| `ErrModeUnsupported` | Windows only. `EnforceMode`, `EnsurePrivateDir`, a `CheckPrivateDir` call, a lease (with `errors.ErrUnsupported` too), or a write or probe given a nonzero `WithMode` or `WithMkdirMode`; a zero mode is `ErrInvalidArgument`. Returned before anything is created |

Each refusal that found something worth naming arrives as a typed error carrying what the check read, so a caller can report it with `errors.As` instead of parsing the message or looking at the path again, by which time it may have changed. Each unwraps to its sentinel, so `errors.Is` still matches.

| Sentinel | Typed error | Fields |
| --- | --- | --- |
| `ErrNotRegular` | `*NotRegularError` | `Name`, and `Mode`, whose `Type()` names what was there |
| `ErrNotDirectory` | `*NotDirectoryError` | `Name`, and `Mode`, whose `Type()` names what was there; for `OpenParentInRoot`, `Name` is the ancestor component |
| `ErrNotOwned` | `*NotOwnedError` | `Name`, `Owner`, the uid that owns it or -1 when that could not be read, and `EUID`, the effective uid it was compared with |
| `ErrModeTooOpen` | `*ModeTooOpenError` | `Name`, and `Mode`, the permission bits plus setuid, setgid and sticky the check read |
| `ErrModeNotStored` | `*ModeNotStoredError` | `Name`, `Requested` and `Stored`, in the same bits |
| `ErrFileTooLarge` | `*FileTooLargeError` | `Limit`, the cap, and `Size`, how large the content was found or would have grown: a lower bound on its full size |

A failure inside the write itself is a `*WriteError{Err, Phase}`. `Phase` is one of `PhaseTempCreate`, `PhaseTempWrite`, `PhaseTempChmod`, `PhaseTempSync`, `PhaseTempClose` or `PhaseRename`. `PhaseTempCreate` covers opening the target's directory, so a missing parent without `WithMkdirMode` or `WithMkdirAll(true)` surfaces there, as well as creating the temp file. `PhaseTempChmod` occurs only under `WithMode`. On Windows the temp stays open until after the rename, so `PhaseTempClose` does not occur there, and `PhaseRename` includes the retry of a busy target. A failed directory sync after the rename has no phase, because `Result.Durable` reports it, and neither has the flush that replaces it on Windows.

Failures before the write starts keep their own types. Path and symlink failures use the values in the table. On Windows a target with the read-only attribute is refused before the temp file is created, with an error matching `fs.ErrPermission`. Context failures wrap `context.Canceled` or `context.DeadlineExceeded`. A `WithMkdirMode` or `WithMkdirAll` directory creation failure wraps the underlying `os` error behind the `filesafe:` prefix. A `*WriteError` always means the data did not reach its final path.
