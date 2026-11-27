package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// A runner process registers itself in a registry directory (runners/ in lucy's runtime
// directory) while it runs, so `lucy runner drain` finds it: one file <id>.pid holding its pid and
// org, locked for the process's life. Only a locked file names a live runner: a runner that died
// released its lock, so its pid, which the OS may have given another process since, is never
// signalled. The registry and its parent must be real directories owned by this user and closed to
// others, and only entries this user owns are read, so a registry planted in a shared /tmp by
// another user never names a process to signal.

// Registration is a live runner process.
type Registration struct {
	ID  string
	Org string
	PID int
}

// ErrRegistered refuses a runner whose id a live runner already registered.
var ErrRegistered = errors.New("runner: a live runner already uses this id")

// Register records this process as runner id of org in dir (created 0700; refused when it or its
// parent is a symlink, another user's, or open to others) until release, which removes the entry. A second live runner with
// the same id is refused (ErrRegistered).
func Register(dir, id, org string) (release func() error, err error) {
	if err := CheckRunnerID(id); err != nil {
		return nil, err
	}
	if err := checkOrg(org); err != nil {
		return nil, err
	}
	if err := prepareRegistry(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".pid")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- a checked runner id in the checked 0700 registry
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close() // the lock error is what matters
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %q", ErrRegistered, id)
		}
		return nil, fmt.Errorf("runner: lock %s: %w", path, err)
	}
	if err := writeRegistration(f, os.Getpid(), org); err != nil {
		_ = f.Close() // the write error is what matters
		return nil, err
	}
	return func() error {
		// Removed while still locked, so no other process's entry of the same id is removed.
		return errors.Join(os.Remove(path), f.Close())
	}, nil
}

func writeRegistration(f *os.File, pid int, org string) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(pid)+" "+org+"\n"), 0)
	return err
}

// Registered returns the live runners registered in dir, by id. A missing dir has none; a dir that
// fails Register's checks is refused.
func Registered(dir string) ([]Registration, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("runner: the registry %q is not absolute", dir)
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err := checkPrivateDirs(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Registration
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".pid")
		if !ok || !e.Type().IsRegular() || CheckRunnerID(id) != nil {
			continue
		}
		r, live, err := readRegistration(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if live {
			r.ID = id
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Registration) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// readRegistration reads one entry and reports whether a live process holds its lock.
func readRegistration(path string) (Registration, bool, error) {
	f, err := os.Open(path) // #nosec G304 -- an entry of the registry directory, by a checked runner id
	if errors.Is(err, fs.ErrNotExist) {
		return Registration{}, false, nil // its runner removed it meanwhile
	}
	if err != nil {
		return Registration{}, false, err
	}
	defer f.Close() //nolint:errcheck // read-only
	if fi, err := f.Stat(); err != nil {
		return Registration{}, false, err
	} else if !fi.Mode().IsRegular() || !ownedByMe(fi) {
		return Registration{}, false, nil
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err == nil {
		return Registration{}, false, nil // nobody holds it: its runner is gone (closing unlocks)
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		return Registration{}, false, err
	}
	b := make([]byte, 512)
	n, err := f.Read(b)
	if err != nil && n == 0 {
		return Registration{}, false, nil // locked but not yet written: starting
	}
	pidText, org, _ := strings.Cut(strings.TrimSpace(string(b[:n])), " ")
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 1 {
		return Registration{}, false, nil
	}
	return Registration{Org: org, PID: pid}, true, nil
}

func prepareRegistry(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("runner: the registry %q is not absolute", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return checkPrivateDirs(dir)
}

// checkPrivateDirs refuses dir unless it and its parent are real directories (not symlinks) owned
// by this user with no group or other permissions.
func checkPrivateDirs(dir string) error {
	for _, d := range []string{filepath.Dir(dir), dir} {
		fi, err := os.Lstat(d)
		if err != nil {
			return err
		}
		if !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 || !ownedByMe(fi) {
			return fmt.Errorf("runner: %s must be a directory of mode 0700 owned by uid %d, not a symlink (it is %v)", d, os.Geteuid(), fi.Mode())
		}
	}
	return nil
}

func ownedByMe(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
