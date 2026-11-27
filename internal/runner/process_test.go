package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/pathguard"
)

// TestMain doubles as the program the process executor runs: the executor passes LUCY_RUN, and
// LUCY_RUN_GOAL picks what the program does.
func TestMain(m *testing.M) {
	if os.Getenv("LUCY_RUN") != "" {
		os.Exit(helper(os.Getenv("LUCY_RUN_GOAL")))
	}
	os.Exit(m.Run())
}

func helper(goal string) int {
	switch goal {
	case "work":
		wd, _ := os.Getwd()
		fmt.Println("hello from", os.Getenv("LUCY_RUN"), "generation", os.Getenv("LUCY_RUN_GENERATION"))
		fmt.Println("tmpdir-is-cwd", os.Getenv("TMPDIR") == wd)
		fmt.Println("leaked", os.Getenv("LUCY_TEST_TOKEN") != "")
		fmt.Fprintln(os.Stderr, "a warning")
		fmt.Println("lucy:pr=12")
		fmt.Print("no newline")
		return 0
	case "fail":
		fmt.Println("giving up")
		return 3
	case "hang":
		time.Sleep(time.Minute)
		return 0
	case "spawn":
		exe, err := os.Executable()
		if err != nil {
			return 8
		}
		gc := exec.Command(exe)
		gc.Env = append(os.Environ(), "LUCY_RUN_GOAL=hang")
		if err := gc.Start(); err != nil {
			return 8
		}
		fmt.Println("grandchild", gc.Process.Pid)
		time.Sleep(time.Minute)
		return 0
	}
	return 9
}

type procFixture struct {
	root, home string
	ex         *ProcessExecutor
}

func newProc(t *testing.T) procFixture {
	t.Helper()
	base := t.TempDir()
	pf := procFixture{root: filepath.Join(base, "runner"), home: filepath.Join(base, "home")}
	if err := os.Mkdir(pf.home, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pf.ex = &ProcessExecutor{Root: pf.root, Home: pf.home, Command: []string{exe}, StopGrace: time.Second}
	return pf
}

func procLease(goal string) Lease {
	return Lease{Task: Task{Run: "f1#2", Org: "acme", Project: "web", Flight: "f1", Generation: 2, Goal: goal, Base: "main"},
		Runner: "r1", Fence: 7}
}

// drain polls an execution until it reports a result.
func drain(t *testing.T, x Execution) ([]string, Result) {
	t.Helper()
	var lines []string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		u := x.Poll()
		for _, e := range u.Events {
			lines = append(lines, e.Text)
		}
		if u.Result != nil {
			return lines, *u.Result
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the program did not finish")
	return nil, Result{}
}

func TestProcessExecutorRunsAndCleansUp(t *testing.T) {
	t.Setenv("LUCY_TEST_TOKEN", "do-not-pass-me")
	pf := newProc(t)
	x, err := pf.ex.Start(context.Background(), procLease("work"))
	if err != nil {
		t.Fatal(err)
	}
	lines, res := drain(t, x)
	if res.Outcome != OutcomeDone || res.PR != 12 {
		t.Fatalf("result %+v", res)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{"hello from f1#2 generation 2", "tmpdir-is-cwd true", "leaked false", "a warning", "no newline"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "lucy:pr=") {
		t.Error("the pull request directive was streamed as output")
	}
	entries, err := os.ReadDir(pf.root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the run's scratch directory was kept: %v, %v", entries, err)
	}
	if st, err := os.Stat(pf.root); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("root: %v, %v", st, err)
	}
}

func TestProcessExecutorFailsAndStops(t *testing.T) {
	pf := newProc(t)
	x, err := pf.ex.Start(context.Background(), procLease("fail"))
	if err != nil {
		t.Fatal(err)
	}
	if _, res := drain(t, x); res.Outcome != OutcomeFailed || !strings.Contains(res.Reason, "exit status 3") {
		t.Fatalf("result %+v", res)
	}

	x, err = pf.ex.Start(context.Background(), procLease("hang"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	x.Stop()
	if _, res := drain(t, x); res.Outcome != OutcomeStopped {
		t.Fatalf("result %+v", res)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("stop took too long")
	}
}

func TestProcessExecutorRefusesBadRoots(t *testing.T) {
	pf := newProc(t)
	for name, root := range map[string]string{
		"filesystem root": "/",
		"home":            pf.home,
		"home's parent":   filepath.Dir(pf.home),
		"relative":        "runner",
		"unclean":         pf.root + "/../runner",
	} {
		ex := *pf.ex
		ex.Root = root
		if _, err := ex.Start(context.Background(), procLease("work")); err == nil {
			t.Errorf("%s: started under root %q", name, root)
		}
	}
	ex := *pf.ex
	ex.Command = nil
	if _, err := ex.Start(context.Background(), procLease("work")); err == nil {
		t.Error("started with no command")
	}
}

// removeScratch deletes only inside its root: never the root, the home directory, "/" or a
// path that leaves the root through "..", a symlink or another directory.
func TestRemoveScratchStaysInsideRoot(t *testing.T) {
	pf := newProc(t)
	if err := os.MkdirAll(pf.root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(pf.root), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(pf.root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	guard, err := pathguard.New(pf.home, pathguard.Root{Kind: pathguard.Scratch, Path: pf.root})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", pf.home, pf.root, outside, link, pf.root + "/../outside", "relative"} {
		if err := removeScratch(guard, p); !errors.Is(err, pathguard.ErrRefused) {
			t.Errorf("removeScratch(%q) = %v, want refused", p, err)
		}
	}
	for _, p := range []string{pf.home, pf.root, outside} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted: %v", p, err)
		}
	}
	inside := filepath.Join(pf.root, "run-1")
	if err := os.MkdirAll(filepath.Join(inside, "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeScratch(guard, inside); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("inside the root was not deleted: %v", err)
	}
}
