package filesafe

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProbePOSIXRename checks, on the system temp volume and on the runner's
// work volume when RUNNER_TEMP names one, that a write replaces a target
// another handle holds open with FILE_SHARE_DELETE. Only a rename with
// FILE_RENAME_POSIX_SEMANTICS may do that; the legacy rename Go falls back to
// refuses a target with any open handle ([MS-FSA] 2.1.5.14.11), so a refusal
// here means the volume does not honour POSIX semantics
// (https://learn.microsoft.com/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information).
// It swaps rootRename, so it does not run in parallel.
func TestProbePOSIXRename(t *testing.T) {
	dirs := map[string]string{"temp": t.TempDir()}
	if rt := os.Getenv("RUNNER_TEMP"); rt != "" {
		d, err := os.MkdirTemp(rt, "filesafe-probe-")
		if err != nil {
			t.Fatalf("MkdirTemp(%q) = %v", rt, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(d) })
		dirs["runner_temp"] = d
	}
	for label, dir := range dirs {
		t.Run(label, func(t *testing.T) {
			fsName, flags := volumeInfo(t, dir)
			t.Logf("probe: dir %s volume %q filesystem %s flags %#x POSIX_UNLINK_RENAME %t",
				dir, filepath.VolumeName(dir), fsName, flags, flags&fileSupportsPOSIXUnlinkRename != 0)

			target := filepath.Join(dir, "target")
			if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatalf("OpenRoot: %v", err)
			}
			defer root.Close()
			held, err := root.Open("target")
			if err != nil {
				t.Fatalf("root.Open(target): %v", err)
			}
			defer held.Close()

			spy := spyRenames(t)
			_, err = WriteFile(t.Context(), target, []byte("new"))
			t.Logf("probe: replace attempts %d, errors %v", len(spy.errs), spy.errs)
			if err != nil {
				t.Fatalf("WriteFile over a share-delete handle = %v, want nil: the volume refused POSIX rename semantics", err)
			}
			if got := readHandle(t, held); got != "old" {
				t.Errorf("held handle reads %q after the replace, want %q (existing handles keep the replaced file)", got, "old")
			}
			assertContent(t, target, "new")
			t.Logf("probe: PASS on %s (%s)", filepath.VolumeName(dir), fsName)
		})
	}
}
