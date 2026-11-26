//go:build darwin

package server

import "golang.org/x/sys/unix"

// peerCred reads LOCAL_PEERCRED (what getpeereid uses) for the uid and gid, and LOCAL_PEERPID for
// the pid.
func peerCred(fd int) (PeerCred, error) {
	x, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return PeerCred{}, err
	}
	cred := PeerCred{UID: int(x.Uid)}
	if x.Ngroups > 0 {
		cred.GID = int(x.Groups[0])
	}
	if pid, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
		cred.PID = pid
	}
	return cred, nil
}
