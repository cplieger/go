package ownersock

import (
	"fmt"
	"net"
	"syscall"
)

// socketPeerUID reads the connecting process's effective uid, which
// SO_PEERCRED (unix(7)) records when the peer called connect.
func socketPeerUID(c *net.UnixConn) (int, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	var (
		cred    *syscall.Ucred
		credErr error
	)
	if err := rc.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return -1, err
	}
	if credErr != nil {
		return -1, fmt.Errorf("SO_PEERCRED: %w", credErr)
	}
	return int(cred.Uid), nil
}
