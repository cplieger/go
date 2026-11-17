package health

// [DIAG-7a3c] temporary CI probe for the windows go-os diagnosis; reverted
// before the step ends. Every subtest fails on purpose so --log-failed shows
// its values.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func diagErr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%q (%T) notexist=%v perm=%v", err.Error(), errors.Unwrap(err), errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrPermission))
}

func TestDIAG7a3c(t *testing.T) {
	t.Run("overlong", func(t *testing.T) {
		p := overlongName(t)
		_, err := os.Lstat(p)
		_, reason := probeCheck(p)
		t.Errorf("[DIAG-7a3c] overlong lstat=%s reason-equals=%v", diagErr(err), err != nil && reason == "unhealthy: marker stat failed: "+err.Error())
	})
	t.Run("readonly", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "app.healthy")
		if err := os.WriteFile(p, nil, 0o444); err != nil {
			t.Fatal(err)
		}
		_, openErr := os.OpenFile(p, os.O_WRONLY, 0)
		l, buf := jsonLog()
		st := NewStatus(WithLogger(l), WithMarker(p))
		got := withMsg(t, buf, msgMarkerDegraded)
		logged := ""
		if len(got) == 1 {
			logged = fmt.Sprint(got[0]["error"])
		}
		old := time.Now().Add(-2 * time.Hour)
		chtErr := os.Chtimes(p, old, old)
		info, _ := os.Stat(p)
		s1 := inspect(p, WithMaxAge(time.Hour)).state
		chmErr := os.Chmod(p, 0o644)
		s2 := inspect(p, WithMaxAge(time.Hour)).state
		t.Errorf("[DIAG-7a3c] readonly open=%s degraded=%v lines=%d logged-equals-open=%v chtimes=%s modtime-old=%v inspect-ro=%v(degraded=%v) chmod=%s inspect-rw=%v(stale=%v)",
			diagErr(openErr), st.mirror.degraded, len(got), openErr != nil && logged == openErr.Error(), diagErr(chtErr),
			info != nil && info.ModTime().Before(old.Add(time.Minute)), s1, s1 == markerDegraded, diagErr(chmErr), s2, s2 == markerStale)
	})
	t.Run("folder_is_a_file", func(t *testing.T) {
		p := markerIn(t)
		blockFolder(t, p)
		t.Errorf("[DIAG-7a3c] folder-file write=%s remove=%s osremove=%s", diagErr(writeMarker(p)), diagErr(removeMarker(p)), diagErr(os.Remove(p)))
	})
	t.Run("nonempty_dir_at_marker", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "app.healthy")
		block := func() {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "keep"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		block()
		_, osOpen := os.OpenFile(p, os.O_WRONLY, 0)
		w := writeMarker(p)
		osRm := os.Remove(p)
		r := removeMarker(p)
		t.Errorf("[DIAG-7a3c] dir write=%s osopen-equal=%v remove=%s osremove-equal=%v",
			diagErr(w), osOpen != nil && w != nil && osOpen.Error() == w.Error(), diagErr(r), osRm != nil && r != nil && osRm.Error() == r.Error())
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}

		l, buf := jsonLog()
		st := NewStatus(WithLogger(l), WithMarker(p))
		block()
		for _, ok := range []bool{true, true, false, true, false, false} {
			st.Set(ok)
		}
		nc, nr := len(withMsg(t, buf, msgCreateFailed)), len(withMsg(t, buf, msgRemoveFailed))
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}

		l2, buf2 := jsonLog()
		p2 := filepath.Join(t.TempDir(), "app.healthy")
		st2 := NewStatus(WithLogger(l2), WithMarker(p2))
		st2.Set(false)
		if err := os.Mkdir(p2, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p2, "keep"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		st2.Set(false)
		failed := strings.Count(buf2.String(), msgRemoveFailed)
		if err := os.RemoveAll(p2); err != nil {
			t.Fatal(err)
		}
		buf2.Reset()
		st2.Set(false)
		rec := records(t, buf2)
		t.Errorf("[DIAG-7a3c] dir streak create-lines=%d remove-lines=%d; set-false fail-lines=%d recovery=%v", nc, nr, failed, rec)
	})
}
