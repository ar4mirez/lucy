//go:build unix

package runner

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// grandchild polls x until the program reports the pid of the child it started.
func grandchild(t *testing.T, x Execution) (int, []string) {
	t.Helper()
	var lines []string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range x.Poll().Events {
			lines = append(lines, e.Text)
			if v, ok := strings.CutPrefix(e.Text, "grandchild "); ok {
				if pid, err := strconv.Atoi(v); err == nil && pid > 0 {
					return pid, lines
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	x.Stop()
	t.Fatal("the program did not report its child")
	return 0, nil
}

// requireGone fails unless pid exits soon; it kills pid if it does not.
func requireGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the program's child %d outlived the run", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Stopping a run kills what the program started too, not only the program.
func TestProcessExecutorStopKillsDescendants(t *testing.T) {
	pf := newProc(t)
	x, err := pf.ex.Start(context.Background(), procLease("spawn"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := grandchild(t, x)
	x.Stop()
	if _, res := drain(t, x); res.Outcome != OutcomeStopped {
		t.Fatalf("result %+v", res)
	}
	requireGone(t, pid)
}

// A program that exits 0 while a child it started still holds its output is done, keeps its pull
// request, and its child does not outlive the run.
func TestProcessExecutorExitKillsDescendants(t *testing.T) {
	pf := newProc(t)
	x, err := pf.ex.Start(context.Background(), procLease("detach"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := grandchild(t, x)
	if _, res := drain(t, x); res.Outcome != OutcomeDone || res.PR != 5 {
		t.Fatalf("result %+v", res)
	}
	requireGone(t, pid)
}
