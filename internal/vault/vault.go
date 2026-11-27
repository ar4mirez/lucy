// Package vault is lucy's per-org credential vault (LUCY-CRED-002): credentials encrypted at rest
// with a key per org, read only by that org's credential gateway.
//
// Layout, under one directory that only its owner can open (0700):
//
//	keys/kek                   the key-encryption key's parameters: "LVK1", PBKDF2 iterations,
//	                           a random salt and a check value sealed under the key
//	keys/<org>.wkey            the org's 32-byte key, sealed with AES-256-GCM under the
//	                           key-encryption key: "LVW1", a 12-byte nonce, then the ciphertext
//	orgs/<org>/<name>.sealed   one credential: "LV1", a 12-byte nonce, then AES-256-GCM ciphertext
//
// Each credential is sealed with AES-256-GCM under its org's key, with "lucy-vault-v1", the org
// and the name as additional data, so a sealed file copied to another name or another org's
// directory does not open, and one org's key opens nothing of another's. Each org key is in turn
// sealed under the key-encryption key, which is derived from the owner's passphrase with
// PBKDF2-HMAC-SHA256 and never stored, so a copy of the whole vault opens nothing without the
// passphrase (D14). Until it is unlocked with the passphrase (Unlock) a vault lists names but
// reads and writes no credential. A vault made before keys were wrapped holds keys/<org>.key in
// plain; the first Unlock wraps every one of them and removes the plain copies, and from then on
// a plain key is refused. The keys sit apart from the sealed credentials so a copy of orgs/ alone
// (a backup, a stray sync) reveals nothing; lucy's backups (m2) leave keys/ out. Deleting an org's
// key makes every one of its credentials unrecoverable at once (LUCY-CRED-006, m2).
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
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Limits.
const (
	KeySize       = 32        // AES-256
	MaxValueBytes = 16 * 1024 // one credential
	maxLabel      = 63
	// MinPassphrase and MaxPassphrase bound the passphrase, in bytes.
	MinPassphrase = 12
	MaxPassphrase = 1024
	saltSize      = 16
)

// kdfIterations is the PBKDF2-HMAC-SHA256 work a new vault's key-encryption key is derived with
// (OWASP's 2023 figure); minIterations is the least a vault's parameters may name. Tests lower both.
var (
	kdfIterations = 600_000
	minIterations = 100_000
)

var (
	magic        = []byte("LV1")
	kekMagic     = []byte("LVK1")
	wrappedMagic = []byte("LVW1")
	kekCheck     = []byte("lucy-vault-kek-check")

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
	// ErrLocked is a credential read or write on a vault not yet unlocked with its passphrase.
	ErrLocked = errors.New("vault: locked: unlock it with the vault passphrase first")
	// ErrPassphrase is a passphrase that does not unlock the vault.
	ErrPassphrase = errors.New("vault: the passphrase does not unlock this vault")
)

// Vault is an open vault. It is safe for concurrent use.
type Vault struct {
	root *os.Root

	mu  sync.RWMutex
	kek cipher.AEAD // set by Unlock
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

// Close closes the vault and forgets its key-encryption key.
func (v *Vault) Close() error {
	v.mu.Lock()
	v.kek = nil
	v.mu.Unlock()
	return v.root.Close()
}

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

func keyPath(org string) string          { return "keys/" + org + ".key" } // a plain key, from before keys were wrapped
func wrappedPath(org string) string      { return "keys/" + org + ".wkey" }
func orgDir(org string) string           { return "orgs/" + org }
func sealedPath(org, name string) string { return orgDir(org) + "/" + name + ".sealed" }

// keyAdditional is the data each wrapped org key is bound to.
func keyAdditional(org string) []byte { return []byte("lucy-vault-key-v1\x00" + org) }

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

// key returns org's key, creating it when create is true and the org has none. It needs the vault
// unlocked, and refuses an org that holds a plain key: Unlock wrapped every plain key there was,
// so one found now was put there since.
func (v *Vault) key(org string, create bool) ([]byte, error) {
	v.mu.RLock()
	kek := v.kek
	v.mu.RUnlock()
	if kek == nil {
		return nil, ErrLocked
	}
	if _, err := v.root.Lstat(keyPath(org)); err == nil {
		return nil, fmt.Errorf("%w: org %s holds an unwrapped key, and the vault's keys are wrapped", ErrInsecure, org)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("vault: %w", err)
	}
	w, err := v.readFile(wrappedPath(org), len(wrappedMagic)+12+KeySize+16)
	if errors.Is(err, ErrNotFound) && create {
		k := make([]byte, KeySize)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		w, err := wrap(kek, org, k)
		if err != nil {
			clear(k)
			return nil, err
		}
		tmp, err := v.writeTemp(wrappedPath(org), w)
		if err != nil {
			clear(k)
			return nil, err
		}
		lerr := v.root.Link(tmp, wrappedPath(org))
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
	return unwrap(kek, org, w)
}

// wrap seals org's key under the key-encryption key.
func wrap(kek cipher.AEAD, org string, k []byte) ([]byte, error) {
	nonce := make([]byte, kek.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(append(slices.Clone(wrappedMagic), nonce...), kek.Seal(nil, nonce, k, keyAdditional(org))...), nil
}

// unwrap opens org's wrapped key.
func unwrap(kek cipher.AEAD, org string, w []byte) ([]byte, error) {
	n := len(wrappedMagic) + kek.NonceSize()
	if len(w) != n+KeySize+kek.Overhead() || !bytes.Equal(w[:len(wrappedMagic)], wrappedMagic) {
		return nil, fmt.Errorf("%w: org %s's wrapped key", ErrCorrupt, org)
	}
	k, err := kek.Open(nil, w[len(wrappedMagic):n], w[n:], keyAdditional(org))
	if err != nil {
		return nil, fmt.Errorf("%w: org %s's wrapped key", ErrCorrupt, org)
	}
	return k, nil
}

// Unlock derives the vault's key-encryption key from passphrase, which the vault never stores,
// and keeps it until Close. On a vault with no key-encryption key yet it makes one, with a fresh
// random salt, and wraps every plain org key the vault holds, then removes the plain copies; on
// one that has it, a passphrase that does not derive the same key is refused (ErrPassphrase).
// The caller clears passphrase. Unlock holds keys/.lock while it runs, so two processes never
// migrate the same vault at once, and each step is ordered so that a crash leaves a vault the
// next Unlock finishes: every key is wrapped before the parameters are published, and a plain
// key is removed only once its wrapped copy opens to the same key.
func (v *Vault) Unlock(passphrase []byte) error {
	if len(passphrase) < MinPassphrase || len(passphrase) > MaxPassphrase {
		return fmt.Errorf("%w: a vault passphrase is %d to %d bytes", ErrInvalid, MinPassphrase, MaxPassphrase)
	}
	lock, err := v.lock()
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck // closing releases the lock
	kek, err := v.deriveKEK(passphrase)
	if err != nil {
		return err
	}
	if err := v.finishMigration(kek); err != nil {
		return err
	}
	v.mu.Lock()
	v.kek = kek
	v.mu.Unlock()
	return nil
}

// HasPassphrase reports whether the vault's keys are wrapped under a passphrase already: false for
// a new vault, or one from before keys were wrapped, whose first Unlock sets the passphrase.
func (v *Vault) HasPassphrase() (bool, error) {
	_, err := v.root.Lstat("keys/kek")
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("vault: %w", err)
	}
	return true, nil
}

// lock takes keys/.lock, exclusively.
func (v *Vault) lock() (*os.File, error) {
	f, err := v.root.OpenFile("keys/.lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close() // the lock error is what matters
		return nil, fmt.Errorf("vault: lock: %w", err)
	}
	return f, nil
}

// deriveKEK reads the vault's key-encryption parameters and derives its key from passphrase, or,
// on a vault without them, makes them, wraps every plain key under the new key and then
// publishes them.
func (v *Vault) deriveKEK(passphrase []byte) (cipher.AEAD, error) {
	b, err := v.readFile("keys/kek", len(kekMagic)+4+saltSize+12+len(kekCheck)+16)
	if errors.Is(err, ErrNotFound) {
		return v.makeKEK(passphrase)
	}
	if err != nil {
		return nil, err
	}
	if len(b) < len(kekMagic)+4+saltSize+12 || !bytes.Equal(b[:len(kekMagic)], kekMagic) {
		return nil, fmt.Errorf("%w: keys/kek", ErrCorrupt)
	}
	rest := b[len(kekMagic):]
	iter := int(binary.BigEndian.Uint32(rest))
	if iter < minIterations {
		return nil, fmt.Errorf("%w: keys/kek names %d PBKDF2 iterations, fewer than %d", ErrCorrupt, iter, minIterations)
	}
	salt, sealed := rest[4:4+saltSize], rest[4+saltSize:]
	kek, err := kekFor(passphrase, salt, iter)
	if err != nil {
		return nil, err
	}
	if len(sealed) < kek.NonceSize() {
		return nil, fmt.Errorf("%w: keys/kek", ErrCorrupt)
	}
	got, err := kek.Open(nil, sealed[:kek.NonceSize()], sealed[kek.NonceSize():], []byte("lucy-vault-kek-v1"))
	if err != nil || !bytes.Equal(got, kekCheck) {
		return nil, ErrPassphrase
	}
	return kek, nil
}

func kekFor(passphrase, salt []byte, iter int) (cipher.AEAD, error) {
	k, err := pbkdf2.Key(sha256.New, string(passphrase), salt, iter, KeySize)
	if err != nil {
		return nil, err
	}
	defer clear(k)
	return aead(k)
}

// makeKEK makes a vault's key-encryption parameters: it wraps every plain key under the new key,
// replacing any wrapped copy a crashed earlier attempt left, and only then publishes them.
func (v *Vault) makeKEK(passphrase []byte) (cipher.AEAD, error) {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	kek, err := kekFor(passphrase, salt, kdfIterations)
	if err != nil {
		return nil, err
	}
	orgs, err := v.plainKeys()
	if err != nil {
		return nil, err
	}
	wrapped, err := v.wrappedKeys()
	if err != nil {
		return nil, err
	}
	for _, org := range wrapped {
		if !slices.Contains(orgs, org) {
			// A wrapped key with no parameters to open it and no plain key to make it again from.
			return nil, fmt.Errorf("%w: org %s's wrapped key, and the vault has no key-encryption parameters", ErrCorrupt, org)
		}
	}
	for _, org := range orgs {
		k, err := v.plainKey(org)
		if err != nil {
			return nil, err
		}
		w, err := wrap(kek, org, k)
		clear(k)
		if err != nil {
			return nil, err
		}
		if err := v.writeFile(wrappedPath(org), w); err != nil {
			return nil, err
		}
	}
	nonce := make([]byte, kek.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	var iter [4]byte
	binary.BigEndian.PutUint32(iter[:], uint32(kdfIterations)) // #nosec G115 -- a small positive constant
	params := slices.Concat(kekMagic, iter[:], salt, nonce, kek.Seal(nil, nonce, kekCheck, []byte("lucy-vault-kek-v1")))
	tmp, err := v.writeTemp("keys/kek", params)
	if err != nil {
		return nil, err
	}
	lerr := v.root.Link(tmp, "keys/kek")
	_ = v.root.Remove(tmp)
	if lerr != nil {
		return nil, fmt.Errorf("vault: %w", lerr) // under keys/.lock no other Unlock publishes it
	}
	return kek, nil
}

// finishMigration removes every plain key whose wrapped copy opens to the same key under kek, and
// every keys/<org>.key.tmp-* a crashed key write left. A plain key with no such wrapped copy stays,
// and that org is refused (key).
func (v *Vault) finishMigration(kek cipher.AEAD) error {
	orgs, err := v.plainKeys()
	if err != nil {
		return err
	}
	for _, org := range orgs {
		w, err := v.readFile(wrappedPath(org), len(wrappedMagic)+12+KeySize+16)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		k, err := unwrap(kek, org, w)
		if err != nil {
			continue
		}
		plain, err := v.plainKey(org)
		same := err == nil && bytes.Equal(plain, k)
		clear(k)
		clear(plain)
		if !same {
			continue
		}
		if err := v.root.Remove(keyPath(org)); err != nil {
			return fmt.Errorf("vault: %w", err)
		}
	}
	des, err := v.keyEntries()
	if err != nil {
		return err
	}
	for _, de := range des {
		if base, _, ok := strings.Cut(de.Name(), ".key.tmp-"); ok && CheckLabel(base) == "" && de.Type().IsRegular() {
			if err := v.root.Remove("keys/" + de.Name()); err != nil {
				return fmt.Errorf("vault: %w", err)
			}
		}
	}
	return nil
}

func (v *Vault) keyEntries() ([]fs.DirEntry, error) {
	f, err := v.root.Open("keys")
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only
	des, err := f.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	return des, nil
}

// keysWith returns the orgs whose keys/<org><suffix> is a plain file, sorted.
func (v *Vault) keysWith(suffix string) ([]string, error) {
	des, err := v.keyEntries()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, de := range des {
		if org, ok := strings.CutSuffix(de.Name(), suffix); ok && CheckLabel(org) == "" && de.Type().IsRegular() {
			out = append(out, org)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (v *Vault) plainKeys() ([]string, error)   { return v.keysWith(".key") }
func (v *Vault) wrappedKeys() ([]string, error) { return v.keysWith(".wkey") }

// plainKey reads org's plain key, from before keys were wrapped.
func (v *Vault) plainKey(org string) ([]byte, error) {
	k, err := v.readFile(keyPath(org), KeySize)
	if err != nil {
		return nil, err
	}
	if len(k) != KeySize {
		clear(k)
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
