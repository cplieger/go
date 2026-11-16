//go:build !linux

package ownersock

import (
	"errors"
	"net"
)

// socketPeerUID refuses every peer: the check reads Linux's SO_PEERCRED.
func socketPeerUID(*net.UnixConn) (int, error) {
	return -1, errors.New("peer credentials are read on Linux only")
}
