package selfupdate

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMise is a mise stand-in that uses only shell builtins, so PATH can hold nothing else and no
// test can reach the network. It logs each call's arguments and the verification settings it saw.
const fakeMise = `#!/bin/sh
printf 'args=%s attest=%s slsa=%s\n' "$*" "$MISE_GITHUB_GITHUB_ATTESTATIONS" "$MISE_GITHUB_SLSA" >> "$FAKE_MISE_LOG"
if [ "$1" = "--version" ]; then
	if [ -n "$FAKE_MISE_VERSION_FAIL" ]; then
		echo "mise: cannot start" >&2
		exit 4
	fi
	echo "${FAKE_MISE_VERSION-2026.11.12 linux-x64 (2026-11-21)}"
	exit 0
fi
if [ -n "$FAKE_MISE_FAIL" ]; then
	echo "mise: something broke" >&2
	exit 3
fi
case "$1" in
latest) echo "${FAKE_MISE_LATEST:-0.2.0}" ;;
use) echo "mise github:ar4mirez/lucy installed" ;;
*) echo "fake mise: unexpected $1" >&2; exit 9 ;;
esac
`

type fixture struct {
	u   Updater
	log string
}

// newFixture puts a fake mise alone on PATH and a lucy binary inside a mise install tree.
func newFixture(t *testing.T, env ...string) fixture {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "mise"), []byte(fakeMise), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	exe := filepath.Join(dir, "data", "installs", installDir, "0.1.0", "lucy")
	if err := os.MkdirAll(filepath.Dir(exe), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "mise.log")
	u := Updater{
		Version:    "0.1.0",
		Executable: func() (string, error) { return exe, nil },
		LookPath:   exec.LookPath,
		// A user who switched mise's verification off must not switch it off for lucy.
		Env: append([]string{"PATH=" + bin, "FAKE_MISE_LOG=" + log,
			"MISE_GITHUB_GITHUB_ATTESTATIONS=false", "MISE_GITHUB_SLSA=false"}, env...),
	}
	return fixture{u: u, log: log}
}

func (f fixture) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(f.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestRun(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		env        []string
		wantCode   int
		wantCalls  []string
		wantStdout string
		wantStderr string
	}{
		{
			name:       "update to latest",
			wantCalls:  []string{"args=--version attest=true slsa=true", "args=use -g github:ar4mirez/lucy@latest attest=true slsa=true"},
			wantStdout: "installed github:ar4mirez/lucy@latest through mise",
		},
		{
			name:       "roll back with --to",
			args:       []string{"--to", "v0.0.9"},
			wantCalls:  []string{"args=--version attest=true slsa=true", "args=use -g github:ar4mirez/lucy@0.0.9 attest=true slsa=true"},
			wantStdout: "roll back with `lucy self-update --to 0.1.0`",
		},
		{
			name:       "check finds a newer release",
			args:       []string{"--check"},
			wantCalls:  []string{"args=latest github:ar4mirez/lucy attest=true slsa=true"},
			wantStdout: "the latest release is 0.2.0",
		},
		{
			name:       "check when up to date",
			args:       []string{"--check"},
			env:        []string{"FAKE_MISE_LATEST=v0.1.0"},
			wantCalls:  []string{"args=latest github:ar4mirez/lucy attest=true slsa=true"},
			wantStdout: "lucy 0.1.0 is the latest release",
		},
		{
			name:       "check rejects a non-version answer",
			args:       []string{"--check"},
			env:        []string{"FAKE_MISE_LATEST=no releases"},
			wantCode:   1,
			wantCalls:  []string{"args=latest github:ar4mirez/lucy attest=true slsa=true"},
			wantStderr: "not a version",
		},
		{
			name:       "mise fails",
			env:        []string{"FAKE_MISE_FAIL=1"},
			wantCode:   1,
			wantCalls:  []string{"args=--version attest=true slsa=true", "args=use -g github:ar4mirez/lucy@latest attest=true slsa=true"},
			wantStderr: "mise: something broke",
		},
		{
			name:       "minimum mise accepted",
			env:        []string{"FAKE_MISE_VERSION=2026.10.19 macos-arm64 (2026-10-19)"},
			wantCalls:  []string{"args=--version attest=true slsa=true", "args=use -g github:ar4mirez/lucy@latest attest=true slsa=true"},
			wantStdout: "(attestation checks on)",
		},
		{
			name:       "newer mise accepted",
			env:        []string{"FAKE_MISE_VERSION=2027.1.0 linux-x64 (2027-01-02)"},
			wantCalls:  []string{"args=--version attest=true slsa=true", "args=use -g github:ar4mirez/lucy@latest attest=true slsa=true"},
			wantStdout: "installed github:ar4mirez/lucy@latest",
		},
		{
			name:       "old mise refused",
			env:        []string{"FAKE_MISE_VERSION=2026.10.18 linux-x64 (2026-10-18)"},
			wantCode:   1,
			wantCalls:  []string{"args=--version attest=true slsa=true"},
			wantStderr: "mise 2026.10.18 is older than 2026.10.19",
		},
		{
			name:       "old mise refused for --to",
			args:       []string{"--to", "0.0.9"},
			env:        []string{"FAKE_MISE_VERSION=2025.12.31"},
			wantCode:   1,
			wantCalls:  []string{"args=--version attest=true slsa=true"},
			wantStderr: "`mise self-update`",
		},
		{
			name:       "unparseable mise version refused",
			env:        []string{"FAKE_MISE_VERSION=mise nightly"},
			wantCode:   1,
			wantCalls:  []string{"args=--version attest=true slsa=true"},
			wantStderr: "cannot read a version",
		},
		{
			name:       "mise --version fails",
			env:        []string{"FAKE_MISE_VERSION_FAIL=1"},
			wantCode:   1,
			wantCalls:  []string{"args=--version attest=true slsa=true"},
			wantStderr: "`mise --version` failed",
		},
		{name: "help", args: []string{"--help"}, wantStdout: "lucy self-update --to <ver>"},
		{name: "bad version", args: []string{"--to", "latest; rm -rf"}, wantCode: 2, wantStderr: "is not a release version"},
		{name: "check with --to", args: []string{"--check", "--to", "0.1.0"}, wantCode: 2, wantStderr: "cannot be combined"},
		{name: "stray argument", args: []string{"now"}, wantCode: 2, wantStderr: `unexpected argument "now"`},
		{name: "unknown flag", args: []string{"--force"}, wantCode: 2, wantStderr: "flag provided but not defined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.env...)
			var out, errOut bytes.Buffer
			code := f.u.Run(tc.args, &out, &errOut)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tc.wantCode, errOut.String())
			}
			if got := f.calls(t); strings.Join(got, "\n") != strings.Join(tc.wantCalls, "\n") {
				t.Errorf("mise calls = %q, want %q", got, tc.wantCalls)
			}
			if !strings.Contains(out.String(), tc.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tc.wantStdout)
			}
			if !strings.Contains(errOut.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tc.wantStderr)
			}
		})
	}
}

func TestRunRefusesWhenNotInstalledByMise(t *testing.T) {
	f := newFixture(t)
	f.u.Executable = func() (string, error) { return filepath.Join(t.TempDir(), "bin", "lucy"), nil }
	var out, errOut bytes.Buffer
	if code := f.u.Run(nil, &out, &errOut); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "was not installed by mise") || !strings.Contains(errOut.String(), "mise use -g github:ar4mirez/lucy@latest") {
		t.Errorf("stderr = %q, want the not-installed-by-mise error and the install command", errOut.String())
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("mise was called: %q", calls)
	}
}

func TestRunNeedsMiseOnPath(t *testing.T) {
	f := newFixture(t)
	t.Setenv("PATH", t.TempDir())
	var out, errOut bytes.Buffer
	if code := f.u.Run(nil, &out, &errOut); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "mise is not on PATH") {
		t.Errorf("stderr = %q, want the mise-not-on-PATH error", errOut.String())
	}
}

func TestRunFollowsShimSymlink(t *testing.T) {
	f := newFixture(t)
	real, _ := f.u.Executable()
	link := filepath.Join(t.TempDir(), "lucy")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	f.u.Executable = func() (string, error) { return link, nil }
	var out, errOut bytes.Buffer
	if code := f.u.Run([]string{"--check"}, &out, &errOut); code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr %q)", code, errOut.String())
	}
}

func TestInstalledByMise(t *testing.T) {
	cases := map[string]bool{
		"/home/a/.local/share/mise/installs/github-ar4mirez-lucy/0.1.0/lucy":   true,
		"/Users/a/.local/share/mise/installs/github-ar4mirez-lucy/latest/lucy": true,
		"/opt/mise/installs/github-ar4mirez-lucy/0.1.0/bin/lucy":               true,
		"/home/a/.local/share/mise/installs/github-ar4mirez-lucy":              false,
		"/home/a/.local/share/mise/installs/ubi-ar4mirez-lucy/0.1.0/lucy":      false,
		"/home/a/src/lucy/bin/lucy":                                            false,
		"/usr/local/bin/lucy":                                                  false,
	}
	for path, want := range cases {
		if got := InstalledByMise(path); got != want {
			t.Errorf("InstalledByMise(%q) = %v, want %v", path, got, want)
		}
	}
}
