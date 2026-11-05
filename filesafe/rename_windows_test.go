package filesafe

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

// TestDurableOn pins that only NTFS, whose file flush persists the file's
// directory entry ([MS-FSA] 2.1.5.7 and its note 80), reports a durable write.
func TestDurableOn(t *testing.T) {
	t.Parallel()
	for fsName, want := range map[string]bool{
		"NTFS": true, "ReFS": false, "FAT": false, "FAT32": false, "exFAT": false, "": false,
	} {
		if got := durableOn(fsName); got != want {
			t.Errorf("durableOn(%q) = %t, want %t", fsName, got, want)
		}
	}
}

// TestReplaceBusy pins which replace refusals are retried: the sharing, lock
// and access-denied errors another handle causes, also inside the
// *os.LinkError os.Root.Rename returns; nothing else.
func TestReplaceBusy(t *testing.T) {
	t.Parallel()
	link := func(err error) error { return &os.LinkError{Op: "renameat", Old: "a", New: "b", Err: err} }
	cases := map[string]struct {
		err  error
		want bool
	}{
		"sharing_violation":  {link(syscall.Errno(32)), true},
		"lock_violation":     {link(syscall.Errno(33)), true},
		"access_denied":      {link(syscall.ERROR_ACCESS_DENIED), true},
		"bare_errno":         {syscall.Errno(32), true},
		"file_not_found":     {link(syscall.ERROR_FILE_NOT_FOUND), false},
		"not_same_device":    {link(syscall.Errno(17)), false},
		"not_an_errno":       {link(errors.New("other")), false},
		"wrapped_by_fmt":     {fmt.Errorf("x: %w", link(syscall.Errno(33))), true},
		"nil_error_is_false": {nil, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := replaceBusy(tc.err); got != tc.want {
				t.Errorf("replaceBusy(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}
