//go:build linux

package server

import "golang.org/x/sys/unix"

// peerCred reads SO_PEERCRED: the uid, gid and pid of the process that connected.
func peerCred(fd int) (PeerCred, error) {
	u, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return PeerCred{}, err
	}
	return PeerCred{UID: int(u.Uid), GID: int(u.Gid), PID: int(u.Pid)}, nil
}
