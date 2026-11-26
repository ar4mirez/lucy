// Package selfupdate implements `lucy self-update`: it updates lucy through mise, the tool that
// installed it (D7, LUCY-REL-006).
//
// lucy does not download anything itself. It checks that the running binary lives in mise's
// install tree for github:ar4mirez/lucy, then runs mise with its GitHub attestation and SLSA checks
// forced on, so a user setting cannot switch off verification of the new release.
package selfupdate

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Tool is lucy's mise tool spec.
const Tool = "github:ar4mirez/lucy"

// installDir is the directory mise installs Tool's versions under: <data>/installs/<installDir>/<v>.
const installDir = "github-ar4mirez-lucy"

// verifyEnv forces mise's release verification on for the commands lucy runs.
var verifyEnv = []string{
	"MISE_GITHUB_GITHUB_ATTESTATIONS=true",
	"MISE_GITHUB_SLSA=true",
	"MISE_GITHUB_ATTESTATIONS=true",
	"MISE_SLSA=true",
}

// versionRe accepts a release version as lucy tags it, with or without a leading "v".
var versionRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

const usage = `Usage:
  lucy self-update               update lucy to the latest release through mise
  lucy self-update --to <ver>    install release <ver> instead (roll back or pin)
  lucy self-update --check       report whether a newer release exists; change nothing

lucy must have been installed by mise: mise use -g github:ar4mirez/lucy@latest
mise verifies the release's GitHub attestation before it installs it.
`

// Updater runs self-update. The zero value is not usable; use New or set every field.
type Updater struct {
	// Version is the running lucy's version.
	Version string
	// Executable returns the running binary's path (os.Executable).
	Executable func() (string, error)
	// LookPath finds a command on PATH (exec.LookPath).
	LookPath func(string) (string, error)
	// Env is the environment mise runs with (os.Environ()); verifyEnv is appended to it.
	Env []string
}

// New returns an Updater for the running process.
func New(version string) Updater {
	return Updater{Version: version, Executable: os.Executable, LookPath: exec.LookPath, Env: os.Environ()}
}

// Run executes `lucy self-update` with args and returns the process exit code:
// 0 on success, 1 when the update cannot run or mise fails, 2 on a usage error.
func (u Updater) Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("self-update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	to := fs.String("to", "", "")
	check := fs.Bool("check", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return 0
		}
		fmt.Fprintf(stderr, "error: %v\nhelp: run `lucy self-update --help`\n", err)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected argument %q\nhelp: run `lucy self-update --help`\n", fs.Arg(0))
		return 2
	}
	if *check && *to != "" {
		fmt.Fprint(stderr, "error: --check and --to cannot be combined\nhelp: run `lucy self-update --help`\n")
		return 2
	}
	target := "latest"
	if *to != "" {
		if !versionRe.MatchString(*to) {
			fmt.Fprintf(stderr, "error: --to %q is not a release version such as 0.2.0\nhelp: list releases with `mise ls-remote %s`\n", *to, Tool)
			return 2
		}
		target = strings.TrimPrefix(*to, "v")
	}

	mise, err := u.preflight()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if *check {
		return u.check(mise, stdout, stderr)
	}
	return u.update(mise, target, stdout, stderr)
}

// preflight returns mise's path once it has confirmed lucy was installed by mise.
func (u Updater) preflight() (string, error) {
	exe, err := u.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find the running lucy binary: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if !InstalledByMise(exe) {
		return "", fmt.Errorf("%s was not installed by mise, so lucy cannot update it\n"+
			"help: install lucy with `mise use -g %s@latest`, or update your own build from source", exe, Tool)
	}
	mise, err := u.LookPath("mise")
	if err != nil {
		return "", fmt.Errorf("mise is not on PATH, but lucy was installed by it\n" +
			"help: put mise on PATH (https://mise.jdx.dev), then run `lucy self-update` again")
	}
	return mise, nil
}

// InstalledByMise reports whether exe is inside mise's install tree for Tool.
func InstalledByMise(exe string) bool {
	parts := strings.Split(filepath.ToSlash(exe), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "installs" && parts[i+1] == installDir {
			return true
		}
	}
	return false
}

func (u Updater) check(mise string, stdout, stderr io.Writer) int {
	var out, errOut bytes.Buffer
	if err := u.mise(mise, &out, &errOut, "latest", Tool); err != nil {
		fmt.Fprintf(stderr, "error: `mise latest %s` failed: %v\n%s", Tool, err, errOut.String())
		return 1
	}
	latest := strings.TrimPrefix(strings.TrimSpace(out.String()), "v")
	current := strings.TrimPrefix(u.Version, "v")
	if !versionRe.MatchString(latest) {
		fmt.Fprintf(stderr, "error: mise reported %q as the latest release, which is not a version\n", latest)
		return 1
	}
	if latest == current {
		fmt.Fprintf(stdout, "lucy %s is the latest release\n", current)
		return 0
	}
	fmt.Fprintf(stdout, "lucy %s is installed; the latest release is %s\nhelp: run `lucy self-update`\n", current, latest)
	return 0
}

func (u Updater) update(mise, target string, stdout, stderr io.Writer) int {
	spec := Tool + "@" + target
	fmt.Fprintf(stdout, "lucy: running `mise use -g %s` (attestation checks on)\n", spec)
	if err := u.mise(mise, stdout, stderr, "use", "-g", spec); err != nil {
		fmt.Fprintf(stderr, "error: `mise use -g %s` failed: %v\nhelp: lucy %s is unchanged; see the mise output above\n", spec, err, u.Version)
		return 1
	}
	fmt.Fprintf(stdout, "lucy: installed %s through mise\n", spec)
	if previous := strings.TrimPrefix(u.Version, "v"); versionRe.MatchString(previous) && previous != target {
		fmt.Fprintf(stdout, "help: run `lucy --version` to confirm; roll back with `lucy self-update --to %s`\n", previous)
	} else {
		fmt.Fprint(stdout, "help: run `lucy --version` to confirm\n")
	}
	return 0
}

// mise runs mise with args, its verification settings forced on.
func (u Updater) mise(mise string, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.Command(mise, args...)
	cmd.Env = append(append([]string{}, u.Env...), verifyEnv...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
