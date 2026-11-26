// Package pathguard decides whether a path is safe for lucy to delete. It is the primitive behind
// the policy engine's destructive-op rule (LUCY-POL-007) and, from m1, every delete lucy makes
// (LUCY-SEC-005).
//
// The lesson it encodes (incident 1): a worker's code ran rm -rf with "/" and wiped most of
// ~/.config. A guard that compares strings is not enough, because "/proj/link/.." or a symlink
// inside a project can name "/" or $HOME. So the guard:
//
//   - refuses "/", $HOME and every ancestor of $HOME, always;
//   - accepts only paths strictly inside a declared root (a project worktree or a task scratch dir),
//     never a root itself;
//   - refuses relative paths, "." and ".." components, empty components, control and invisible
//     Unicode format characters (which can make a path read differently than it is) and invalid
//     UTF-8;
//   - checks the path after resolving every symlink, and checks the requested spelling too: both
//     must be inside a root, and the resolved path decides which one.
//
// Lexical checks are pure (CheckResolved); Resolve reads the filesystem but never writes. A caller
// that deletes must delete the resolved path it was given, immediately, and must not follow
// symlinks while doing so; TODO(LUCY-SEC-005): the m1 delete executor that does exactly that.
package pathguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Version identifies the guard's rules. The policy engine folds it into its rule-set hash, so a
// change here is a new rule set.
const Version = 1

// Errors. Every refusal wraps ErrRefused; the wrapped detail says why.
var (
	ErrRefused   = errors.New("pathguard: path refused")
	ErrBadRoot   = errors.New("pathguard: invalid root")
	ErrBadHome   = errors.New("pathguard: invalid home")
	errNotInRoot = errors.New("not strictly inside a declared root")
)

// RootKind says what a root is for. The destructive-op rule treats them differently: a delete in a
// task's scratch dir is routine, a delete in a project needs the captain.
type RootKind string

// Root kinds.
const (
	Scratch RootKind = "scratch" // a task's TMPDIR or scratch dir
	Project RootKind = "project" // a project worktree
)

// Root is a directory lucy may delete inside.
type Root struct {
	Kind RootKind `json:"kind"`
	Path string   `json:"path"` // absolute; as declared
}

// Guard holds validated, symlink-resolved roots and home.
type Guard struct {
	home  string
	roots []Root // Path resolved
}

// New validates home and roots and resolves their symlinks. Every root must exist, be absolute
// and be a directory, and must not be "/", home or an ancestor of home: a root like "/home" would
// make the home directory deletable.
func New(home string, roots ...Root) (*Guard, error) {
	h, err := resolveDir(home)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadHome, err)
	}
	if h == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: home is the filesystem root", ErrBadHome)
	}
	g := &Guard{home: h}
	for _, r := range roots {
		if r.Kind != Scratch && r.Kind != Project {
			return nil, fmt.Errorf("%w: %q has unknown kind %q", ErrBadRoot, r.Path, r.Kind)
		}
		p, err := resolveDir(r.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadRoot, err)
		}
		if err := CheckRoot(h, p); err != nil {
			return nil, err
		}
		g.roots = append(g.roots, Root{Kind: r.Kind, Path: p})
	}
	return g, nil
}

// Home returns the resolved home directory.
func (g *Guard) Home() string { return g.home }

// Roots returns the resolved roots.
func (g *Guard) Roots() []Root { return append([]Root(nil), g.roots...) }

// Resolve checks a requested path and returns its symlink-resolved form and the root that holds
// it. The path must exist. Resolve reads the filesystem; it never writes.
func (g *Guard) Resolve(path string) (string, Root, error) {
	if err := CheckSpelling(path); err != nil {
		return "", Root{}, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", Root{}, fmt.Errorf("%w: %s: cannot resolve: %v", ErrRefused, path, err)
	}
	root, err := CheckResolved(g.home, g.roots, path, resolved)
	if err != nil {
		return "", Root{}, err
	}
	return resolved, root, nil
}

// CheckRoot reports whether p (absolute, clean, resolved) may be a root under home.
func CheckRoot(home, p string) error {
	switch {
	case !filepath.IsAbs(p) || filepath.Clean(p) != p:
		return fmt.Errorf("%w: %q is not absolute and clean", ErrBadRoot, p)
	case p == string(filepath.Separator):
		return fmt.Errorf("%w: the filesystem root cannot be a root", ErrBadRoot)
	case p == home:
		return fmt.Errorf("%w: the home directory cannot be a root", ErrBadRoot)
	case within(p, home):
		return fmt.Errorf("%w: %q contains the home directory", ErrBadRoot, p)
	}
	return nil
}

// CheckSpelling checks the requested form of a path, before any resolution: absolute, at most
// 4096 bytes, valid UTF-8, no control or Unicode format characters, and no ".", ".." or empty
// components. One trailing separator is allowed.
func CheckSpelling(path string) error {
	refuse := func(why string) error { return fmt.Errorf("%w: %q: %s", ErrRefused, path, why) }
	if path == "" {
		return refuse("empty")
	}
	if len(path) > 4096 {
		return refuse("longer than 4096 bytes")
	}
	if !utf8.ValidString(path) {
		return refuse("not valid UTF-8")
	}
	for _, c := range path {
		if c == 0 || unicode.IsControl(c) || unicode.Is(unicode.Cf, c) || c == utf8.RuneError {
			return refuse(fmt.Sprintf("contains the invisible or control character %U", c))
		}
		if lookalikes[c] {
			return refuse(fmt.Sprintf("contains %U, which looks like a separator or a dot", c))
		}
	}
	if !filepath.IsAbs(path) {
		return refuse("not absolute")
	}
	trimmed := strings.TrimSuffix(path, string(filepath.Separator))
	if trimmed == "" {
		return refuse("the filesystem root")
	}
	for _, part := range strings.Split(trimmed, string(filepath.Separator))[1:] {
		switch part {
		case "":
			return refuse("empty path component")
		case ".", "..":
			return refuse(fmt.Sprintf("%q component", part))
		}
	}
	return nil
}

// CheckResolved is the pure core of the guard. requested is the path as asked for, resolved is
// the same path after symlink resolution; home and the roots must already be resolved. Both paths
// must be strictly inside a root, and neither may be "/", home or an ancestor of home. It returns
// the root that holds the resolved path.
func CheckResolved(home string, roots []Root, requested, resolved string) (Root, error) {
	if err := CheckSpelling(requested); err != nil {
		return Root{}, err
	}
	if err := CheckSpelling(resolved); err != nil {
		return Root{}, err
	}
	if home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home || home == string(filepath.Separator) {
		return Root{}, fmt.Errorf("%w: home %q is not a resolved directory", ErrBadHome, home)
	}
	for _, p := range []string{filepath.Clean(requested), filepath.Clean(resolved)} {
		if p == home {
			return Root{}, fmt.Errorf("%w: %q is the home directory", ErrRefused, p)
		}
		if within(p, home) {
			return Root{}, fmt.Errorf("%w: %q contains the home directory", ErrRefused, p)
		}
	}
	if _, ok := holder(roots, home, filepath.Clean(requested)); !ok {
		return Root{}, fmt.Errorf("%w: %q is %v", ErrRefused, requested, errNotInRoot)
	}
	root, ok := holder(roots, home, filepath.Clean(resolved))
	if !ok {
		return Root{}, fmt.Errorf("%w: %q resolves to %q, which is %v", ErrRefused, requested, resolved, errNotInRoot)
	}
	return root, nil
}

// lookalikes are characters that render like "/", "\" or "." but are ordinary filename
// characters. A path the captain is asked to approve must read as what it is, so they are refused.
var lookalikes = map[rune]bool{
	'\u2215': true, // division slash
	'\u2044': true, // fraction slash
	'\u29F8': true, // big solidus
	'\uFF0F': true, // fullwidth solidus
	'\u29F5': true, // reverse solidus operator
	'\uFF3C': true, // fullwidth reverse solidus
	'\u2024': true, // one dot leader
	'\u2025': true, // two dot leader
	'\uFF0E': true, // fullwidth full stop
	'\u3002': true, // ideographic full stop
}

// holder returns the innermost valid root strictly containing p.
func holder(roots []Root, home, p string) (Root, bool) {
	var best Root
	found := false
	for _, r := range roots {
		if CheckRoot(home, r.Path) != nil || r.Kind != Scratch && r.Kind != Project {
			continue // an invalid root never admits anything
		}
		if within(r.Path, p) && (!found || len(r.Path) > len(best.Path)) {
			best, found = r, true
		}
	}
	return best, found
}

// within reports whether p is strictly inside dir. Both must be clean and absolute.
func within(dir, p string) bool {
	if dir == string(filepath.Separator) {
		return p != dir && strings.HasPrefix(p, dir)
	}
	return strings.HasPrefix(p, dir+string(filepath.Separator))
}

func resolveDir(p string) (string, error) {
	if p == "" || !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(r)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%q is not a directory", p)
	}
	return r, nil
}
