package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
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

// startDaemon runs `lucy daemon` on a socket in a temp dir until the test ends, and returns the
// socket.
func startDaemon(t *testing.T) string {
	t.Helper()
	return startDaemonWith(t, nil)
}

// startDaemonWith starts a daemon as startDaemon does, after seed (when set) has written to its
// database, which no daemon holds yet.
func startDaemonWith(t *testing.T, seed func(db string)) string {
	t.Helper()
	dir := shortTempDir(t)
	if seed != nil {
		seed(filepath.Join(dir, "data", "lucy.db"))
	}
	sock := filepath.Join(dir, "run", "lucy.sock")
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	ctx, cancel := context.WithCancel(context.Background())
	signalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"daemon", "--socket", sock, "--db", filepath.Join(dir, "data", "lucy.db")}, io.Discard, io.Discard)
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if code, _, _ := lucy("status", "--socket", sock); code == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon not up")
		}
	}
	t.Cleanup(func() {
		lucy("daemon", "stop", "--socket", sock)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("lucy daemon did not stop")
		}
		cancel()
		signalContext = defaultSignalContext
	})
	return sock
}

func TestVaultCommands(t *testing.T) {
	sock := startDaemon(t)
	dir := filepath.Join(t.TempDir(), "vault")
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	value := "test_" + hex.EncodeToString(b) // made up by the test
	pass := passphraseFile(t, testVaultPassphrase)
	code, out, errOut := withStdin(t, value+"\n", "vault", "set", "--dir", dir, "--socket", sock, "--passphrase-file", pass, "--org", "acme", "github")
	if code != 0 || out != "set: acme/github\n" || strings.Contains(out+errOut, value) {
		t.Fatalf("set: %d %q %q", code, out, errOut)
	}
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Unlock([]byte(testVaultPassphrase)); err != nil {
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
	if code, out, _ := lucy("vault", "rm", "--dir", dir, "--socket", sock, "--org", "acme", "github"); code != 0 || out != "removed: acme/github\n" {
		t.Fatalf("rm: %d %q", code, out)
	}
	if code, _, errOut := lucy("vault", "rm", "--dir", dir, "--socket", sock, "--org", "acme", "github"); code != 1 || !strings.Contains(errOut, "no such credential") {
		t.Fatalf("rm again: %d %q", code, errOut)
	}
	// Each change is in the audit chain: requested, then applied or failed, by name only.
	code, out, _ = lucy("events", "--socket", sock, "--limit", "500")
	if code != 0 || strings.Contains(out, value) {
		t.Fatalf("events: %d", code)
	}
	if n := strings.Count(out, ",vault/acme,"); n != 6 {
		t.Fatalf("%d vault events, want 6 (set, rm and the failed rm, each requested and done):\n%s", n, out)
	}
}

// A failed change records the kind of error, never its text: no path, no org/name beyond the
// record's own fields. The full error still goes to stderr.
func TestVaultFailureRecordsItsKindOnly(t *testing.T) {
	sock := startDaemon(t)
	dir := filepath.Join(t.TempDir(), "vault")
	if code, _, errOut := lucy("vault", "rm", "--dir", dir, "--socket", sock, "--org", "acme", "missing"); code != 1 || !strings.Contains(errOut, "no such credential") {
		t.Fatalf("rm of a missing credential: %d %q", code, errOut)
	}
	if err := os.MkdirAll(filepath.Join(dir, "orgs", "acme", "github.sealed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := lucy("vault", "rm", "--dir", dir, "--socket", sock, "--org", "acme", "github"); code != 1 || !strings.Contains(errOut, "acme/github is not a plain file") {
		t.Fatalf("rm of a directory: %d %q", code, errOut)
	}
	c := client.New(sock, "lucy-test", "test")
	defer c.Close()
	page, err := c.Events(context.Background(), api.EventsParams{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	var failed []vault.Changed
	for _, e := range page.Events {
		if e.Stream != vault.Stream("acme") {
			continue
		}
		var ch vault.Changed
		if err := json.Unmarshal(e.Payload, &ch); err != nil {
			t.Fatal(err)
		}
		if ch.Phase == vault.PhaseFailed {
			failed = append(failed, ch)
		}
		if raw := string(e.Payload); strings.Contains(raw, dir) || strings.Contains(raw, "/") || strings.Contains(raw, "plain file") {
			t.Errorf("the record holds a path or the error's text: %s", raw)
		}
	}
	if len(failed) != 2 || failed[0].Name != "missing" || failed[0].Error != vault.ReasonNotFound ||
		failed[1].Name != "github" || failed[1].Error != vault.ReasonInsecure {
		t.Fatalf("failed records %+v, want not found then insecure", failed)
	}
}

// With no daemon to record it, a change is refused and the vault is left as it was.
func TestVaultChangeNeedsTheDaemon(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	sock := filepath.Join(shortTempDir(t), "none.sock")
	code, _, errOut := withStdin(t, "made-up\n", "vault", "set", "--dir", dir, "--socket", sock, "--passphrase-file",
		passphraseFile(t, testVaultPassphrase), "--org", "acme", "github")
	if code != 1 || !strings.Contains(errOut, "the change was not made") {
		t.Fatalf("set without a daemon: %d %q", code, errOut)
	}
	if code, out, _ := lucy("vault", "ls", "--dir", dir, "--org", "acme"); code != 0 || out != "credentials[0]: acme holds none\n" {
		t.Fatalf("the vault changed without a record: %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "keys", "kek")); !os.IsNotExist(err) {
		t.Fatalf("the passphrase was set without a record: %v", err)
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
	if err := v.Unlock([]byte(testVaultPassphrase)); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("beta", "github", []byte("made-up")); err != nil {
		t.Fatal(err)
	}
	if _, err := (orgVault{v: v, org: "acme"}).Credential(context.Background(), "beta", "github"); err == nil {
		t.Fatal("the acme gateway read beta's credential")
	}
}

// testVaultPassphrase is the made-up passphrase test vaults use.
const testVaultPassphrase = "test passphrase, not a real one"

// passphraseFile writes pass to a new 0400 file outside any vault and returns its path.
func passphraseFile(t *testing.T, pass string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "passphrase")
	if err := os.WriteFile(p, []byte(pass+"\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	return p
}

// The vault passphrase comes from a file only its owner can read, outside the vault, or the
// terminal; a wrong one or an unsafe file changes nothing, and each
// attempt is recorded as a failed change: a wrong passphrase as locked.
func TestVaultPassphrase(t *testing.T) {
	sock := startDaemon(t)
	dir := filepath.Join(t.TempDir(), "vault")
	vaultEvents := func() int {
		code, out, _ := lucy("events", "--socket", sock, "--limit", "500")
		if code != 0 {
			t.Fatalf("events: %d", code)
		}
		return strings.Count(out, ",vault/acme,")
	}
	set := func(name string, args ...string) (int, string) {
		code, _, errOut := withStdin(t, "test_value\n", append(append([]string{"vault", "set", "--dir", dir, "--socket", sock}, args...), "--org", "acme", name)...)
		return code, errOut
	}

	// No file and no terminal: asked on the terminal, twice for a new vault, once after.
	var asked []bool
	promptPassphrase = func(confirm bool) ([]byte, error) {
		asked = append(asked, confirm)
		return []byte(testVaultPassphrase), nil
	}
	t.Cleanup(func() { promptPassphrase = promptTTY })
	if code, errOut := set("github"); code != 0 {
		t.Fatalf("set with a prompted passphrase: %d %q", code, errOut)
	}
	if code, errOut := set("npm"); code != 0 {
		t.Fatalf("set again: %d %q", code, errOut)
	}
	if len(asked) != 2 || !asked[0] || asked[1] {
		t.Fatalf("prompted %v; want a confirmation only when the vault had no passphrase", asked)
	}

	// A systemd credential directory is not looked in: with no file named, the terminal is asked.
	creds := t.TempDir()
	if err := os.WriteFile(filepath.Join(creds, "lucy-vault"), []byte(testVaultPassphrase), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	asked = nil
	if code, errOut := set("pypi"); code != 0 {
		t.Fatalf("set with CREDENTIALS_DIRECTORY set: %d %q", code, errOut)
	}
	if len(asked) != 1 {
		t.Fatalf("prompted %v with CREDENTIALS_DIRECTORY set; want the terminal asked once", asked)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	before := vaultEvents()

	wide := passphraseFile(t, testVaultPassphrase)
	if err := os.Chmod(wide, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(passphraseFile(t, testVaultPassphrase), link); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(dir, "passphrase")
	if err := os.WriteFile(inside, []byte(testVaultPassphrase), 0o400); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ file, want string }{
		"a wrong passphrase":      {passphraseFile(t, "another test passphrase"), "does not unlock this vault"},
		"a file others can read":  {wide, "only its owner may read it"},
		"a symlinked file":        {link, "not a plain file"},
		"a file inside the vault": {inside, "keep it outside the vault"},
		"a short passphrase":      {passphraseFile(t, "short"), "12 to 1024 bytes"},
	} {
		if code, errOut := set("github", "--passphrase-file", c.file); code != 1 || !strings.Contains(errOut, c.want) {
			t.Errorf("%s: %d %q", name, code, errOut)
		}
	}
	if n := vaultEvents(); n != before+10 {
		t.Fatalf("refused sets recorded %d vault events; want %d, each requested and failed", n, before+10)
	}
	code, out, _ := lucy("events", "--socket", sock, "--limit", "500")
	if code != 0 || !strings.Contains(out, ",vault/acme,") {
		t.Fatalf("events: %d", code)
	}
	c := client.New(sock, "lucy-test", "test")
	defer c.Close()
	page, err := c.Events(context.Background(), api.EventsParams{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	locked := 0
	for _, e := range page.Events {
		if e.Type == "vault.changed" && strings.Contains(string(e.Payload), vault.ReasonLocked) {
			locked++
		}
	}
	if locked != 2 {
		t.Fatalf("%d changes recorded as locked; want the wrong and the short passphrase's", locked)
	}

	// The gateway unlocks the vault at start: a wrong passphrase file stops it before it serves.
	if code, _, errOut := lucy("gateway", "--org", "acme", "--socket", sock, "--vault", dir, "--listen", filepath.Join(shortTempDir(t), "gw.sock"),
		"--passphrase-file", passphraseFile(t, "another test passphrase")); code != 1 || !strings.Contains(errOut, "does not unlock this vault") {
		t.Fatalf("a gateway with a wrong passphrase: %d %q", code, errOut)
	}
}
