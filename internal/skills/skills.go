// Package skills is lucy's bundled skills (LUCY-RUN-009): files embedded in the binary, each with
// its SHA-256 in builtin/MANIFEST. Before every spawn the runner installs them into the harness's
// skills directory in the org's synthetic home, through a Root on that home: each file is written
// when it differs, read back and verified against the manifest, and every entry of the directory
// that is not a bundled skill, or a file of one that is not in the manifest, is removed. A mismatch
// refuses the spawn. Only lucy's skills are ever installed; the owner's and a repository's never.
package skills

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

//go:embed builtin
var builtinFS embed.FS

// ErrMismatch refuses a skill file whose content does not hash to its manifest line.
var ErrMismatch = errors.New("skills: a skill file does not match its manifest hash")

// File is one file of a bundled skill: its path inside the skills directory, and its SHA-256.
type File struct {
	Path   string // skill/file, for example lucy-worker/SKILL.md
	SHA256 string // hex
}

// Manifest returns the bundled skills' files, sorted by path, and checks that the embedded files
// are exactly those the manifest lists, each with its hash.
func Manifest() ([]File, error) {
	data, err := builtinFS.ReadFile("builtin/MANIFEST")
	if err != nil {
		return nil, err
	}
	var files []File
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sum, p, ok := strings.Cut(line, " ")
		if !ok || len(sum) != 64 || !validPath(p) {
			return nil, fmt.Errorf("skills: bad manifest line %q", line)
		}
		files = append(files, File{Path: p, SHA256: sum})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	listed := map[string]bool{}
	for _, f := range files {
		b, err := builtinFS.ReadFile("builtin/" + f.Path)
		if err != nil {
			return nil, fmt.Errorf("skills: %s is in the manifest but not bundled", f.Path)
		}
		if digest(b) != f.SHA256 {
			return nil, fmt.Errorf("%w: the bundled %s", ErrMismatch, f.Path)
		}
		listed[f.Path] = true
	}
	err = fs.WalkDir(builtinFS, "builtin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "builtin/MANIFEST" {
			return err
		}
		if rel := strings.TrimPrefix(p, "builtin/"); !listed[rel] {
			return fmt.Errorf("skills: %s is bundled but not in the manifest", rel)
		}
		return nil
	})
	return files, err
}

// validPath reports whether p is skill/file: two plain, clean elements.
func validPath(p string) bool {
	parts := strings.Split(p, "/")
	if len(parts) != 2 || path.Clean(p) != p {
		return false
	}
	for _, e := range parts {
		if e == "" || e == "." || e == ".." || strings.HasPrefix(e, ".") {
			return false
		}
	}
	return true
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Install puts the bundled skills into dir (a path relative to home, for example .claude/skills)
// and returns what it changed. Neither dir nor any element on the way to it may be a symlink; it
// writes only inside dir, through home. removeAll deletes a path relative to home, recursively:
// the runner passes the path guard's executor (LUCY-SEC-005), so every recursive delete lucy makes
// goes through it; it is called only on entries inside dir.
func Install(home *os.Root, dir string, removeAll func(rel string) error) ([]string, error) {
	files, err := Manifest()
	if err != nil {
		return nil, err
	}
	if err := noLinks(home, dir); err != nil {
		return nil, err
	}
	if err := home.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	var changed []string
	skills := map[string]map[string]bool{} // skill -> its files
	for _, f := range files {
		skill, name, _ := strings.Cut(f.Path, "/")
		if skills[skill] == nil {
			skills[skill] = map[string]bool{}
		}
		skills[skill][name] = true
	}
	// Remove whatever in dir is not a bundled skill, or not a file of one.
	entries, err := readDir(home, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		p := dir + "/" + e.Name()
		if _, ok := skills[e.Name()]; !ok || e.Type()&fs.ModeSymlink != 0 || !e.IsDir() {
			if err := removeAll(p); err != nil {
				return nil, fmt.Errorf("skills: remove %s: %w", p, err)
			}
			changed = append(changed, "removed "+e.Name())
			continue
		}
		inner, err := readDir(home, p)
		if err != nil {
			return nil, err
		}
		for _, ie := range inner {
			if !skills[e.Name()][ie.Name()] || !ie.Type().IsRegular() {
				if err := removeAll(p + "/" + ie.Name()); err != nil {
					return nil, fmt.Errorf("skills: remove %s/%s: %w", p, ie.Name(), err)
				}
				changed = append(changed, "removed "+e.Name()+"/"+ie.Name())
			}
		}
	}
	for _, f := range files {
		want, err := builtinFS.ReadFile("builtin/" + f.Path)
		if err != nil {
			return nil, err
		}
		p := dir + "/" + f.Path
		skill, _, _ := strings.Cut(f.Path, "/")
		if err := home.MkdirAll(dir+"/"+skill, 0o700); err != nil {
			return nil, fmt.Errorf("skills: %w", err)
		}
		if have, err := home.ReadFile(p); err != nil || !bytes.Equal(have, want) {
			tmp := p + ".lucy-tmp"
			if err := home.WriteFile(tmp, want, 0o600); err != nil {
				return nil, fmt.Errorf("skills: %w", err)
			}
			if err := home.Rename(tmp, p); err != nil {
				_ = home.Remove(tmp) // the rename error is what matters
				return nil, fmt.Errorf("skills: %w", err)
			}
			changed = append(changed, "installed "+f.Path)
		}
		have, err := home.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("skills: verify %s: %w", f.Path, err)
		}
		if digest(have) != f.SHA256 {
			return nil, fmt.Errorf("%w: the installed %s", ErrMismatch, f.Path)
		}
	}
	return changed, nil
}

// noLinks refuses dir when it, or any directory on the way to it inside home, is a symlink.
func noLinks(home *os.Root, dir string) error {
	p := ""
	for _, e := range strings.Split(dir, "/") {
		p = path.Join(p, e)
		fi, err := home.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("skills: %w", err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("skills: %s in the org's home is not a plain directory (a symlink?); lucy never writes through one", p)
		}
	}
	return nil
}

func readDir(home *os.Root, dir string) ([]fs.DirEntry, error) {
	f, err := home.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	defer f.Close() //nolint:errcheck // a read-only handle's close error is no news
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// Instructions returns the body of the bundled skill for a Pilot role (lucy-<role>), without its
// front matter, verified against its manifest hash: the role's instructions, lucy's own text.
func Instructions(role string) (string, error) {
	files, err := Manifest()
	if err != nil {
		return "", err
	}
	want := "lucy-" + role + "/SKILL.md"
	for _, f := range files {
		if f.Path != want {
			continue
		}
		b, err := builtinFS.ReadFile("builtin/" + f.Path)
		if err != nil {
			return "", err
		}
		if digest(b) != f.SHA256 {
			return "", fmt.Errorf("%w: the bundled %s", ErrMismatch, f.Path)
		}
		body := string(b)
		if rest, ok := strings.CutPrefix(body, "---\n"); ok {
			if _, after, found := strings.Cut(rest, "\n---\n"); found {
				body = after
			}
		}
		return strings.TrimSpace(body), nil
	}
	return "", fmt.Errorf("skills: no bundled skill for the %q role", role)
}
