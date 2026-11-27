package corpus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ar4mirez/lucy/internal/pathguard"
)

// Fixture is the filesystem a delete scenario is checked against, built fresh in a temporary
// directory for every run: a home directory with a config dir worth protecting, a project
// worktree inside it, a flight's scratch dir outside it, a directory outside every root, and
// symlinks that point out of the roots. Most scenarios check the Clearance a delete would need;
// the ones that run the delete executor (Remove) delete only inside the fixture's own temporary
// directory, through the path guard (LUCY-SEC-005).
type Fixture struct {
	Home      string // resolved
	Project   string // a project root, inside Home
	Scratch   string // the flight's scratch root, outside Home
	Elsewhere string // outside every root
}

// Placeholders a scenario's paths may use; Expand replaces them with the fixture's paths.
const (
	PHome      = "{home}"
	PProject   = "{project}"
	PScratch   = "{scratch}"
	PElsewhere = "{elsewhere}"
	PHomeUp    = "{home-parent}" // the directory that holds home
)

// newFixture builds the fixture under dir, which must exist.
func newFixture(dir string) (*Fixture, error) {
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	f := &Fixture{
		Home:      filepath.Join(base, "home"),
		Scratch:   filepath.Join(base, "scratch", "f1"),
		Elsewhere: filepath.Join(base, "elsewhere"),
	}
	f.Project = filepath.Join(f.Home, "src", "web")
	for _, d := range []string{
		filepath.Join(f.Home, ".config", "app"), filepath.Join(f.Project, "build"), filepath.Join(f.Project, "src"),
		filepath.Join(f.Scratch, "cache"), f.Elsewhere,
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	for _, file := range []string{
		filepath.Join(f.Home, ".config", "app", "settings"), filepath.Join(f.Project, "build", "out.bin"),
		filepath.Join(f.Scratch, "cache", "blob"), filepath.Join(f.Elsewhere, "notes"),
	} {
		if err := os.WriteFile(file, []byte("keep\n"), 0o600); err != nil {
			return nil, err
		}
	}
	for link, target := range map[string]string{
		filepath.Join(f.Project, "link-config"): filepath.Join(f.Home, ".config"),
		filepath.Join(f.Project, "link-root"):   string(filepath.Separator),
		filepath.Join(f.Project, "link-build"):  filepath.Join(f.Project, "build"),
		filepath.Join(f.Scratch, "escape"):      f.Home,
		filepath.Join(f.Scratch, "elsewhere"):   f.Elsewhere,
	} {
		if err := os.Symlink(target, link); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// Roots are the roots a delete may happen in.
func (f *Fixture) Roots() []pathguard.Root {
	return []pathguard.Root{{Kind: pathguard.Project, Path: f.Project}, {Kind: pathguard.Scratch, Path: f.Scratch}}
}

// Expand replaces the placeholders in p.
func (f *Fixture) Expand(p string) string {
	return strings.NewReplacer(PHomeUp, filepath.Dir(f.Home), PHome, f.Home, PProject, f.Project, PScratch, f.Scratch, PElsewhere, f.Elsewhere).Replace(p)
}

// Resolve is what the daemon does to gather a delete's facts: it resolves every symlink of the
// requested path. It returns "" when the path cannot be resolved, which the policy denies.
func (f *Fixture) Resolve(p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return r
}

// Repoint makes the symlink at link point to target.
func (f *Fixture) Repoint(link, target string) error {
	link, target = f.Expand(link), f.Expand(target)
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("corpus: %s is not a fixture symlink", link)
	}
	if !strings.HasPrefix(link, filepath.Dir(f.Home)+string(filepath.Separator)) {
		return fmt.Errorf("corpus: %s is outside the fixture", link)
	}
	if err := os.Remove(link); err != nil { // a symlink inside the fixture's own temporary directory
		return err
	}
	return os.Symlink(target, link)
}

// Intact reports an error if any file the fixture created is gone.
func (f *Fixture) Intact() error {
	for _, file := range []string{
		filepath.Join(f.Home, ".config", "app", "settings"), filepath.Join(f.Project, "build", "out.bin"),
		filepath.Join(f.Scratch, "cache", "blob"), filepath.Join(f.Elsewhere, "notes"),
	} {
		if _, err := os.Stat(file); err != nil {
			return fmt.Errorf("corpus: fixture file %s is gone: %w", file, err)
		}
	}
	return nil
}
