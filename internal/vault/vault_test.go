package vault_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/vault"
)

// Every value here is made up by the test; the vault lives in a temp dir.

func secret(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return []byte("test_" + hex.EncodeToString(b))
}

// testPassphrase is the made-up passphrase test vaults are unlocked with.
var testPassphrase = []byte("test passphrase, not a real one")

func open(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vault")
	return openAt(t, dir), dir
}

// openAt opens and unlocks the vault in dir.
func openAt(t *testing.T, dir string) *vault.Vault {
	t.Helper()
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	if err := v.Unlock(testPassphrase); err != nil {
		t.Fatal(err)
	}
	return v
}

// wrappedKeySize is a wrapped key file: "LVW1", a 12-byte nonce, the 32-byte key and a 16-byte tag.
const wrappedKeySize = 4 + 12 + vault.KeySize + 16

var ctx = context.Background()

func TestSetAndRead(t *testing.T) {
	v, dir := open(t)
	s := secret(t)
	if err := v.Set("acme", "github", s); err != nil {
		t.Fatal(err)
	}
	got, err := v.Credential(ctx, "acme", "github")
	if err != nil || !bytes.Equal(got, s) {
		t.Fatalf("got %q, %v", got, err)
	}
	clear(got) // the caller's own buffer
	if again, _ := v.Credential(ctx, "acme", "github"); !bytes.Equal(again, s) {
		t.Fatal("clearing a returned credential changed the vault")
	}
	s2 := secret(t)
	if err := v.Set("acme", "github", s2); err != nil {
		t.Fatal(err)
	}
	if got, _ := v.Credential(ctx, "acme", "github"); !bytes.Equal(got, s2) {
		t.Fatal("a second Set did not replace the credential")
	}
	names, err := v.Names("acme")
	if err != nil || len(names) != 1 || names[0].Name != "github" || names[0].Updated.IsZero() {
		t.Fatalf("names %+v, %v", names, err)
	}
	if none, err := v.Names("beta"); err != nil || len(none) != 0 {
		t.Fatalf("an org with nothing: %+v, %v", none, err)
	}

	// At rest: nothing on disk holds the value, every directory is 0700 and every file 0600.
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() && info.Mode().Perm() != 0o700 || !d.IsDir() && info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", p, info.Mode())
		}
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if bytes.Contains(b, s) || bytes.Contains(b, s2) {
				t.Errorf("%s holds a credential in clear", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "keys", "acme.wkey")); err != nil || fi.Size() != wrappedKeySize {
		t.Fatalf("org key: %v, %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keys", "acme.key")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a plain org key: %v", err)
	}
}

func TestSealedFilesAreBoundToTheirOrgAndName(t *testing.T) {
	v, dir := open(t)
	if err := v.Set("acme", "github", secret(t)); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("beta", "github", secret(t)); err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(dir, "orgs", "acme", "github.sealed")
	b, err := os.ReadFile(sealed)
	if err != nil {
		t.Fatal(err)
	}
	// Copied under another name of the same org, or into another org: it does not open.
	if err := os.WriteFile(filepath.Join(dir, "orgs", "acme", "npm.sealed"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Credential(ctx, "acme", "npm"); !errors.Is(err, vault.ErrCorrupt) {
		t.Errorf("a credential renamed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orgs", "beta", "github.sealed"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Credential(ctx, "beta", "github"); !errors.Is(err, vault.ErrCorrupt) {
		t.Errorf("another org's credential: %v", err)
	}
	// Tampered: one flipped bit and it does not open.
	b[len(b)-1] ^= 1
	if err := os.WriteFile(sealed, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Credential(ctx, "acme", "github"); !errors.Is(err, vault.ErrCorrupt) {
		t.Errorf("a tampered credential: %v", err)
	}
	// Without its org's key nothing opens.
	if err := v.Set("gamma", "x", secret(t)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "keys", "gamma.wkey")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Credential(ctx, "gamma", "x"); !errors.Is(err, vault.ErrCorrupt) {
		t.Errorf("a credential without its key: %v", err)
	}
}

func TestRefusesUnsafeInput(t *testing.T) {
	v, _ := open(t)
	for _, bad := range [][2]string{{"", "x"}, {"Acme", "x"}, {"../acme", "x"}, {"acme", "../keys/acme"}, {"acme", "a/b"},
		{"acme", ""}, {"acme", strings.Repeat("a", 64)}, {"-acme", "x"}, {"acme", "x.sealed"}} {
		if err := v.Set(bad[0], bad[1], []byte("v")); err == nil {
			t.Errorf("Set(%q, %q) accepted", bad[0], bad[1])
		}
		if _, err := v.Credential(ctx, bad[0], bad[1]); err == nil {
			t.Errorf("Credential(%q, %q) accepted", bad[0], bad[1])
		}
		if err := v.Remove(bad[0], bad[1]); err == nil {
			t.Errorf("Remove(%q, %q) accepted", bad[0], bad[1])
		}
	}
	if err := v.Set("acme", "x", nil); err == nil {
		t.Error("an empty credential was accepted")
	}
	if err := v.Set("acme", "x", make([]byte, vault.MaxValueBytes+1)); err == nil {
		t.Error("an oversized credential was accepted")
	}
	if _, err := v.Credential(ctx, "acme", "missing"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("a missing credential: %v", err)
	}
	if _, err := vault.Open("relative/vault"); err == nil {
		t.Error("a relative vault directory")
	}
}

func TestRefusesInsecureFiles(t *testing.T) {
	wide := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Open(wide); !errors.Is(err, vault.ErrInsecure) {
		t.Errorf("a vault others can open: %v", err)
	}
	v, dir := open(t)
	if err := v.Set("acme", "github", secret(t)); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "keys", "acme.wkey")
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Credential(ctx, "acme", "github"); !errors.Is(err, vault.ErrInsecure) {
		t.Errorf("a key others can read: %v", err)
	}
	if err := os.Chmod(key, 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink in a key's place is refused, wherever it points.
	outside := filepath.Join(t.TempDir(), "elsewhere.key")
	if err := os.WriteFile(outside, make([]byte, vault.KeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "keys", "beta.wkey")); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("beta", "x", secret(t)); !errors.Is(err, vault.ErrInsecure) {
		t.Errorf("a symlinked key: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "keys", "delta.key")); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("delta", "x", secret(t)); !errors.Is(err, vault.ErrInsecure) {
		t.Errorf("a symlinked plain key: %v", err)
	}
	// A symlinked org directory that leads out of the vault is never followed.
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "orgs", "gamma")); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("gamma", "x", secret(t)); err == nil {
		t.Error("a credential was written through a symlink out of the vault")
	}
}

func TestRemove(t *testing.T) {
	v, dir := open(t)
	if err := v.Set("acme", "github", secret(t)); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("acme", "npm", secret(t)); err != nil {
		t.Fatal(err)
	}
	if err := v.Remove("acme", "github"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Credential(ctx, "acme", "github"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("a removed credential: %v", err)
	}
	if err := v.Remove("acme", "github"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("removing it again: %v", err)
	}
	if _, err := v.Credential(ctx, "acme", "npm"); err != nil {
		t.Errorf("another credential was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keys", "acme.wkey")); err != nil {
		t.Errorf("the org's key: %v", err)
	}
}

func TestConcurrentFirstSetsShareOneKey(t *testing.T) {
	for round := range 20 {
		v, _ := open(t)
		org := "org-" + strings.Repeat("a", round%5+1)
		const writers = 8
		vals := make([][]byte, writers)
		errs := make(chan error, writers)
		for i := range writers {
			vals[i] = secret(t)
			go func() { errs <- v.Set(org, "cred-"+string(rune('a'+i)), vals[i]) }()
		}
		for range writers {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: a first Set failed: %v", round, err)
			}
		}
		for i := range writers {
			got, err := v.Credential(ctx, org, "cred-"+string(rune('a'+i)))
			if err != nil || !bytes.Equal(got, vals[i]) {
				t.Fatalf("round %d: credential %d: got %q, %v", round, i, got, err)
			}
		}
	}
}

func TestLeftoverTempKeyDoesNotBreakTheOrg(t *testing.T) {
	v, dir := open(t)
	if err := os.WriteFile(filepath.Join(dir, "keys", "acme.wkey.tmp-0011223344556677"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := secret(t)
	if err := v.Set("acme", "github", s); err != nil {
		t.Fatal(err)
	}
	if got, err := v.Credential(ctx, "acme", "github"); err != nil || !bytes.Equal(got, s) {
		t.Fatalf("got %q, %v", got, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "keys", "acme.wkey")); err != nil || fi.Size() != wrappedKeySize {
		t.Fatalf("wrapped key %v, %v", fi, err)
	}
}

// A wrapped vault opens nothing without its passphrase: a vault not unlocked reads and writes no
// credential, a wrong passphrase is refused, and nothing in it holds the org's key in clear.
func TestWrappedKeysNeedThePassphrase(t *testing.T) {
	v, dir := open(t)
	s := secret(t)
	if err := v.Set("acme", "github", s); err != nil {
		t.Fatal(err)
	}
	locked, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	if _, err := locked.Credential(ctx, "acme", "github"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("a read on a locked vault: %v", err)
	}
	if err := locked.Set("acme", "npm", s); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("a write on a locked vault: %v", err)
	}
	if names, err := locked.Names("acme"); err != nil || len(names) != 1 {
		t.Fatalf("names on a locked vault: %+v, %v", names, err)
	}
	if err := locked.Unlock([]byte("another test passphrase")); !errors.Is(err, vault.ErrPassphrase) {
		t.Fatalf("a wrong passphrase: %v", err)
	}
	if err := locked.Unlock([]byte("short")); !errors.Is(err, vault.ErrPassphrase) {
		t.Fatalf("a passphrase under the minimum: %v", err)
	}
	if err := locked.Unlock(testPassphrase); err != nil {
		t.Fatal(err)
	}
	if got, err := locked.Credential(ctx, "acme", "github"); err != nil || !bytes.Equal(got, s) {
		t.Fatalf("after unlocking: %q, %v", got, err)
	}
	des, err := os.ReadDir(filepath.Join(dir, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	if strings.Join(names, ",") != ".lock,acme.wkey,kek" {
		t.Fatalf("keys/ holds %v", names)
	}
}

// legacyVault is a vault from before keys were wrapped: keys/acme.key in plain and one credential
// sealed under it, written here from the documented format.
func legacyVault(t *testing.T, value []byte) (dir string, key []byte) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "vault")
	key = make([]byte, vault.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"keys", "orgs/acme"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "acme.key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	sealed := append(append([]byte("LV1"), nonce...), g.Seal(nil, nonce, value, []byte("lucy-vault-v1\x00acme\x00github"))...)
	if err := os.WriteFile(filepath.Join(dir, "orgs", "acme", "github.sealed"), sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, key
}

// The first unlock wraps every plain key and removes it, and its leftover copies; the credentials
// sealed under it still open. From then on a plain key is refused, never wrapped.
func TestUnlockMigratesPlainKeys(t *testing.T) {
	s := secret(t)
	dir, key := legacyVault(t, s)
	if err := os.WriteFile(filepath.Join(dir, "keys", "acme.key.tmp-0011223344556677"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	v := openAt(t, dir)
	if got, err := v.Credential(ctx, "acme", "github"); err != nil || !bytes.Equal(got, s) {
		t.Fatalf("a credential sealed before the migration: %q, %v", got, err)
	}
	for _, f := range []string{"acme.key", "acme.key.tmp-0011223344556677"} {
		if _, err := os.Stat(filepath.Join(dir, "keys", f)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("keys/%s after the migration: %v", f, err)
		}
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if b, err := os.ReadFile(p); err != nil || bytes.Contains(b, key) {
			t.Errorf("%s holds the org key in clear (%v)", p, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A plain key put in a wrapped vault is refused, by this process and after another unlock.
	if err := os.WriteFile(filepath.Join(dir, "keys", "beta.key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("beta", "x", s); !errors.Is(err, vault.ErrInsecure) {
		t.Fatalf("a plain key after the migration: %v", err)
	}
	again := openAt(t, dir)
	if err := again.Set("beta", "x", s); !errors.Is(err, vault.ErrInsecure) {
		t.Fatalf("a plain key after another unlock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keys", "beta.wkey")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a plain key put in later was wrapped: %v", err)
	}
}

// A migration that crashed is finished by the next unlock: before the parameters were published
// any wrapped copy is made again, and after it a plain key whose wrapped copy opens to it is
// removed, while one that does not match is kept and refused.
func TestUnlockFinishesACrashedMigration(t *testing.T) {
	t.Run("before the parameters", func(t *testing.T) {
		s := secret(t)
		dir, _ := legacyVault(t, s)
		if err := os.WriteFile(filepath.Join(dir, "keys", "acme.wkey"), []byte("a wrapped key from a crashed attempt"), 0o600); err != nil {
			t.Fatal(err)
		}
		v := openAt(t, dir)
		if got, err := v.Credential(ctx, "acme", "github"); err != nil || !bytes.Equal(got, s) {
			t.Fatalf("%q, %v", got, err)
		}
	})
	t.Run("after the parameters", func(t *testing.T) {
		s := secret(t)
		dir, key := legacyVault(t, s)
		openAt(t, dir) // migrated
		if err := os.WriteFile(filepath.Join(dir, "keys", "acme.key"), key, 0o600); err != nil {
			t.Fatal(err) // as if the crash came before the plain key was removed
		}
		v := openAt(t, dir)
		if _, err := os.Stat(filepath.Join(dir, "keys", "acme.key")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the plain key its wrapped copy matches: %v", err)
		}
		if got, err := v.Credential(ctx, "acme", "github"); err != nil || !bytes.Equal(got, s) {
			t.Fatalf("%q, %v", got, err)
		}
		other := make([]byte, vault.KeySize)
		if err := os.WriteFile(filepath.Join(dir, "keys", "acme.key"), other, 0o600); err != nil {
			t.Fatal(err)
		}
		v = openAt(t, dir)
		if _, err := v.Credential(ctx, "acme", "github"); !errors.Is(err, vault.ErrInsecure) {
			t.Fatalf("a plain key its wrapped copy does not match: %v", err)
		}
	})
	t.Run("a wrapped key and no parameters", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "vault")
		if err := os.MkdirAll(filepath.Join(dir, "keys"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keys", "acme.wkey"), make([]byte, wrappedKeySize), 0o600); err != nil {
			t.Fatal(err)
		}
		v, err := vault.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer v.Close()
		if err := v.Unlock(testPassphrase); !errors.Is(err, vault.ErrCorrupt) {
			t.Fatalf("unlock: %v", err)
		}
	})
}

// Two processes unlocking one plain vault at once agree on one key-encryption key.
func TestConcurrentUnlocksShareOneKEK(t *testing.T) {
	s := secret(t)
	dir, _ := legacyVault(t, s)
	errs := make(chan error, 4)
	vs := make([]*vault.Vault, 4)
	for i := range vs {
		v, err := vault.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { v.Close() })
		vs[i] = v
		go func() { errs <- v.Unlock(testPassphrase) }()
	}
	for range vs {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for i, v := range vs {
		if got, err := v.Credential(ctx, "acme", "github"); err != nil || !bytes.Equal(got, s) {
			t.Fatalf("vault %d: %q, %v", i, got, err)
		}
	}
}
