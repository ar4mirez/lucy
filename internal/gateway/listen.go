package gateway

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ListenUnix listens on a Unix socket at path for the org's workers: its directory is created 0700
// and must be a real directory, not a symlink, with no group or other permissions; the socket is
// 0600. A socket left by a gateway that is gone is replaced; a file that is not a socket, or a
// socket that still answers, is refused.
func ListenUnix(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("gateway: the socket must be a clean absolute path, not %q", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("gateway: create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 || fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("gateway: %s must be a directory only its owner can open (mode %v)", dir, fi.Mode())
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&fs.ModeSocket == 0 {
			return nil, fmt.Errorf("gateway: %s exists and is not a socket", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close() // only a probe
			return nil, fmt.Errorf("gateway: another gateway is listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("gateway: listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, ln.Close())
	}
	return ln, nil
}
