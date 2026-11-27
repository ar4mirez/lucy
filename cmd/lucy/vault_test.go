package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/vault"
)

// withStdin runs lucy with stdin fed from in, as a pipe.
func withStdin(t *testing.T, in string, args ...string) (int, string, string) {
	t.Helper()
	oldIn, oldTerm := stdin, stdinIsTerminal
	stdin, stdinIsTerminal = strings.NewReader(in), func() bool { return false }
	defer func() { stdin, stdinIsTerminal = oldIn, oldTerm }()
	return lucy(args...)
}

func TestVaultCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	value := "test_" + hex.EncodeToString(b) // made up by the test
	code, out, errOut := withStdin(t, value+"\n", "vault", "set", "--dir", dir, "--org", "acme", "github")
	if code != 0 || out != "set: acme/github\n" || strings.Contains(out+errOut, value) {
		t.Fatalf("set: %d %q %q", code, out, errOut)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Credential(context.Background(), "acme", "github")
	v.Close()
	if err != nil || string(got) != value {
		t.Fatalf("stored %q, %v; want the value without its newline", got, err)
	}
	code, out, _ = lucy("vault", "ls", "--dir", dir, "--org", "acme")
	if code != 0 || !strings.HasPrefix(out, "credentials[1]{name,updated}:\n  github,") || strings.Contains(out, value) {
		t.Fatalf("ls: %d %q", code, out)
	}
	if code, out, _ := lucy("vault", "ls", "--dir", dir, "--org", "beta"); code != 0 || out != "credentials[0]: beta holds none\n" {
		t.Fatalf("ls of an empty org: %d %q", code, out)
	}
	if code, out, _ := lucy("vault", "rm", "--dir", dir, "--org", "acme", "github"); code != 0 || out != "removed: acme/github\n" {
		t.Fatalf("rm: %d %q", code, out)
	}
	if code, _, errOut := lucy("vault", "rm", "--dir", dir, "--org", "acme", "github"); code != 1 || !strings.Contains(errOut, "no such credential") {
		t.Fatalf("rm again: %d %q", code, errOut)
	}
}

func TestVaultSetRefusesATerminalAndBadInput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	oldIn, oldTerm := stdin, stdinIsTerminal
	stdin, stdinIsTerminal = strings.NewReader("typed\n"), func() bool { return true }
	code, _, errOut := lucy("vault", "set", "--dir", dir, "--org", "acme", "github")
	stdin, stdinIsTerminal = oldIn, oldTerm
	if code != 2 || !strings.Contains(errOut, "not a terminal") {
		t.Fatalf("a terminal: %d %q", code, errOut)
	}
	for _, c := range []struct {
		in   string
		args []string
	}{
		{"", []string{"vault", "set", "--dir", dir, "--org", "acme", "github"}},
		{"\n", []string{"vault", "set", "--dir", dir, "--org", "acme", "github"}},
		{strings.Repeat("x", vault.MaxValueBytes+1), []string{"vault", "set", "--dir", dir, "--org", "acme", "github"}},
		{"v", []string{"vault", "set", "--dir", dir, "--org", "Acme", "github"}},
		{"v", []string{"vault", "set", "--dir", dir, "--org", "acme", "../keys"}},
		{"v", []string{"vault", "set", "--dir", dir, "--org", "acme"}},
		{"v", []string{"vault", "set", "--dir", dir, "github"}},
		{"v", []string{"vault", "ls", "--dir", dir, "--org", "acme", "extra"}},
		{"v", []string{"vault"}},
		{"v", []string{"vault", "nope"}},
	} {
		if code, _, errOut := withStdin(t, c.in, c.args...); code != 2 || !strings.Contains(errOut, "help: ") {
			t.Errorf("%v: %d %q", c.args, code, errOut)
		}
	}
	if code, out, _ := lucy("vault", "--help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("help: %d", code)
	}
}

// A gateway's vault view gives it its own org's credentials and nobody else's.
func TestOrgVaultIsOneOrg(t *testing.T) {
	v, err := vault.Open(filepath.Join(t.TempDir(), "vault"))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.Set("beta", "github", []byte("made-up")); err != nil {
		t.Fatal(err)
	}
	if _, err := (orgVault{v: v, org: "acme"}).Credential(context.Background(), "beta", "github"); err == nil {
		t.Fatal("the acme gateway read beta's credential")
	}
}
