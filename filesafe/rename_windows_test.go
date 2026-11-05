package filesafe

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

// TestDurableOn pins that only local NTFS, whose file flush persists the
// file's directory entry ([MS-FSA] 2.1.5.7 and its note 80), reports a
// durable write; a remote volume never does, whatever name it reports.
func TestDurableOn(t *testing.T) {
	t.Parallel()
	type volume struct {
		fsName string
		remote bool
	}
	for v, want := range map[volume]bool{
		{"NTFS", false}: true, {"ReFS", false}: false, {"FAT", false}: false,
		{"FAT32", false}: false, {"exFAT", false}: false, {"", false}: false,
		{"NTFS", true}: false, {"ReFS", true}: false,
	} {
		if got := durableOn(v.fsName, v.remote); got != want {
			t.Errorf("durableOn(%q, remote=%t) = %t, want %t", v.fsName, v.remote, got, want)
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
