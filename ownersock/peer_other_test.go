//go:build !linux && !windows

package ownersock

import (
	"errors"
	"testing"
)

func TestAccept_refusesEveryConnectionOffLinux(t *testing.T) {
	_, path := sockPaths(t)
	s := serve(t, listens(t, path, nil))
	refusedBeforeAnyByte(t, path)
	_, err := get(t, path)
	if oerr, ok := errors.AsType[*Error](err); !ok || oerr.Code != CodePeerRefused {
		t.Errorf("a same-uid request off Linux: %v, want %s", err, CodePeerRefused)
	}
	if n := s.hits.Load(); n != 0 {
		t.Errorf("%d requests reached the handler, want 0", n)
	}
}
