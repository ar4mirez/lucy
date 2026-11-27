package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrRootOrg is the error a runner gets for a root bound to another org.
var ErrRootOrg = errors.New("runner: the root belongs to another org")

// rootOrgFile is the marker in a runner's root naming the one org whose runs it holds.
const rootOrgFile = ".org"

// ValidOrg reports whether org is an org name: not empty, at most 200 bytes, one path element
// that does not start with ".", and free of "#" and control characters.
func ValidOrg(org string) bool {
	if org == "" || len(org) > 200 || org != filepath.Base(org) || strings.HasPrefix(org, ".") ||
		strings.ContainsAny(org, "#/") {
		return false
	}
	for _, r := range org {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ClaimRoot binds the runner root to org, so one org's runs never share a root, and with it the
// org's synthetic home, with another's. On first use it records org in .org in root, 0600, created
// exclusively so of two first users one wins and the other reads what it wrote. A root bound to
// another org is refused with ErrRootOrg; a marker that is not a plain file owned by this user
// holding an org name is refused too. root must exist.
func ClaimRoot(root, org string) error {
	if !ValidOrg(org) {
		return fmt.Errorf("runner: %q is not an org name", org)
	}
	marker := filepath.Join(root, rootOrgFile)
	tmp, err := os.CreateTemp(root, rootOrgFile+"-*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(org + "\n")
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	err = os.Link(tmp.Name(), marker)
	_ = os.Remove(tmp.Name())
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	owner, err := readRootOrg(marker)
	if err != nil {
		return err
	}
	if owner != org {
		return fmt.Errorf("%w: %s is bound to org %q, not %q; give each org's runner its own --root", ErrRootOrg, root, owner, org)
	}
	return nil
}

// readRootOrg returns the org a root's marker names.
func readRootOrg(marker string) (string, error) {
	bad := func(why string) error {
		return fmt.Errorf("runner: the root's org marker %s %s; the root is refused", marker, why)
	}
	fi, err := os.Lstat(marker)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || !ownedByMe(fi) {
		return "", bad(fmt.Sprintf("must be a plain file owned by uid %d, not a symlink (it is %v)", os.Geteuid(), fi.Mode()))
	}
	f, err := os.OpenFile(marker, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- the constant marker name in the runner's own root, checked above to be a plain file of this user's
	if err != nil {
		return "", err
	}
	defer f.Close()
	if now, err := f.Stat(); err != nil || !os.SameFile(fi, now) {
		return "", bad("changed while it was read")
	}
	b, err := io.ReadAll(io.LimitReader(f, 256))
	if err != nil {
		return "", err
	}
	org, ok := strings.CutSuffix(string(b), "\n")
	if !ok || !ValidOrg(org) {
		return "", bad("does not hold an org name")
	}
	return org, nil
}
