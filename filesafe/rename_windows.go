package filesafe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Win32 codes for a replace refused by another handle; syscall names only
// ERROR_ACCESS_DENIED (https://learn.microsoft.com/windows/win32/debug/system-error-codes--0-499-).
const (
	errSharingViolation syscall.Errno = 32
	errLockViolation    syscall.Errno = 33
)

// renameRetryDelays are the waits between replace attempts: 7 attempts over
// 630 ms. Scanners and indexers usually release a file within tens of
// milliseconds, and a write that blocks its caller much past half a second
// is worse than a clear ErrFileBusy.
var renameRetryDelays = []time.Duration{
	10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond,
	80 * time.Millisecond, 160 * time.Millisecond, 320 * time.Millisecond,
}

// rootRename is the replace call; a package var so tests can count attempts.
var rootRename = (*os.Root).Rename

// nonDurableVolumes holds the serial of each volume already logged as unable
// to report a durable write, so each is logged once per process.
var nonDurableVolumes sync.Map

var (
	procGetVolumeInformationByHandleW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetVolumeInformationByHandleW")
	procNtQueryVolumeInformationFile  = syscall.NewLazyDLL("ntdll.dll").NewProc("NtQueryVolumeInformationFile")
)

// FileFsDeviceInformation and its FILE_REMOTE_DEVICE characteristic, set for
// a volume on a remote file system such as SMB
// (https://learn.microsoft.com/openspecs/windows_protocols/ms-fscc/616b66d5-b335-4e1c-8f87-b4a55e8d3e4a).
const (
	fileFsDeviceInformation = 4
	fileRemoteDevice        = 0x10
)

// closeTempBeforeRename keeps the temp open: commitTempInRoot flushes the
// published file through this handle, and the root's handle shares DELETE,
// so the rename's own open of the temp is not refused.
func closeTempBeforeRename(*os.File) error { return nil }

// commitTempInRoot replaces name with the synced, still-open temp through
// root.Rename, retrying a busy refusal, then flushes the published file
// through tmp and closes it. A failed replace closes and removes the temp
// and returns a PhaseRename WriteError. Windows has no directory fsync: on
// NTFS the flush after the rename persists it ([MS-FSA] 2.1.5.7,
// https://learn.microsoft.com/openspecs/windows_protocols/ms-fsa/).
func commitTempInRoot(ctx context.Context, root *os.Root, tmp *os.File, tmpName, name, _ string, c *cfg) (durable bool, err error) {
	if rnErr := renameWithRetry(ctx, root, tmpName, name); rnErr != nil {
		if clErr := tmp.Close(); clErr != nil {
			c.logger.Debug("filesafe: temp file close failed", "path", tmpName, "error", clErr)
		}
		removeTempInRoot(root, tmpName, c.logger)
		return false, &WriteError{Phase: PhaseRename, Err: rnErr}
	}
	durable = publishDurable(root, tmp, name, c)
	if clErr := tmp.Close(); clErr != nil {
		c.logger.Debug("filesafe: published file close failed",
			"root", root.Name(), "path", name, "error", clErr)
	}
	return durable, nil
}

// renameWithRetry retries a replace refused by another handle, waiting
// renameRetryDelays between attempts, and wraps the last refusal in
// ErrFileBusy once they run out or ctx ends. Any other error returns at once.
func renameWithRetry(ctx context.Context, root *os.Root, tmpName, name string) error {
	for attempt := 0; ; attempt++ {
		err := rootRename(root, tmpName, name)
		if err == nil || !replaceBusy(err) {
			return err
		}
		if attempt == len(renameRetryDelays) {
			return fmt.Errorf("%w: %d attempts: %w", ErrFileBusy, attempt+1, err)
		}
		t := time.NewTimer(renameRetryDelays[attempt])
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("%w: %w: %w", ErrFileBusy, ctx.Err(), err)
		case <-t.C:
		}
	}
}

// replaceBusy reports a refusal another handle can cause. Access denied is
// one: Go's fallback rename after a refused POSIX rename fails with it while
// the target has an open handle ([MS-FSA] 2.1.5.14.11).
func replaceBusy(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case errSharingViolation, errLockViolation, syscall.ERROR_ACCESS_DENIED:
		return true
	}
	return false
}

// publishDurable flushes the just-published file through f and reports
// whether the write is durable. A failed flush or volume query is logged at
// Warn; a volume durableOn refuses is logged at Info once per volume, since
// every write there reports the same.
func publishDurable(root *os.Root, f *os.File, name string, c *cfg) bool {
	if err := f.Sync(); err != nil {
		c.logger.Warn("filesafe: flush after the rename failed; write is not durable",
			"root", root.Name(), "path", name, "error", err)
		return false
	}
	fsName, serial, err := volumeFilesystem(f)
	if err != nil {
		c.logger.Warn("filesafe: filesystem query failed; write is not durable",
			"root", root.Name(), "path", name, "error", err)
		return false
	}
	remote, err := volumeRemote(f)
	if err != nil {
		c.logger.Warn("filesafe: device query failed; write is not durable",
			"root", root.Name(), "path", name, "error", err)
		return false
	}
	if durableOn(fsName, remote) {
		return true
	}
	if _, logged := nonDurableVolumes.LoadOrStore(serial, struct{}{}); !logged {
		c.logger.Info("filesafe: writes on this volume report Durable=false",
			"root", root.Name(), "filesystem", fsName, "remote", remote)
	}
	return false
}

// durableOn reports whether a file flush persists the file's rename. Only
// local NTFS documents that it does ([MS-FSA] 2.1.5.7 and its note 80). A
// remote volume reports its server's filesystem name, often NTFS (Samba's
// default fstype), whatever the server's flush persists, so it reports
// false, as do ReFS, FAT and exFAT.
func durableOn(fsName string, remote bool) bool { return !remote && fsName == "NTFS" }

// volumeFilesystem returns the filesystem name and the volume serial number
// of the volume holding f
// (https://learn.microsoft.com/windows/win32/api/fileapi/nf-fileapi-getvolumeinformationbyhandlew).
func volumeFilesystem(f *os.File) (fsName string, serial uint32, err error) {
	if err := procGetVolumeInformationByHandleW.Find(); err != nil {
		return "", 0, err
	}
	var buf [syscall.MAX_PATH + 1]uint16
	ok, _, callErr := procGetVolumeInformationByHandleW.Call(f.Fd(), 0, 0,
		uintptr(unsafe.Pointer(&serial)), 0, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ok == 0 {
		return "", 0, callErr
	}
	return syscall.UTF16ToString(buf[:]), serial, nil
}

// volumeRemote reports whether the volume holding f is on a remote file
// system, a mapped drive included
// (https://learn.microsoft.com/windows-hardware/drivers/ddi/ntifs/nf-ntifs-ntqueryvolumeinformationfile).
func volumeRemote(f *os.File) (bool, error) {
	if err := procNtQueryVolumeInformationFile.Find(); err != nil {
		return false, err
	}
	var iosb struct{ status, information uintptr }
	var info struct{ deviceType, characteristics uint32 }
	r, _, _ := procNtQueryVolumeInformationFile.Call(f.Fd(),
		uintptr(unsafe.Pointer(&iosb)), uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info), fileFsDeviceInformation)
	// An NTSTATUS is 32 bits; the register's upper half is not part of it.
	if status := r & 0xffffffff; status != 0 {
		return false, fmt.Errorf("NtQueryVolumeInformationFile(FileFsDeviceInformation): NTSTATUS %#x", status)
	}
	return info.characteristics&fileRemoteDevice != 0, nil
}

// refuseReadOnlyTarget refuses a target with the read-only attribute (mode
// without 0o200 on Windows), which a replace can never overwrite
// (https://learn.microsoft.com/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information),
// so it fails before a temp exists instead of after the whole retry bound.
func refuseReadOnlyTarget(name string, mode fs.FileMode) error {
	if mode.Perm()&0o200 == 0 {
		return fmt.Errorf("filesafe: target %q has the read-only attribute: %w", name, fs.ErrPermission)
	}
	return nil
}

// syncCreatedDirParent is a no-op on Windows, which has no directory sync:
// FlushFileBuffers refuses the read-only handle Go opens on a directory. The
// commit's flush of the published file stands for it on NTFS, and the volume
// check reports any other filesystem's write not durable.
func syncCreatedDirParent(*os.Root, string) error { return nil }
