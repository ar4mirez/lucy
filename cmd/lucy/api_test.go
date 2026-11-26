package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	for _, cmd := range [][]string{{"status"}, {"events"}, {"daemon", "stop"}} {
		code, out, errOut := lucy(append(cmd, "--socket", sock)...)
		if code != 1 || out != "" || !strings.Contains(errOut, "lucy daemon is not running") ||
			!strings.Contains(errOut, "help: start it with `lucy daemon`") {
			t.Errorf("%v: code %d stdout %q stderr %q", cmd, code, out, errOut)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"status", "extra"}, {"events", "--limit", "x"}, {"daemon", "--nope"}, {"daemon", "stop", "x"},
	} {
		if code, _, errOut := lucy(args...); code != 2 || !strings.Contains(errOut, "help: run `lucy") {
			t.Errorf("%v: code %d stderr %q", args, code, errOut)
		}
	}
	for _, args := range [][]string{{"status", "--help"}, {"events", "-h"}, {"daemon", "--help"}} {
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
	for _, want := range []string{"daemon: running\n", "version: 7.7.7-test\n", "api_version: 1\n", "event_head: 1\n", "socket: " + sock + "\n", "caller: local/uid/"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("status: code %d, want %q in %q (stderr %q)", code, want, out, errOut)
		}
	}

	code, out, _ = lucy("events", "--socket", sock)
	if code != 0 || !strings.HasPrefix(out, "events[1]{seq,stream,stream_version,type,type_version,time,actor}:\n  1,daemon,1,daemon.started,1,") {
		t.Errorf("events: code %d stdout %q", code, out)
	}
	code, out, _ = lucy("events", "--socket", sock, "--after", "1")
	if code != 0 || out != "events[0]: none after seq 1 (head 1)\n" {
		t.Errorf("empty events: code %d stdout %q", code, out)
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
