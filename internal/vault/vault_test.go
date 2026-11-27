package vault_test

import (
	"bytes"
	"context"
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

func open(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vault")
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v, dir
}

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
	if fi, err := os.Stat(filepath.Join(dir, "keys", "acme.key")); err != nil || fi.Size() != vault.KeySize {
		t.Fatalf("org key: %v, %v", fi, err)
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
	if err := os.Remove(filepath.Join(dir, "keys", "gamma.key")); err != nil {
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
	key := filepath.Join(dir, "keys", "acme.key")
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
	if err := os.Symlink(outside, filepath.Join(dir, "keys", "beta.key")); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("beta", "x", secret(t)); !errors.Is(err, vault.ErrInsecure) {
		t.Errorf("a symlinked key: %v", err)
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
	if _, err := os.Stat(filepath.Join(dir, "keys", "acme.key")); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, "keys", "acme.key.tmp-0011223344556677"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := secret(t)
	if err := v.Set("acme", "github", s); err != nil {
		t.Fatal(err)
	}
	if got, err := v.Credential(ctx, "acme", "github"); err != nil || !bytes.Equal(got, s) {
		t.Fatalf("got %q, %v", got, err)
	}
	k, err := os.ReadFile(filepath.Join(dir, "keys", "acme.key"))
	if err != nil || len(k) != vault.KeySize {
		t.Fatalf("key is %d bytes, %v", len(k), err)
	}
	des, err := os.ReadDir(filepath.Join(dir, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	if len(des) != 2 {
		t.Fatalf("keys/ holds %d files, want the leftover and the key", len(des))
	}
}
