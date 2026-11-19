# The key file

This page covers where to put the key file, how one process holds it and how to rotate its keys. It also lists what each error means and where a key file works, for a developer wiring sealed into an app and for the person who runs that app.

## Where to put it

- Pass an absolute path from your app's own setting. The module reads no environment variable.
- Put the file in a directory only the app's user can enter, owned by that user, with no group or other access. `Create` makes a missing directory, one level, at 0700. It also syncs the directory's entry into its parent, so the app's user must be able to read the parent at the start that creates the file.
- The file is written at 0600, owned by the app's effective user. An open accepts a mode with no permission bit outside 0600, such as 0600 or 0400. It refuses any other bit (group or other access, owner execute, setuid, setgid or sticky), another user's file, a symlink and anything that is not a regular file.
- The directory must be on a writable filesystem, since the lock file and every rotation are written there.

## Opening and creating

`OpenKeyFile` never creates anything, a directory included. Where nothing is at the path, it returns a `KeyFile` holding no key, whose `Sealer` answers `ErrNoKey` from every call that needs a key. Any error from `OpenKeyFile` is a key outage, never a reason to make a new key. An app that keeps running through one hands its stores a nil `*Sealer`, which answers `ErrNoKey` too, and shows its key-file state.

Call `Create` only at the app's first start, or when the app gives up what a lost key file sealed, since a new key opens nothing sealed before it. Create the file only when the app's store holds no sealed value. A lost key file then shows as an outage instead of a silent new key. `Create` answers `ErrKeyExists` and writes nothing when the `KeyFile` holds a key or anything is at the path.

## One process holds the file

Beside the key file, the module keeps an empty lock file, the key file's name with `.lock` added. It is created at 0600 the first time a `KeyFile` holds the key file, and nothing removes it. Leave it in place at that mode. A lock file that is a symlink, not a regular file, another user's or open to group or other is refused with `ErrUnsafeKeyFile`.

A `KeyFile` that holds a key holds an exclusive lease on the lock file, from `OpenKeyFile` or `Create` until `Close`. A second `KeyFile` on the same path, in the same process or another, waits for the lease until its context ends, then answers `ErrKeyFileInUse`. So:

- Open each key file once per process, and share the `KeyFile`.
- Run `Rotate` and `Retire` in the process that serves the key file, from the app's own admin command. A second process that opens the file waits, then is refused.
- When an old and a new instance overlap during a restart, the new one waits for the lease within the context it passes to `OpenKeyFile`. The wait ends when the old process exits.
- Close the `KeyFile` last at shutdown, after the stores that use its `Sealer`. `Close` never waits on another holder: a `Create` still waiting for another holder's lease stops at once with an error matching `fs.ErrClosed`. It waits for a `Create`, `Rotate` or `Retire` whose filesystem step is running, such as the key directory's sync, the re-read or the write, until filesafe returns. Such a call that has not begun its write then answers an error matching `fs.ErrClosed` and writes nothing. After `Close`, the `Sealer` answers `ErrNoKey`.

## Rotating keys

1. Call `Rotate`. The new key seals from the moment `Rotate` returns, and the previous key still opens. While a previous key is held, `Rotate` answers `ErrRotationPending`.
2. Seal again every value that opens with `current == false`. A housekeeper that walks the store does this.
3. Take a new `Subkey` and rebuild what holds the old one, or restart. A `Subkey`, and a sealer `New` builds over it, keep the old key after `Rotate` and `Retire`. Any rotation that answers a leaked key needs this step.
4. Call `Retire` behind a person's step, once every value under the previous key is sealed again. Nothing retires a key on its own. A value still under the previous key stops opening.

Keep the one `Sealer` the `KeyFile` gave you for the life of the process, since it reads the keys at each call. Keep values a store holds on that `Sealer`, never on a `Subkey`.

`Rotate` and `Retire` answer `ErrKeyFileChanged` when the file is missing, holds keys this `KeyFile` neither installed nor wrote, or its lock file was replaced. They check this first, so a changed file answers `ErrKeyFileChanged` even where `Rotate` would answer `ErrRotationPending` or `Retire` nil. A person or another tool changed it, so nothing is written. Once someone decides which file is right, restart the app. Or close the `KeyFile`, open the path again, and hand every holder of the old `Sealer` the new one.

## Writes that survive a power loss

Every write goes through filesafe's atomic write, which syncs the new file and then its directory. A write installs its new keys only when filesafe reports it durable. Otherwise the call answers `ErrNotDurable`, with these consequences:

- The new file is at the path, and nothing is installed. The `KeyFile` keeps its keys and its lease, and every file a crash can leave holds the key its `Sealer` seals under.
- The next `OpenKeyFile` adopts whatever is at the path. So fix the disk or the filesystem and reboot the host before the app starts again. Or retry the call through the same `KeyFile` once the cause is fixed.
- A `Create` that could not sync the key directory into its parent wrote no file, and a retry syncs again.
- On a filesystem that cannot sync a directory at all, every write answers `ErrNotDurable`, so no key file can be created there.

## What each error means

| Error | When | What to do |
| --- | --- | --- |
| `ErrNoKey` | No key file exists yet, or the `KeyFile` is closed | Call `Create` at a first start, or show the missing-key state |
| `ErrUnsafeKeyFile` | Another user could read or replace the key file, its directory or its lock file | Fix the owner and mode, or remove the symlink |
| `ErrKeyFileFormat` | Not a key file this release reads, or over 512 bytes | Restore the key file from a backup, or run the release that wrote it |
| `ErrKeyFileInUse` | Another `KeyFile` held the lease until the context ended | Stop the other holder, or wait longer |
| `ErrKeyExists` | `Create` found a key in the `KeyFile` or a file at the path | Open the existing file instead |
| `ErrRotationPending` | `Rotate` while a previous key is held | Seal again, then call `Retire` |
| `ErrKeyFileChanged` | Someone changed or removed the key file or its lock file | Decide which file is right, then restart |
| `ErrNotDurable` | filesafe could not report a write durable | Fix the disk and reboot, or retry once fixed |
| `fs.ErrClosed` | A write on a `KeyFile` whose `Close` has begun | Nothing at shutdown, since nothing was written |
| `fs.ErrPermission` | The app's user cannot read a directory on the path, such as the parent `Create` syncs | Make it readable by the app's user |
| `filesafe.ErrModeNotStored` | The filesystem did not keep the 0600 of a new key file or lock file | Move the key file to a filesystem that keeps modes |
| `filesafe.ErrEmptyPath`, `filesafe.ErrUnsafePath` | The path is empty, relative or holds a NUL byte | Pass an absolute path |

An error from a done context wraps the context's error. A key-file error names the path, and no error shows a key or a line of the file.

## Filesystems and hosts

- The lease is an exclusive `flock(2)` on the lock file, opened for writing, which works on local filesystems, NFS and SMB.
- One host only. In three setups the lease excludes only processes on the same host, as flock(2) and nfs(5) describe:
  - SMB before Linux 5.5.
  - An NFS mount with `local_lock=flock` or `local_lock=all`.
  - An NFS mount with `nolock`.
- An NFS server exported with `async` may lose a key file it reported written, as exports(5) warns.
- An existing key file in a read-only directory makes `OpenKeyFile` fail with the filesystem's error, because the lock file cannot be created or opened for writing there.
- On an NFS `hard` mount whose server is down, a running sync, re-read or write holds `Close` until the server answers.

## Systems

A key file works on Linux. It also runs, untested, on macOS, DragonFly BSD, FreeBSD, NetBSD, OpenBSD and illumos, which have file owners, modes and `flock(2)`. Windows, AIX and Solaris refuse a key file with an error matching both `ErrUnsafeKeyFile` and `filesafe.ErrModeUnsupported`. The module cannot check owners and modes or take the lease there. `New`, `NewEphemeral` and every `Sealer` call work on all of them. js, wasip1 and Plan 9 are unsupported, since filesafe does not build there.

## Backup and restore

- Back up the key file by copying it. The lock file is not part of a backup.
- Restore it only while the app is stopped. A restored key file without a lock file gets a new one at its first open. It is as durable as the copy that made it.
- Losing the key file loses every value sealed under it, the secrets in an app's export archives included. A restore onto a new install needs the key file, or nothing it sealed opens.
- No call imports a key. Restoring is copying the file back.
