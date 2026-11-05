# filesafe

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/go/filesafe.svg)](https://pkg.go.dev/github.com/cplieger/go/filesafe) [![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/go?filename=filesafe%2Fgo.mod)](https://github.com/cplieger/go/blob/main/filesafe/go.mod)

filesafe writes files from Go so that a crash or a power loss leaves the complete old content or the complete new content, never a partial file.

It replaces the temp file, fsync and rename sequence you would otherwise write around `os.WriteFile`. It runs on Linux, containers included, and on Windows, with the limits listed under [Windows](#windows). It uses only the standard library at run time, needs Go 1.27.3 or later and is licensed under Apache-2.0. macOS and BSD may work but are untested.

## Why use it

filesafe is built for Go services that keep state, configuration or credentials on disk and must not lose them to a crash.

- Every write syncs the file and makes its rename durable, and `Result.Durable` reports whether every sync succeeded.
- A nil error means the new data is at the path. Any other error leaves the old file in place, and a failure inside the write is a `*WriteError` naming the step that failed.
- `WithMode` proves the mode the filesystem stored, so a filesystem that widens a 0600 request fails the write instead of publishing a readable file. It is not available on Windows.
- The `*InRoot` functions run through an `*os.Root`, so a symlink or `..` in a name cannot leave the directory tree.
- Writes refuse a symlink, FIFO, socket or device node at the target path.

Consider [google/renameio](https://github.com/google/renameio) if you also need to replace a symbolic link atomically.

## Install

```sh
go get github.com/cplieger/go/filesafe@latest
```

## Usage

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/cplieger/go/filesafe"
)

func main() {
	ctx := context.Background()

	// Replace a file. Result.Durable reports whether the new content
	// survives a power loss.
	res, err := filesafe.WriteFile(ctx, "/var/lib/myapp/state.json", []byte(`{"ok":true}`))
	if err != nil {
		log.Fatal(err)
	}
	if !res.Durable {
		log.Printf("%s is written but not yet durable", res.Path)
	}

	// A credential: staged owner-only, published at exactly 0600 or not at all.
	_, err = filesafe.WriteFile(ctx, "/var/lib/myapp/token", []byte("secret"),
		filesafe.WithMode(0o600))
	if err != nil {
		log.Fatal(err)
	}

	// Confined to one tree: a symlink or ".." in the name cannot leave it.
	root, err := os.OpenRoot("/var/lib/myapp")
	if err != nil {
		log.Fatal(err)
	}
	defer root.Close()
	_, err = filesafe.WriteFileInRoot(ctx, root, "cache/index.json", []byte("[]"),
		filesafe.WithMkdirMode(0o750))
	if err != nil {
		log.Fatal(err)
	}

	// Read it back under a 1 MiB cap.
	data, err := filesafe.ReadBoundedInRoot(ctx, root, "cache/index.json", 1<<20)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("read %d bytes", len(data))
}
```

To stream content, use `WriteReader`. To write a file piece by piece, call `NewPendingFile`, write to it, then `Commit` to publish it or `Cleanup` to abort. The 23 runnable examples on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/filesafe#pkg-examples) cover writes, reads, sweeps, probes and private directories, and `go test` runs them.

## API

- Writes: `WriteFile`, `WriteReader`, `NewPendingFile` and their `*InRoot` forms.
- Reads: `ReadBounded`, `ReadBoundedFile`, `ReadBoundedInRoot`, `OpenRegular`, `OpenRegularInRoot`, `OpenRegularInRootNoFollow`.
- Shared directory trees: `WalkDirInRoot`, `OpenParentInRoot`, `RemoveFileInRoot`.
- Modes: `EnforceMode`, `EnsurePrivateDir`.
- Temp files and checks: `CleanupStaleTemps`, `CleanupStaleTempsInRoot`, `TempName`, `IsPackageTemp`, `ProbeWritable`, `ProbeWritableInRoot`, `ValidatePath`, `Identify`.
- Options: `WithMode`, `WithMkdirMode`, `WithMaxBytes`, `WithLogger`, `WithRecursive`, `WithRepairOwnedDir`.
- Errors: sentinel values for `errors.Is`, `ErrFileBusy` and `ErrModeUnsupported` among them, and `*WriteError` and `*NotRegularError` for `errors.As`.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/go/filesafe). [Choosing a function](docs/choosing-a-function.md) says which call fits which job.

## What a nil error means

Every write creates a temp file in the target's directory, writes and syncs it, renames it over the target, then syncs the directory. Windows has no directory sync, so there the write flushes the published file instead. After a crash the path holds the complete old content or the complete new content.

A nil error means the data reached its final path. `Result.Durable` then says whether that content will survive a power loss. It is false when the sync after the rename failed, for example with an `EIO` from a failing disk. A crash could then still bring back the old file. The package logs a warning in that case, and a caller that needs crash durability retries the write or raises an alert. A non-nil error means the data did not reach the path, and the previous file is untouched.

Two writers on one path each publish a whole file, and the last rename wins. The package takes no lock.

A process killed before the rename leaves its temp file behind. Nothing in the package removes it on its own, so call `CleanupStaleTemps` at startup or on a schedule, as [Orphaned temp files](docs/temp-files.md) explains.

[Durability and errors](docs/durability.md) has the full sequence and every error value.

## File modes

Without `WithMode`, filesafe sets no mode. The file is created the way `os.Create` creates one, at 0666, narrowed by the umask and widened or narrowed by any ACL the directory passes on. A write that replaces an existing file gives the new file those defaults, and the old file's mode is not kept. That suits a shared tree whose permissions an operator manages, such as a media library.

Pass `WithMode` for any file whose readers must be restricted, such as credentials, configuration and backups. The payload is staged in a file proven owner-only, the final file is proven to hold the mode, and a filesystem that stores anything else fails the write with `ErrModeNotStored`.

`WithMkdirMode` creates missing parent directories at a mode it also proves. `EnsurePrivateDir` sets up one directory level that only the effective user may enter. [File modes and private directories](docs/file-modes.md) explains both.

## Windows

The writes, the reads, the sweeps and the probes work on Windows (amd64 and arm64). filesafe never calls `os.Rename`, which on Windows gives no atomic-replace guarantee ([golang/go#22397](https://github.com/golang/go/issues/22397#issuecomment-498856679)). It renames through `*os.Root`, which issues one `FileRenameInformationEx` call with `FILE_RENAME_REPLACE_IF_EXISTS` and `FILE_RENAME_POSIX_SEMANTICS`. That call replaces the target in one step, even while other processes hold it open, as long as their handles allow deletion ([FILE_RENAME_INFORMATION](https://learn.microsoft.com/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information)).

These limits apply on Windows:

- A target another process holds open without delete sharing cannot be replaced while that handle stays open. `os.Open` and `os.ReadFile` in any Go program open files that way, and so do many editors, backup tools and virus scanners. The write retries 7 times over about 0.6 seconds, then fails with a `*WriteError` in `PhaseRename` matching `ErrFileBusy`. A cancelled context ends the retry early. `ReadBounded`, `ReadBoundedInRoot` and the `OpenRegular` functions, like any open through an `*os.Root`, allow deletion and never block a write.
- `ErrFileBusy` also covers an access-denied refusal, because Windows reports a file held open and a refusing ACL with the same error. The wrapped `syscall.Errno` tells which.
- Go renames without POSIX semantics when a filesystem refuses them. That rename is still atomic, but it is refused while any handle is open on the target, even one that allows deletion, so the write retries and fails with `ErrFileBusy` as above.
- A target with the read-only attribute is refused before anything is written, with an error matching `fs.ErrPermission`.
- `WithMode`, `WithMkdirMode`, `EnforceMode` and `EnsurePrivateDir` return `ErrModeUnsupported` before anything is created. Go's chmod on Windows only sets the read-only attribute, and access is governed by the ACL the new file inherits from its directory. Create a missing parent directory yourself, for example with `os.MkdirAll`.
- There is no directory sync. On a local NTFS volume the write flushes the published file after the rename, which also persists the rename. On ReFS, FAT, exFAT and network shares nothing documents that, so `Result.Durable` is false there, and the package logs that at `Info` once per volume for the life of the process. A share or mapped drive counts as a network share even when its server's volume is NTFS.
- A symlink at the target is refused with `ErrSymlinkTarget`, as on Linux. A junction or any other reparse point, such as a cloud-sync placeholder, is refused with `ErrNotRegular`.
- An absolute path starts with a drive letter (`C:\`) or is a UNC path (`\\server\share\`). A rooted path such as `\data` and a drive-relative path such as `C:data` are refused with `ErrUnsafePath`, and so is either form as a root-relative name.
- A stale temp file another process holds open without delete sharing cannot be removed. The sweep counts it in `Failed` and leaves it for a later sweep.

## Unsupported by design

These are deliberate non-goals, not missing features.

- `fs.FS` interop. `fs.FS` is a read-only interface, and atomic writes are outside its scope.
- Atomic symlink replacement. Use [google/renameio](https://github.com/google/renameio) for that.
- Detecting a cross-mount temp directory. Temp files are always created in the target's own directory, on the same mount, which is the only correct place for an atomic rename.

## Documentation

- [Choosing a function](docs/choosing-a-function.md) maps each job to a call and lists every option.
- [Durability and errors](docs/durability.md) covers the write sequence, `Result` and every error value.
- [File modes and private directories](docs/file-modes.md) covers `WithMode`, `EnforceMode` and `EnsurePrivateDir`.
- [Confinement, symlinks and special files](docs/confinement.md) covers `*os.Root` use and shared trees.
- [Orphaned temp files and the writability probe](docs/temp-files.md) covers sweeps and `ProbeWritable`.
- [Detecting a changed file](docs/reload-staleness.md) covers `FileIdentity` for caches.

## Credits

The incremental `PendingFile` API, a staged file that is either committed into place or cleaned up, follows [google/renameio](https://github.com/google/renameio).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

Apache-2.0. See [LICENSE](LICENSE).
