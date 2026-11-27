package runner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Only a runner that holds its entry's lock is live: a second runner with its id is refused, an
// entry nobody holds (its runner died, and its pid may be another process's now) is never
// listed, and releasing removes the entry.
func TestRegistry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runners")
	release, err := Register(dir, "acme-host-1", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("registry %v, %v; want a 0700 directory", fi, err)
	}
	if _, err := Register(dir, "acme-host-1", "acme"); !errors.Is(err, ErrRegistered) {
		t.Fatalf("a second runner with the id: %v", err)
	}
	// A dead runner's entry: written, never locked.
	if err := os.WriteFile(filepath.Join(dir, "gone.pid"), []byte("4242 acme\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err := Registered(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0] != (Registration{ID: "acme-host-1", Org: "acme", PID: os.Getpid()}) {
		t.Fatalf("live runners %+v", live)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "acme-host-1.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the released entry: %v", err)
	}
	if live, err := Registered(dir); err != nil || len(live) != 0 {
		t.Fatalf("after release: %+v, %v", live, err)
	}
	if again, err := Register(dir, "acme-host-1", "acme"); err != nil {
		t.Fatalf("the id once released: %v", err)
	} else if err := again(); err != nil {
		t.Fatal(err)
	}
	if live, err := Registered(filepath.Join(dir, "missing")); err != nil || live != nil {
		t.Fatalf("a missing registry: %+v, %v", live, err)
	}
}

func TestRegisterRefuses(t *testing.T) {
	open := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil { // past the umask
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ dir, id, org string }{
		"a registry open to others": {open, "r1", "acme"},
		"a symlinked registry":      {link, "r1", "acme"},
		"a relative registry":       {"runners", "r1", "acme"},
		"a bad id":                  {filepath.Join(t.TempDir(), "r"), "../x", "acme"},
		"a bad org":                 {filepath.Join(t.TempDir(), "r"), "r1", "a/b"},
	} {
		if release, err := Register(c.dir, c.id, c.org); err == nil {
			_ = release()
			t.Errorf("%s: registered", name)
		}
	}
}
