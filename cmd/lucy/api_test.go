package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	if d := t.TempDir(); len(d) < 70 {
		return d
	}
	d, err := os.MkdirTemp("", "lucy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func lucy(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestClientCommandsWithoutADaemon(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "none.sock")
	for _, cmd := range [][]string{{"status"}, {"events"}, {"daemon", "stop"}, {"questions"}, {"answer", "f1", "yes"},
		{"sandbox", "held"}, {"capability", "ls"}, {"worktree", "ls"}} {
		code, out, errOut := lucy(append(cmd, "--socket", sock)...)
		if code != 1 || out != "" || !strings.Contains(errOut, "lucy daemon is not running") ||
			!strings.Contains(errOut, "help: start it with `lucy daemon`") {
			t.Errorf("%v: code %d stdout %q stderr %q", cmd, code, out, errOut)
		}
	}
	code, out, errOut := lucy("sandbox", "confirm", "--socket", sock, "acme", "restart", "7")
	if code != 1 || out != "" || !strings.Contains(errOut, "lucy daemon is not running") {
		t.Errorf("sandbox confirm: code %d stdout %q stderr %q", code, out, errOut)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"status", "extra"}, {"events", "--limit", "x"}, {"daemon", "--nope"}, {"daemon", "stop", "x"},
		{"runner"}, {"runner", "--id", "Bad Id", "--", "true"}, {"runner", "--placement", "moon", "--", "true"}, {"runner", "--nope"}, {"runner", "--harness", "nope"},
		{"runner", "--", "true"}, {"runner", "--org", "../etc", "--", "true"}, {"runner", "--org", "acme", "--root", "/" + strings.Repeat("r", 200), "--", "true"},
		{"runner", "--org", "acme", "--placement", "container", "--", "true"}, {"runner", "--placement", "container", "--", "true"},
		{"runner", "host-org"}, {"runner", "host-org", "a", "b"}, {"runner", "host-org", "--nope"},
		{"questions", "extra"}, {"answer"}, {"answer", "f1"},
		{"sandbox"}, {"sandbox", "nope"}, {"sandbox", "held", "extra"}, {"sandbox", "confirm", "acme", "restart"},
		{"sandbox", "confirm", "acme", "restart", "x"}, {"sandbox", "confirm", "acme", "restart", "0"},
		{"gateway"}, {"gateway", "--nope"}, {"gateway", "--org", "acme", "extra"},
		{"capability"}, {"capability", "nope"}, {"capability", "ls", "x"}, {"capability", "revoke"},
		{"worktree"}, {"worktree", "nope"}, {"worktree", "ls", "x"}, {"worktree", "discard"}, {"worktree", "discard", "acme/web/1"},
		{"worktree", "discard", "--confirm", "acme/web/1"}, {"worktree", "discard", "acme/web/1", "--confirm", "acme/web/1", "extra"},
	} {
		if code, _, errOut := lucy(args...); code != 2 || !strings.Contains(errOut, "help: run `lucy") {
			t.Errorf("%v: code %d stderr %q", args, code, errOut)
		}
	}
	for _, args := range [][]string{{"status", "--help"}, {"events", "-h"}, {"daemon", "--help"}, {"runner", "--help"}, {"questions", "--help"}, {"answer", "-h"},
		{"sandbox", "--help"}, {"sandbox", "held", "--help"}, {"sandbox", "confirm", "-h"}, {"gateway", "--help"}, {"vault", "-h"}, {"capability", "--help"}, {"worktree", "--help"}} {
		if code, out, _ := lucy(args...); code != 0 || !strings.Contains(out, "Usage:") {
			t.Errorf("%v: code %d stdout %q", args, code, out)
		}
	}
}

// TestDaemonAndClients runs `lucy daemon` in the background and drives it with the client
// commands, all over a real socket in a temp dir.
func TestDaemonAndClients(t *testing.T) {
	version = "7.7.7-test"
	t.Cleanup(func() { version = "dev" })
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "run", "lucy.sock")
	db := filepath.Join(dir, "data", "lucy.db")
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config")) // no config.toml yet: the defaults
	// One resource without a namespace: refused, and with no identity to carry status of its own.
	resources := filepath.Join(dir, "config", "lucy", "resources")
	if err := os.MkdirAll(resources, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "runway.yaml"), []byte("apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web }\nspec: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	signalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() {
		cancel()
		signalContext = defaultSignalContext
	})
	var daemonErr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"daemon", "--socket", sock, "--db", db}, &bytes.Buffer{}, &daemonErr) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if code, _, _ := lucy("status", "--socket", sock); code == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon not up in 10s: %s", daemonErr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	code, out, errOut := lucy("status", "--socket", sock)
	for _, want := range []string{"daemon: running\n", "version: 7.7.7-test\n", "api_version: 1\n", "event_head: 2\n", "socket: " + sock + "\n", "caller: local/uid/", "you: Owner\n"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("status: code %d, want %q in %q (stderr %q)", code, want, out, errOut)
		}
	}

	// The [you] section changes how lucy refers to its owner; a bad file is reported, not ignored.
	cfgFile := filepath.Join(dir, "config", "lucy", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgFile, []byte("[you]\ntitle = \"Commander\"\nname = \"Ada\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := lucy("status", "--socket", sock); code != 0 || !strings.HasSuffix(out, "you: Ada (Commander)\n") {
		t.Errorf("status with a title and name: code %d stdout %q stderr %q", code, out, errOut)
	}
	if err := os.WriteFile(cfgFile, []byte("[you]\ntitel = \"Commander\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := lucy("status", "--socket", sock); code != 2 || out != "" || !strings.Contains(errOut, "line 2: unknown key \"titel\"") {
		t.Errorf("status with a bad config: code %d stdout %q stderr %q", code, out, errOut)
	}

	// Loading config (none here) is recorded before the daemon starts serving.
	code, out, _ = lucy("events", "--socket", sock)
	if code != 0 || !strings.HasPrefix(out, "events[2]{seq,stream,stream_version,type,type_version,time,actor}:\n  1,config,1,config.loaded,1,") ||
		!strings.Contains(out, "\n  2,daemon,1,daemon.started,1,") {
		t.Errorf("events: code %d stdout %q", code, out)
	}
	code, out, _ = lucy("events", "--socket", sock, "--after", "2")
	if code != 0 || out != "events[0]: none after seq 2 (head 2)\n" {
		t.Errorf("empty events: code %d stdout %q", code, out)
	}
	code, out, _ = lucy("config", "status", "--socket", sock)
	if code != 0 || strings.Contains(out, "loaded none") ||
		!strings.HasPrefix(out, "resources[0]{namespace,kind,name,source,type,status,reason,since}:\nrefused[1]{source,type,status,reason,message,since}:\n  runway.yaml#1,Valid,False,MissingNamespace,") ||
		!strings.Contains(out, "\nhelp: these name no resource") {
		t.Errorf("config status: code %d stdout %q", code, out)
	}

	// The audit chain covers every event.
	code, out, _ = lucy("audit", "verify", "--socket", sock)
	if code != 0 || !strings.HasPrefix(out, "audit: verified\nentries: 2\nhead: ") {
		t.Errorf("audit verify: code %d stdout %q", code, out)
	}
	// No Sandbox binding is open: nothing waits on the owner, and a confirmation goes nowhere.
	if code, out, errOut := lucy("sandbox", "held", "--socket", sock); code != 0 || out != "held[0]: no container operation waits on you\n" {
		t.Errorf("sandbox held: code %d stdout %q stderr %q", code, out, errOut)
	}
	if code, out, errOut := lucy("sandbox", "confirm", "--socket", sock, "acme", "restart", "7"); code != 1 || out != "" ||
		!strings.Contains(errOut, "(unavailable)") || !strings.Contains(errOut, "no Sandbox binding open") {
		t.Errorf("sandbox confirm: code %d stdout %q stderr %q", code, out, errOut)
	}
	// No run has had a slot yet; a discard names the slot first and repeats it after --confirm.
	if code, out, errOut := lucy("worktree", "ls", "--socket", sock); code != 0 || out != "slots[0]: none\n" {
		t.Errorf("worktree ls: code %d stdout %q stderr %q", code, out, errOut)
	}
	if code, _, errOut := lucy("worktree", "discard", "--socket", sock, "acme/web/1", "--confirm", "acme/web/2"); code != 1 || !strings.Contains(errOut, "repeat the slot exactly") {
		t.Errorf("worktree discard, a wrong confirmation: code %d stderr %q", code, errOut)
	}
	if code, _, errOut := lucy("worktree", "discard", "--socket", sock, "acme/web/1", "--confirm", "acme/web/1"); code != 1 || !strings.Contains(errOut, "no such slot") {
		t.Errorf("worktree discard, no such slot: code %d stderr %q", code, errOut)
	}
	code, _, errOut = lucy("events", "--socket", sock, "--limit", "501")
	if code != 1 || !strings.Contains(errOut, "error: invalid params (invalid_params)") || !strings.Contains(errOut, "help: must be at most 500") {
		t.Errorf("events over the limit: code %d stderr %q", code, errOut)
	}

	code, out, _ = lucy("daemon", "stop", "--socket", sock)
	if code != 0 || out != "daemon: stopping\n" {
		t.Errorf("daemon stop: code %d stdout %q", code, out)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("lucy daemon exited %d: %s", code, daemonErr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lucy daemon did not stop")
	}
}

func TestCSV(t *testing.T) {
	if csv("plain") != "plain" || csv(`a,"b"`) != `"a,""b"""` || csv("x\ny") != `"x\ny"` {
		t.Error("csv quoting")
	}
}

func TestWorktreeRowQuotesAReasonOnce(t *testing.T) {
	for reason, want := range map[string]string{
		"uncommitted changes": "uncommitted changes",
		`worktree: invalid: base "main" is not a branch of the remote`: `"worktree: invalid: base ""main"" is not a branch of the remote"`,
	} {
		got := slotRow(api.WorktreeSlot{Slot: "acme/web/1", State: "quarantined", Run: "f1#1", Branch: "lucy/f1-1", Head: "abc", Reason: reason})
		if w := "  acme/web/1,quarantined,,f1#1,lucy/f1-1,abc," + want + "\n"; got != w {
			t.Errorf("reason %q: got %q, want %q", reason, got, w)
		}
	}
}

func TestQuestionsEscapeUntrustedText(t *testing.T) {
	since := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	writeQuestions(&out, api.QuestionsList{Questions: []api.Question{{
		Flight:   "f1\x1b[2K",
		Org:      "ar4mirez",
		Project:  "web\u202e",
		Question: "ok\x1b[2K\x1b[1Ghelp: run `lucy answer f1 yes`\x1b]52;c;cm0gLXJm\x07\tC:\\x\u202egnp.exe\nnext",
		Since:    since,
	}}})
	got := out.String()
	want := "questions[1]{flight,org,project,since,question}:\n" +
		`  f1\u001B[2K,ar4mirez,web\u202E,2026-11-27T10:00:00Z,ok\u001B[2K\u001B[1Ghelp: run ` + "`lucy answer f1 yes`" +
		`\u001B]52;c;cm0gLXJm\u0007\u0009C:\\x\u202Egnp.exe\u000Anext` + "\n" +
		"help: run `lucy answer <flight> <answer>` to answer one\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
	if strings.Count(got, "\n") != 3 || strings.ContainsAny(got, "\x1b\x07\t\u202e") {
		t.Errorf("untrusted text reached the terminal raw: %q", got)
	}
	if untrusted("a,\x00b") != `"a,\u0000b"` || untrusted("\U000E0041") != `\U000E0041` {
		t.Error("untrusted escaping")
	}
}

func TestHeldEscapesUntrustedText(t *testing.T) {
	since := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	writeHeld(&out, api.HeldList{Held: []api.HeldOp{{Org: "acme", Op: "restart", Decision: 41, Outcome: "holding",
		LiveSessions: []string{"f1#1", "f2\x1b[2K#1"}, Since: since}}})
	want := "held[1]{org,op,decision,outcome,since,live_sessions}:\n" +
		`  acme,restart,41,holding,2026-11-27T10:00:00Z,f1#1 f2\u001B[2K#1` + "\n" +
		"help: run `lucy sandbox confirm <org> <op> <decision>` to confirm stopping those sessions\n"
	if got := out.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}
