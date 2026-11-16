package ownersock

import (
	"os"
	"strconv"
	"testing"
)

// SO_PEERCRED (unix(7)) reports the connecting process's credentials, which
// for this test's own client are this process's.
func TestAccept_servesItsOwnUser(t *testing.T) {
	_, path := sockPaths(t)
	s := serve(t, listens(t, path, nil))
	body, err := get(t, path)
	if err != nil {
		t.Fatalf("a same-uid request: %v", err)
	}
	if want := "uid=" + strconv.Itoa(os.Geteuid()) + " ok=true"; body != want {
		t.Errorf("the handler saw %q, want %q", body, want)
	}
	if n := s.hits.Load(); n != 1 {
		t.Errorf("%d requests reached the handler, want 1", n)
	}
}
