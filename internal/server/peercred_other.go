//go:build !linux && !darwin

package server

// peerCred refuses every peer on platforms lucy does not support: without the kernel's word on
// who connected, the socket admits no one.
func peerCred(int) (PeerCred, error) { return PeerCred{}, ErrNoPeerCred }
