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

// Stopping a run kills what the program started too, not only the program.
func TestProcessExecutorStopKillsDescendants(t *testing.T) {
	pf := newProc(t)
	x, err := pf.ex.Start(context.Background(), procLease("spawn"))
	if err != nil {
		t.Fatal(err)
	}
	pid := 0
	deadline := time.Now().Add(20 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		for _, e := range x.Poll().Events {
			if v, ok := strings.CutPrefix(e.Text, "grandchild "); ok {
				pid, _ = strconv.Atoi(v)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pid <= 0 {
		x.Stop()
		t.Fatal("the program did not report its child")
	}
	x.Stop()
	if _, res := drain(t, x); res.Outcome != OutcomeStopped {
		t.Fatalf("result %+v", res)
	}
	deadline = time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the program's child %d outlived the stop", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
