package filesafe

import (
	"os"
	"path/filepath"
	"testing"
)

// A root-owned 0644 lock file refuses the exclusive forms' read-write open,
// so they read its owner by lstat, and opens read-only for the shared forms,
// which read it from the handle; the parent then hands it to nobody, which
// must not change a refusal already returned.
func TestNotOwnedError_CarriesBothUidsEachLeaseFormCompared(t *testing.T) {
	setup := func(t *testing.T, base string) {
		path := filepath.Join(base, "foreign.lock")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	between := func(base string) {
		if err := os.Chown(filepath.Join(base, "foreign.lock"), nobodyUID, nobodyUID); err != nil {
			t.Errorf("chown foreign.lock to nobody: %v", err)
		}
	}
	base, ok := asNobody(t, setup, between)
	if !ok {
		return
	}
	// Every Linux keeps /etc/passwd root-owned at 0644.
	path := "/etc/passwd"
	if base != "" {
		path = filepath.Join(base, "foreign.lock")
	}
	errs := make([]error, len(allLeaseForms))
	msgs := make([]string, len(allLeaseForms))
	for i, form := range allLeaseForms {
		l, err := form.take(t.Context(), path)
		closeIfLease(l)
		assertNotOwned(t, form.name, err, path)
		errs[i], msgs[i] = err, err.Error()
	}
	if base == "" {
		return
	}
	awaitParent(t)
	for i, form := range allLeaseForms {
		assertNotOwned(t, form.name+" after the chown", errs[i], path)
		if errs[i].Error() != msgs[i] {
			t.Errorf("%s message after the chown = %q, want %q unchanged", form.name, errs[i].Error(), msgs[i])
		}
	}
}
