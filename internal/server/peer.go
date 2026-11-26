package server

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/ar4mirez/lucy/internal/api"
)

// ErrNoPeerCred is returned when the OS does not report a unix socket peer's credentials.
var ErrNoPeerCred = errors.New("server: peer credentials unavailable")

// PeerCred is what the kernel reports about the process on the other end of a unix socket.
type PeerCred struct {
	UID, GID, PID int // PID is 0 where the OS does not report it
}

// PeerListener wraps a unix socket listener. It accepts a connection only if the kernel reports the
// peer runs as AllowUID (SO_PEERCRED on Linux, LOCAL_PEERCRED on macOS); every other connection is
// closed before a byte is read (LUCY-API-003). Accepted connections carry the caller's identity, and
// at most MaxConns are open at once: past that, Accept waits for one to close.
type PeerListener struct {
	net.Listener
	AllowUID int
	// OnRefuse is told about every refused connection; cred is zero when err is set.
	OnRefuse func(cred PeerCred, err error)

	refused   atomic.Int64
	slots     chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// DefaultMaxConns bounds concurrent connections on the socket.
const DefaultMaxConns = 64

// NewPeerListener returns a PeerListener over l that admits only allowUID.
func NewPeerListener(l net.Listener, allowUID, maxConns int) *PeerListener {
	if maxConns <= 0 {
		maxConns = DefaultMaxConns
	}
	return &PeerListener{Listener: l, AllowUID: allowUID, slots: make(chan struct{}, maxConns), done: make(chan struct{})}
}

// Close closes the listener and wakes an Accept waiting for a free slot. It is safe to call twice.
func (l *PeerListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.done)
		l.closeErr = l.Listener.Close()
	})
	return l.closeErr
}

// Refused is the number of connections refused so far.
func (l *PeerListener) Refused() int64 { return l.refused.Load() }

// Accept returns the next connection from the allowed user.
func (l *PeerListener) Accept() (net.Conn, error) {
	for {
		select {
		case l.slots <- struct{}{}:
		case <-l.done:
			return nil, net.ErrClosed
		}
		c, err := l.Listener.Accept()
		if err != nil {
			<-l.slots
			return nil, err
		}
		cred, err := peerCredOf(c)
		if err == nil && cred.UID != l.AllowUID {
			err = errors.New("server: peer runs as another user")
		}
		if err != nil {
			c.Close() //nolint:errcheck // refusing; nothing to report to the peer
			<-l.slots
			l.refused.Add(1)
			if l.OnRefuse != nil {
				l.OnRefuse(cred, err)
			}
			continue
		}
		caller := api.Caller{Transport: "unix", UID: cred.UID, GID: cred.GID, PID: cred.PID, Device: "local"}
		return &callerConn{Conn: c, caller: caller, release: func() { <-l.slots }}, nil
	}
}

func peerCredOf(c net.Conn) (PeerCred, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return PeerCred{}, ErrNoPeerCred
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return PeerCred{}, err
	}
	var cred PeerCred
	var credErr error
	if err := raw.Control(func(fd uintptr) { cred, credErr = peerCred(int(fd)) }); err != nil {
		return PeerCred{}, err
	}
	return cred, credErr
}

// callerConn is an accepted connection that knows who is on the other end.
type callerConn struct {
	net.Conn
	caller  api.Caller
	release func()
	once    sync.Once
}

func (c *callerConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
