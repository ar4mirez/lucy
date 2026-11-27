package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitTimeout bounds one git command: a fetch of a large repository is the slowest.
const gitTimeout = 10 * time.Minute

// git runs git commands for the pool with an environment of its own: no system or user
// configuration, no prompt for credentials, a plain C locale, and only the https, ssh and file
// transports, so a remote can never name a transport that runs a command (ext::).
type git struct {
	path string // the git program; "git" on PATH when empty
	home string // the HOME git sees: the pool's own directory, never the user's
}

func (g git) env() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + g.home,
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=https:ssh:file",
		"GIT_AUTHOR_NAME=lucy", "GIT_AUTHOR_EMAIL=lucy@localhost",
		"GIT_COMMITTER_NAME=lucy", "GIT_COMMITTER_EMAIL=lucy@localhost",
	}
}

// run runs git with args in dir and returns its trimmed standard output.
func (g git) run(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	prog := g.path
	if prog == "" {
		prog = "git"
	}
	cmd := exec.CommandContext(ctx, prog, args...) // #nosec G204 -- git with the pool's own arguments; every name in them is checked first
	cmd.Dir, cmd.Env = dir, g.env()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 512 {
			msg = msg[:512]
		}
		return "", fmt.Errorf("worktree: git %s: %w: %s", args[0], err, msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// isAncestor reports whether commit a is an ancestor of (or is) b.
func (g git) isAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	_, err := g.run(ctx, dir, "merge-base", "--is-ancestor", "--end-of-options", a, b)
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}
