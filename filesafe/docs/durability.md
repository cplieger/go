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

On Windows `Durable` is true only on NTFS, when both the temp sync and the flush after the rename succeeded. A failed flush is logged at `Warn`. On ReFS, FAT, exFAT and network shares every write reports `Durable == false`, because nothing documents that a flush persists a rename there. The package logs that once per volume, at `Info`, rather than warning on every write.

A caller that needs strict durability treats `Durable == false` as a failure to act on, by retrying or alerting. A caller that only needs atomicity can ignore it. A non-nil error always means the data did not reach its final path.

`WithMkdirMode` is covered by the same guarantee. Each directory the write creates has its own parent synced as it is made. A directory entry that was never synced into its parent can vanish in a crash and take the whole subtree with it, including a file that was itself synced and renamed. A write that creates three directory levels therefore syncs four directories, and `Result.Durable` is false if any of those syncs failed.

## Concurrent writers

Each write stages its own temp file under a random name, created exclusively. Two writers on one path each publish a whole file, and the last rename wins. The package takes no lock, so a caller that must not lose an update coordinates its writers itself.

## Errors

Every error matches with `errors.Is` or `errors.As`.

| Error | Meaning |
| --- | --- |
| `ErrEmptyPath` | The path or name argument was empty |
| `ErrUnsafePath` | The path is not absolute, holds a NUL byte or names no entry (`.`, `..`), or a root-relative name is absolute. On Windows a rooted path (`\data`) or a drive-relative one (`C:data`) is not absolute. See [Confinement](confinement.md) |
| `ErrFileTooLarge` | A file exceeded the read limit, or content exceeded a `WithMaxBytes` cap. On a write it may arrive inside a `*WriteError` |
| `ErrSymlinkTarget` | The target of a write is a symlink, or `OpenRegular`, `OpenRegularInRootNoFollow` or `EnsurePrivateDir` refused one |
| `ErrRaced` | `OpenRegularInRootNoFollow` saw the name change identity between its symlink check and its open. A retry is the right response |
| `ErrNotRegular` | The name resolved to a directory, FIFO, device node or socket. Refused on read, on remove and on write |
| `ErrNotDirectory` | `EnsurePrivateDir` found a file, FIFO, device node or socket at the name |
| `ErrNotOwned` | `EnsurePrivateDir` found a directory not owned by the effective user, or could not tell |
| `ErrModeTooOpen` | `EnsurePrivateDir` found a pre-existing directory with group or other access, and no repair was asked for |
| `ErrModeNotStored` | `EnforceMode`, or a write under `WithMode` or `WithMkdirMode`, found the filesystem stored a different mode than asked |
| `ErrAborted` | `PendingFile.Commit` was called after `Cleanup` aborted the write |
| `ErrFileBusy` | Windows only. Another handle refused the replace on every retry, or the context ended while waiting to retry. It arrives inside a `*WriteError` with `PhaseRename`, wrapping the last `syscall.Errno`: a sharing or lock violation means a handle held the file, and access denied is either such a handle or an ACL refusal |
| `ErrModeUnsupported` | Windows only. `EnforceMode`, `EnsurePrivateDir`, or a write or probe given `WithMode` or `WithMkdirMode`. Returned before anything is created |

`ErrNotRegular` arrives as a `*NotRegularError` carrying `Name` and `Mode`. Use `errors.As` to report what was there instead of parsing the message.

A failure inside the write itself is a `*WriteError{Err, Phase}`. `Phase` is one of `PhaseTempCreate`, `PhaseTempWrite`, `PhaseTempChmod`, `PhaseTempSync`, `PhaseTempClose` or `PhaseRename`. `PhaseTempCreate` covers opening the target's directory, so a missing parent without `WithMkdirMode` surfaces there, as well as creating the temp file. `PhaseTempChmod` occurs only under `WithMode`. On Windows the temp stays open until after the rename, so `PhaseTempClose` does not occur there, and `PhaseRename` includes the retry of a busy target. A failed directory sync after the rename has no phase, because `Result.Durable` reports it, and neither has the flush that replaces it on Windows.

Failures before the write starts keep their own types. Path and symlink failures use the values in the table. On Windows a target with the read-only attribute is refused before the temp file is created, with an error matching `fs.ErrPermission`. Context failures wrap `context.Canceled` or `context.DeadlineExceeded`. A `WithMkdirMode` directory creation failure wraps the underlying `os` error behind the `filesafe:` prefix. A `*WriteError` always means the data did not reach its final path.
