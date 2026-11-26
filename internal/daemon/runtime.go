package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
)

// Errors about the runtime directory and socket.
var (
	ErrInsecureRuntimeDir = errors.New("daemon: insecure runtime directory")
	ErrAlreadyRunning     = errors.New("daemon: another lucy daemon is running")
	ErrSocketPathTooLong  = errors.New("daemon: socket path too long")
)

// SocketFile is the socket's name inside the runtime directory.
const SocketFile = "lucy.sock"

const lockFile = "lucy.lock"

// maxSocketPath is the longest unix socket path the OS accepts (sun_path minus the NUL).
func maxSocketPath() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}

// RuntimeDir returns lucy's runtime directory: $XDG_RUNTIME_DIR/lucy, else lucy-<uid> in the
// system temp dir ($TMPDIR, which is per user on macOS). Either way it is created 0700 and refused
// if another user owns it or could read it, so a directory planted in a shared /tmp is refused
// rather than used.
func RuntimeDir() (string, error) {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		if !filepath.IsAbs(d) {
			return "", fmt.Errorf("%w: XDG_RUNTIME_DIR %q is not absolute", ErrInsecureRuntimeDir, d)
		}
		return filepath.Join(d, "lucy"), nil
	}
	return filepath.Join(os.TempDir(), "lucy-"+strconv.Itoa(os.Getuid())), nil
}

// DefaultSocketPath returns the socket path inside RuntimeDir.
func DefaultSocketPath() (string, error) {
	d, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, SocketFile), nil
}

// prepareRuntimeDir creates dir 0700 if needed and refuses it unless it is a real directory (not a
// symlink) owned by this user with no group or other permissions.
func prepareRuntimeDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	switch {
	case !fi.IsDir():
		return fmt.Errorf("%w: %s is not a directory (a symlink is refused)", ErrInsecureRuntimeDir, dir)
	case fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%w: %s has mode %04o; want 0700 (chmod 0700 %s)", ErrInsecureRuntimeDir, dir, fi.Mode().Perm(), dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: %s is owned by uid %d, not %d", ErrInsecureRuntimeDir, dir, st.Uid, os.Geteuid())
	}
	return nil
}

// lock takes an exclusive, non-blocking flock on the runtime dir's lock file, so one daemon owns
// the socket. The lock is released when the returned file is closed or the process exits.
func lock(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close() //nolint:errcheck // the lock error is what matters
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (lock %s is held)", ErrAlreadyRunning, f.Name())
		}
		return nil, fmt.Errorf("daemon: lock %s: %w", f.Name(), err)
	}
	return f, nil
}

// removeStaleSocket removes a socket left by a daemon that died. It runs only while holding the
// lock, so no live daemon owns it, and it removes only a socket file, never anything else.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("%w: %s exists and is not a socket; move it away", ErrInsecureRuntimeDir, path)
	}
	return os.Remove(path)
}
