package pathguard

import (
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree builds a small filesystem in a temp dir, full of symlink tricks:
//
//	base/home/                 fake $HOME
//	base/home/proj/            project root
//	base/home/proj/sub/{f,d/}
//	base/home/other/           not a root
//	base/scratch/t/x           scratch root, outside home like a TMPDIR
//
// and symlinks that point up, out, at "/", at home, across roots and in loops.
type tree struct {
	base, home, proj, other, scratch string
	g                                *Guard
}

func newTree(t *testing.T) *tree {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tr := &tree{
		base:    base,
		home:    filepath.Join(base, "home"),
		proj:    filepath.Join(base, "home", "proj"),
		other:   filepath.Join(base, "home", "other"),
		scratch: filepath.Join(base, "scratch"),
	}
	for _, d := range []string{
		filepath.Join(tr.proj, "sub", "d"), tr.other, filepath.Join(tr.scratch, "t", "x"),
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tr.proj, "sub", "f"), []byte("f"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		filepath.Join(tr.proj, "up"):           "..",
		filepath.Join(tr.proj, "uproot"):       "/",
		filepath.Join(tr.proj, "tohome"):       tr.home,
		filepath.Join(tr.proj, "toother"):      "../other",
		filepath.Join(tr.proj, "insub"):        "sub",
		filepath.Join(tr.proj, "toscratch"):    filepath.Join(tr.scratch, "t"),
		filepath.Join(tr.proj, "dangling"):     "nowhere",
		filepath.Join(tr.proj, "sub", "back"):  "../..",
		filepath.Join(tr.scratch, "toproj"):    filepath.Join(tr.proj, "sub"),
		filepath.Join(tr.scratch, "loop"):      "loop",
		filepath.Join(tr.scratch, "t", "base"): tr.base,
	}
	for name, target := range links {
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	g, err := New(tr.home, Root{Project, tr.proj}, Root{Scratch, tr.scratch})
	if err != nil {
		t.Fatal(err)
	}
	tr.g = g
	return tr
}

func TestResolveTable(t *testing.T) {
	tr := newTree(t)
	cases := []struct {
		path string
		kind RootKind // "" means refused
	}{
		{filepath.Join(tr.proj, "sub"), Project},
		{filepath.Join(tr.proj, "sub") + "/", Project},
		{filepath.Join(tr.proj, "sub", "f"), Project},
		{filepath.Join(tr.proj, "insub", "d"), Project},
		{filepath.Join(tr.scratch, "t", "x"), Scratch},
		{filepath.Join(tr.proj, "toscratch", "x"), Scratch},  // the resolved path decides the root
		{filepath.Join(tr.scratch, "toproj", "d"), Project},  // a scratch link into a project is a project delete
		{filepath.Join(tr.scratch, "toproj") + "/", Project}, // a trailing slash follows the link
		{"/", ""},                                                // the filesystem root
		{tr.home, ""},                                            // home
		{tr.base, ""},                                            // an ancestor of home
		{tr.proj, ""},                                            // a root itself
		{tr.scratch + "/", ""},                                   // a root itself, trailing slash
		{tr.other, ""},                                           // inside home, outside every root
		{filepath.Join(tr.proj, "up"), ""},                       // a link to home
		{filepath.Join(tr.proj, "uproot"), ""},                   // a link to "/"
		{filepath.Join(tr.proj, "tohome"), ""},                   // an absolute link to home
		{filepath.Join(tr.proj, "toother"), ""},                  // a link out of the root
		{filepath.Join(tr.proj, "sub", "back"), ""},              // a nested link up to home
		{filepath.Join(tr.proj, "up", "other"), ""},              // through a link and out
		{filepath.Join(tr.proj, "dangling"), ""},                 // does not resolve
		{filepath.Join(tr.scratch, "loop"), ""},                  // a symlink loop
		{filepath.Join(tr.scratch, "t", "base", "home"), ""},     // a scratch link back to home
		{tr.proj + "/sub/../sub", ""},                            // ".." is refused even when harmless
		{filepath.Join(tr.proj, "sub") + "/./f", ""},             // "." is refused
		{tr.proj + "//sub", ""},                                  // empty component
		{"proj/sub", ""},                                         // relative
		{"", ""},                                                 // empty
		{filepath.Join(tr.proj, "sub\x00"), ""},                  // NUL
		{filepath.Join(tr.proj, "su\u200bb"), ""},                // zero-width space
		{filepath.Join(tr.proj, "\u202esub"), ""},                // right-to-left override
		{filepath.Join(tr.proj, "sub\u2215..\u2215.."), ""},      // division slash looks like a separator
		{filepath.Join(tr.proj, "sub\uFF0F\uFF0E\uFF0E"), ""},    // fullwidth "/.."
		{filepath.Join(tr.base, "home", "pr\u043ej", "sub"), ""}, // Cyrillic o: a different, absent dir
		{filepath.Join(tr.proj, "sub") + "\xff", ""},             // invalid UTF-8
	}
	for _, c := range cases {
		resolved, root, err := tr.g.Resolve(c.path)
		if c.kind == "" {
			if err == nil {
				t.Errorf("Resolve(%q) = %q in %v, want refused", c.path, resolved, root)
			} else if !errors.Is(err, ErrRefused) {
				t.Errorf("Resolve(%q) error %v does not wrap ErrRefused", c.path, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("Resolve(%q): %v, want a %s root", c.path, err, c.kind)
			continue
		}
		if root.Kind != c.kind {
			t.Errorf("Resolve(%q) root %v, want kind %s", c.path, root, c.kind)
		}
	}
}

func TestNewRefusesDangerousRoots(t *testing.T) {
	tr := newTree(t)
	bad := []Root{
		{Project, "/"},
		{Project, tr.home},
		{Project, tr.home + "/"},
		{Scratch, tr.base},                          // contains home
		{Project, filepath.Join(tr.proj, "up")},     // a link to home
		{Project, filepath.Join(tr.proj, "uproot")}, // a link to "/"
		{Project, "relative/dir"},
		{Project, filepath.Join(tr.proj, "sub", "f")}, // not a directory
		{Project, filepath.Join(tr.proj, "missing")},
		{"other", tr.proj},
	}
	for _, r := range bad {
		if _, err := New(tr.home, r); !errors.Is(err, ErrBadRoot) {
			t.Errorf("New(root %v) = %v, want ErrBadRoot", r, err)
		}
	}
	for _, h := range []string{"", "/", "relative", filepath.Join(tr.base, "absent")} {
		if _, err := New(h); !errors.Is(err, ErrBadHome) {
			t.Errorf("New(home %q) = %v, want ErrBadHome", h, err)
		}
	}
}

func TestCheckResolvedIgnoresInvalidRoots(t *testing.T) {
	// A caller that hands CheckResolved unvalidated roots still cannot delete home or "/".
	home := "/home/u"
	roots := []Root{{Project, "/"}, {Project, "/home"}, {Scratch, home}, {"bogus", "/srv"}}
	for _, p := range []string{"/etc/passwd", "/home/u/.config", "/home/v/x", "/srv/x"} {
		if _, err := CheckResolved(home, roots, p, p); err == nil {
			t.Errorf("CheckResolved(%q) admitted the path through an invalid root", p)
		}
	}
	// The requested spelling must be inside a root too, not only the resolved path.
	ok := []Root{{Project, "/w/p"}}
	if _, err := CheckResolved(home, ok, "/elsewhere/link", "/w/p/x"); err == nil {
		t.Error("a request outside every root was admitted because it resolved inside one")
	}
	if _, err := CheckResolved(home, ok, "/w/p/x", "/w/p/x"); err != nil {
		t.Errorf("a plain path inside a root was refused: %v", err)
	}
}

// TestResolveProperty builds random paths out of components that include every trick in the
// tree and checks the guard's promise for each one it admits: the real, kernel-resolved target is
// strictly inside the root the guard named, and is never "/", home, an ancestor of home or a root.
func TestResolveProperty(t *testing.T) {
	tr := newTree(t)
	starts := []string{tr.proj, tr.scratch, tr.home, tr.base, "", tr.other, filepath.Join(tr.scratch, "t")}
	parts := []string{
		"sub", "d", "f", "up", "uproot", "tohome", "toother", "insub", "toscratch", "toproj", "back",
		"..", ".", "", "x", "t", "loop", "dangling", "proj", "home", "scratch", "other", "base",
		"a\u202eb", "sub\u2215..", "pr\u043ej", "etc",
	}
	protected := []string{"/", tr.home, tr.base, tr.proj, tr.scratch}
	rng := rand.New(rand.NewPCG(1, 2))
	admitted, refused := 0, 0
	for i := 0; i < 5000; i++ {
		var b strings.Builder
		b.WriteString(starts[rng.IntN(len(starts))])
		for n := rng.IntN(6); n > 0; n-- {
			b.WriteString("/" + parts[rng.IntN(len(parts))])
		}
		if rng.IntN(4) == 0 {
			b.WriteString("/")
		}
		path := b.String()
		resolved, root, err := tr.g.Resolve(path)
		if err != nil {
			refused++
			continue
		}
		admitted++
		real, rerr := filepath.EvalSymlinks(path)
		if rerr != nil || real != resolved {
			t.Fatalf("Resolve(%q) = %q, but the path resolves to %q (%v)", path, resolved, real, rerr)
		}
		if !within(root.Path, resolved) {
			t.Fatalf("Resolve(%q) = %q, not strictly inside its root %q", path, resolved, root.Path)
		}
		for _, p := range protected {
			if resolved == p || within(resolved, p) {
				t.Fatalf("Resolve(%q) = %q, which is or contains the protected %q", path, resolved, p)
			}
			if same(t, resolved, p) {
				t.Fatalf("Resolve(%q) = %q, the same file as the protected %q", path, resolved, p)
			}
		}
	}
	if admitted < 50 || refused < 50 {
		t.Fatalf("generator is lopsided: %d admitted, %d refused", admitted, refused)
	}
}

func same(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(fa, fb)
}
