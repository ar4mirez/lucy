// Package vault is lucy's per-org credential vault (LUCY-CRED-002): credentials encrypted at rest
// with a key per org, read only by that org's credential gateway.
//
// Layout, under one directory that only its owner can open (0700):
//
//	keys/<org>.key             the org's 32-byte key, 0600
//	orgs/<org>/<name>.sealed   one credential: "LV1", a 12-byte nonce, then AES-256-GCM ciphertext
//
// Each credential is sealed with AES-256-GCM under its org's key, with "lucy-vault-v1", the org
// and the name as additional data, so a sealed file copied to another name or another org's
// directory does not open, and one org's key opens nothing of another's. The keys sit apart from
// the sealed credentials so a copy of orgs/ alone (a backup, a stray sync) reveals nothing; lucy's
// backups (m2) leave keys/ out. Deleting an org's key makes every one of its credentials
// unrecoverable at once (LUCY-CRED-006, m2).
//
// Every file operation goes through an os.Root on the vault directory, so no path, symlink or
// name can reach outside it; names are plain labels; writes go to a new file that is synced and
// renamed into place. The vault never logs a value and never returns its own buffer.
package vault

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Limits.
const (
	KeySize       = 32        // AES-256
	MaxValueBytes = 16 * 1024 // one credential
	maxLabel      = 63
)

var (
	magic = []byte("LV1")

	// ErrNotFound is a credential or org the vault does not hold.
	ErrNotFound = errors.New("vault: no such credential")
	// ErrCorrupt is a key or sealed credential that does not read or does not open: tampered,
	// truncated, moved from another name or org, or written by another key.
	ErrCorrupt = errors.New("vault: a key or sealed credential does not open")
	// ErrInsecure is a vault directory, key or file that others can read or that is not a plain
	// file or directory.
	ErrInsecure = errors.New("vault: insecure file or directory")
	// ErrInvalid is an org, credential name or value the vault does not take.
	ErrInvalid = errors.New("vault: invalid")
)

// Vault is an open vault. It is safe for concurrent use.
type Vault struct {
	root *os.Root
}

// Open opens the vault in dir, creating it 0700 when it does not exist. The directory must be a
// real directory, not a symlink, with no group or other permissions.
func Open(dir string) (*Vault, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, fmt.Errorf("vault: the directory must be a clean absolute path, not %q", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 || fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s (mode %v) must be a directory only its owner can open", ErrInsecure, dir, fi.Mode())
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	v := &Vault{root: r}
	for _, d := range []string{"keys", "orgs"} {
		if err := v.dir(d); err != nil {
			return nil, errors.Join(err, r.Close())
		}
	}
	return v, nil
}

// Close closes the vault.
func (v *Vault) Close() error { return v.root.Close() }

// dir makes sure name is a 0700 directory inside the vault.
func (v *Vault) dir(name string) error {
	if err := v.root.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("vault: %w", err)
	}
	fi, err := v.root.Lstat(name)
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 || fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: %s (mode %v)", ErrInsecure, name, fi.Mode())
	}
	return nil
}

// CheckLabel reports what is wrong with an org or credential name, or "" when it is a plain
// label: 1-63 bytes of a-z, 0-9 and '-', starting with a letter or digit.
func CheckLabel(s string) string {
	if s == "" || len(s) > maxLabel {
		return fmt.Sprintf("must be 1-%d bytes", maxLabel)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' && i > 0) {
			return "may hold only a-z, 0-9 and '-', starting with a letter or digit"
		}
	}
	return ""
}

func checkOrg(org string) error {
	if e := CheckLabel(org); e != "" {
		return fmt.Errorf("%w: org %q %s", ErrInvalid, org, e)
	}
	return nil
}

func checkNames(org, name string) error {
	if err := checkOrg(org); err != nil {
		return err
	}
	if e := CheckLabel(name); e != "" {
		return fmt.Errorf("%w: credential name %q %s", ErrInvalid, name, e)
	}
	return nil
}

func keyPath(org string) string          { return "keys/" + org + ".key" }
func orgDir(org string) string           { return "orgs/" + org }
func sealedPath(org, name string) string { return orgDir(org) + "/" + name + ".sealed" }

// additional is the data each sealed credential is bound to.
func additional(org, name string) []byte {
	return []byte("lucy-vault-v1\x00" + org + "\x00" + name)
}

// readFile reads a regular, owner-only file inside the vault, at most max bytes.
func (v *Vault) readFile(name string, max int) ([]byte, error) {
	fi, err := v.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s (mode %v)", ErrInsecure, name, fi.Mode())
	}
	f, err := v.root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	if len(b) > max {
		return nil, fmt.Errorf("%w: %s is too large", ErrCorrupt, name)
	}
	return b, nil
}

// writeFile writes data to name inside the vault: a new 0600 file beside it, synced, then
// renamed over it, so a reader sees the old file or the new one, never half of one.
func (v *Vault) writeFile(name string, data []byte) error {
	tmp, err := v.writeTemp(name, data)
	if err != nil {
		return err
	}
	if err := v.root.Rename(tmp, name); err != nil {
		_ = v.root.Remove(tmp)
		return fmt.Errorf("vault: %w", err)
	}
	return nil
}

// writeTemp writes data to a new 0600 file beside name, synced, and returns its name.
func (v *Vault) writeTemp(name string, data []byte) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.tmp-%x", name, suffix)
	f, err := v.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("vault: %w", err)
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = v.root.Remove(tmp) // a file this call created inside the vault
		return "", fmt.Errorf("vault: %w", err)
	}
	return tmp, nil
}

// key returns org's key, creating it when create is true and the org has none.
func (v *Vault) key(org string, create bool) ([]byte, error) {
	k, err := v.readFile(keyPath(org), KeySize)
	if errors.Is(err, ErrNotFound) && create {
		k = make([]byte, KeySize)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		tmp, err := v.writeTemp(keyPath(org), k)
		if err != nil {
			clear(k)
			return nil, err
		}
		lerr := v.root.Link(tmp, keyPath(org))
		_ = v.root.Remove(tmp)
		if errors.Is(lerr, fs.ErrExist) {
			clear(k)
			return v.key(org, false) // another writer made it first
		}
		if lerr != nil {
			clear(k)
			return nil, fmt.Errorf("vault: %w", lerr)
		}
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	if len(k) != KeySize {
		return nil, fmt.Errorf("%w: org %s's key is %d bytes", ErrCorrupt, org, len(k))
	}
	return k, nil
}

func aead(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Set stores value as org's credential name, replacing any it held. The org's key is made on its
// first credential.
func (v *Vault) Set(org, name string, value []byte) error {
	if err := checkNames(org, name); err != nil {
		return err
	}
	if len(value) == 0 || len(value) > MaxValueBytes {
		return fmt.Errorf("%w: a credential is 1 to %d bytes", ErrInvalid, MaxValueBytes)
	}
	k, err := v.key(org, true)
	if err != nil {
		return err
	}
	defer clear(k)
	g, err := aead(k)
	if err != nil {
		return err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := append(append(slices.Clone(magic), nonce...), g.Seal(nil, nonce, value, additional(org, name))...)
	if err := v.dir(orgDir(org)); err != nil {
		return err
	}
	return v.writeFile(sealedPath(org, name), sealed)
}

// Credential returns org's credential name, in a new buffer the caller may clear. It implements
// the gateway's credential source.
func (v *Vault) Credential(_ context.Context, org, name string) ([]byte, error) {
	if err := checkNames(org, name); err != nil {
		return nil, err
	}
	sealed, err := v.readFile(sealedPath(org, name), len(magic)+12+MaxValueBytes+16)
	if err != nil {
		return nil, err
	}
	k, err := v.key(org, false)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: org %s has a sealed credential but no key", ErrCorrupt, org)
	}
	if err != nil {
		return nil, err
	}
	defer clear(k)
	g, err := aead(k)
	if err != nil {
		return nil, err
	}
	if len(sealed) < len(magic)+g.NonceSize()+g.Overhead() || !bytes.Equal(sealed[:len(magic)], magic) {
		return nil, fmt.Errorf("%w: %s/%s", ErrCorrupt, org, name)
	}
	nonce := sealed[len(magic) : len(magic)+g.NonceSize()]
	plain, err := g.Open(nil, nonce, sealed[len(magic)+g.NonceSize():], additional(org, name))
	if err != nil {
		return nil, fmt.Errorf("%w: %s/%s", ErrCorrupt, org, name)
	}
	return plain, nil
}

// Entry is one credential the vault holds: its name and when it was last set, never its value.
type Entry struct {
	Name    string
	Updated time.Time
}

// Names returns org's credentials, by name.
func (v *Vault) Names(org string) ([]Entry, error) {
	if err := checkOrg(org); err != nil {
		return nil, err
	}
	f, err := v.root.Open(orgDir(org))
	if errors.Is(err, fs.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	out := []Entry{}
	for _, de := range des {
		name, ok := strings.CutSuffix(de.Name(), ".sealed")
		if !ok || CheckLabel(name) != "" || !de.Type().IsRegular() {
			continue
		}
		info, err := de.Info()
		if err != nil {
			return nil, fmt.Errorf("vault: %w", err)
		}
		out = append(out, Entry{Name: name, Updated: info.ModTime().UTC()})
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Remove deletes org's credential name. It deletes only that one sealed file, inside the vault.
func (v *Vault) Remove(org, name string) error {
	if err := checkNames(org, name); err != nil {
		return err
	}
	fi, err := v.root.Lstat(sealedPath(org, name))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s/%s is not a plain file", ErrInsecure, org, name)
	}
	if err := v.root.Remove(sealedPath(org, name)); err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	return nil
}
