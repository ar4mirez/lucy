package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSim(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	fly := func(args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := run(append([]string{"sim"}, args...), &out, &errOut); code != 0 {
			t.Fatalf("lucy sim %v: exit %d: %s", args, code, errOut.String())
		}
		return out.String()
	}
	a := fly("--seed", "7", "--log")
	for _, want := range []string{"seed: 7\n", "flights[7]{id,state,reason,pr,pilots}:\n", "  a-takeoff,takeoff,,1,1\n",
		"  c-red,grounded,stalled,", "  f-crash,takeoff,,", ",2\n", "task.takeoff\n", "policy,policy.decision\n"} {
		if !strings.Contains(a, want) {
			t.Errorf("output lacks %q:\n%s", want, a)
		}
	}
	if b := fly("--seed", "7", "--log"); a != b {
		t.Error("the same seed printed different output")
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"sim", "extra"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), `unexpected argument "extra"`) {
		t.Errorf("exit %d, stderr %q", code, errOut.String())
	}
	if code := run([]string{"sim", "--help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "lucy sim [--seed <n>]") {
		t.Errorf("help: exit %d", code)
	}
}
