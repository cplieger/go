# Leases

This page explains how a filesafe lease lets one process at a time work on a file, what it refuses and where it holds, for a developer coordinating processes that share files on one host.

## What a lease is

A lease is a `flock(2)` lock on a lock file, held until `Close`. `AcquireLease` waits for it and `TryLease` gives up at once with `ErrLeaseHeld`. Both take an exclusive lease, which one holder has at a time. `AcquireSharedLease` and `TrySharedLease` take a shared lease, which any number of readers hold together while no exclusive holder has it.

`AcquireLease` and `AcquireSharedLease` retry every 50 ms until the lease is free or the context ends. A released lease is seen within one retry. A context already done returns its error and opens nothing. A context that ends while another holder has the lease returns `ErrLeaseHeld`, wrapping the context's error.

The kernel ties the lock to the open file, so it drops it when the holder closes it or exits, a crash included. Two leases on one path in one process exclude each other as two processes do, so a process takes each lease once and shares it. Keep the `*Lease` reachable until `Close`: one the garbage collector reclaims closes its descriptor, which releases the lease.

## The lock file

Name the lock file after what it guards, `<path>.lock`, in the same directory. The exclusive forms create it at 0600, owned by the process's user, when it is missing. They create no directory, so a missing parent is an error matching `fs.ErrNotExist`. The shared forms never create it: a missing file is an error matching `fs.ErrNotExist`, which a reader can take to mean no exclusive holder has run yet.

Never remove a lock file, even after `Close`. A holder that waited on the removed file would lock it while a newcomer locks the new one, and both would believe they hold the lease. `Covers` tells a holder whether the file it locked is still at its path.

Before it locks, every form refuses, from the open descriptor:

- a symlink at the path, with `ErrSymlinkTarget`, without touching the file it points to;
- a directory, FIFO, socket or device, with `ErrNotRegular` (a `*NotRegularError`);
- a file another user owns, with `ErrNotOwned`, since its owner could swap it;
- a file with group or other access, with `ErrModeTooOpen`, since another user who can open it could hold the lease against you;
- a created file whose 0600 the filesystem did not store, with `ErrModeNotStored`.

Only the final component is checked. The lock file's directory should be one only its users can write, such as the directory the guarded file lives in.

A lock file replaced between its open and its lock is `ErrRaced` from `TryLease` and `TrySharedLease`, and a retry from the waiting forms.

## The lease holds no data

A lease gives no access to its lock file's content. Keep your data in its own file beside the lock file, read it with `ReadBounded` and write it with `WriteFile` while you hold the lease. A lock file that also held data would be rewritten in place, and a power loss could leave it torn, which `WriteFile`'s temp file and rename never do. [Choosing a function](choosing-a-function.md#updating-a-file-one-process-at-a-time) shows the read-modify-write.

## NFS, SMB and other hosts

The exclusive forms open the lock file for writing, because NFS and SMB emulate `flock` with byte-range locks, and an exclusive one needs a file opened for writing ([flock(2)](https://man7.org/linux/man-pages/man2/flock.2.html), "NFS details" and "CIFS details"). A directory cannot be opened for writing, which is why a lease never locks one. The shared forms open it read-only, which a shared byte-range lock allows. Nobody reads or writes the lock file, so SMB's mandatory locks touch nothing.

A lease is meant for processes on one host. Across hosts it excludes only where the mount carries locks to the server. These mounts keep it on one client, so two hosts would not exclude each other:

- SMB before Linux 5.5, which does not send `flock` to the server;
- NFS mounted with `local_lock=flock` or `local_lock=all`, and NFS mounted with `nolock` ([nfs(5)](https://man7.org/linux/man-pages/man5/nfs.5.html), `local_lock` and `lock / nolock`).

## Platforms

Leases build for Linux, macOS, FreeBSD, OpenBSD, NetBSD, DragonFly and illumos, and are tested on Linux. On Windows every form returns an error matching both `ErrModeUnsupported` and `errors.ErrUnsupported`. On AIX and Solaris, where Go's `syscall` has no `Flock`, every form returns an error matching `errors.ErrUnsupported`.
