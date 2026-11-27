package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/runner"
)

// The default runner id is a valid id that names the process, for any org, however long.
func TestDefaultRunnerID(t *testing.T) {
	pid := "-" + strconv.Itoa(os.Getpid())
	for _, org := range []string{"acme", "Ar4mirez", "_x", "", strings.Repeat("o", 200)} {
		id := defaultRunnerID(org)
		if err := runner.CheckRunnerID(id); err != nil || !strings.HasSuffix(id, pid) {
			t.Errorf("org %q: id %q, %v", org, id, err)
		}
	}
	if a, b := defaultRunnerID("acme"), defaultRunnerID("beta"); a == b {
		t.Errorf("two orgs share the id %q", a)
	}
}

// A --root that is short as written but resolves through a symlink to a path too long for a run's
// gateway socket is refused before the runner starts.
func TestRunnerRefusesARootTooLongOnceResolved(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 90))
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	links, err := os.MkdirTemp("", "l")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(links) })
	link := filepath.Join(links, "r")
	if err := os.Symlink(long, link); err != nil {
		t.Fatal(err)
	}
	if err := runner.CheckRunGatewayRoot(link); err != nil {
		t.Fatalf("the link itself is short enough: %v", err)
	}
	code, _, errOut := lucy("runner", "--org", "acme", "--root", link, "--socket", filepath.Join(links, "none.sock"), "--", "true")
	if code != 2 || !strings.Contains(errOut, "--root") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

// A runner that declares container placement is refused before it dials the daemon, naming the
// proof it lacks.
func TestRunnerRefusesSelfDeclaredContainerPlacement(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "none.sock")
	code, _, errOut := lucy("runner", "--org", "acme", "--placement", "container", "--socket", sock, "--", "true")
	if code != 2 || !strings.Contains(errOut, "container placement needs berth#216 placement proof") || strings.Contains(errOut, "not running") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}
