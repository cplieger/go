package ownersock

import "testing"

func TestListen_aPathOf107BytesBinds(t *testing.T) {
	_, path := pathOfLen(t, 107)
	listens(t, path, nil)
	dials(t, path)
}
