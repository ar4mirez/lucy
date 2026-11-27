package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/vault"
)

const vaultUsage = `Usage:
  lucy vault set [--dir <dir>] --org <org> <name>    the value is read from stdin
  lucy vault ls  [--dir <dir>] --org <org>
  lucy vault rm  [--dir <dir>] --org <org> <name>

The per-org credential vault: credentials encrypted at rest with a key per org, read only by
that org's credential gateway (` + "`lucy gateway`" + `). A route's credential is the name it
has here.

  set   stores a credential, replacing one of the same name. Its value comes from stdin, never
        an argument or the environment, so it is in no shell history or process list; pipe it in
        (for example from your password manager). lucy refuses to read it from a terminal, where
        it would show as you type. One trailing newline is dropped.
  ls    lists the org's credentials by name and when each was set; never a value.
  rm    deletes one credential.

The vault is vault/ in lucy's data directory by default: keys/ holds each org's key, orgs/ the
sealed credentials, all only its owner can open.
`

// stdin and stdinIsTerminal are the command's input; tests replace them.
var (
	stdin           io.Reader = os.Stdin
	stdinIsTerminal           = func() bool {
		fi, err := os.Stdin.Stat()
		return err == nil && fi.Mode()&os.ModeCharDevice != 0
	}
)

// defaultVaultDir is vault/ in lucy's data directory.
func defaultVaultDir() (string, error) {
	data, err := eventstore.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "vault"), nil
}

func openVault(dir string) (*vault.Vault, error) {
	if dir == "" {
		var err error
		if dir, err = defaultVaultDir(); err != nil {
			return nil, err
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return vault.Open(dir)
}

func runVault(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "error: lucy vault needs set, ls or rm\nhelp: run `lucy vault --help`\n")
		return 2
	}
	sub := args[0]
	switch sub {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, vaultUsage)
		return 0
	case "set", "ls", "rm":
	default:
		fmt.Fprintf(stderr, "error: unknown vault command %q\nhelp: run `lucy vault --help`\n", sub)
		return 2
	}
	fs := flag.NewFlagSet("vault "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", "", "")
	org := fs.String("org", "", "")
	if code, ok := parseFlagsAllowingArgs(fs, args[1:], vaultUsage, "lucy vault --help", stdout, stderr); !ok {
		return code
	}
	want := map[string]int{"set": 1, "ls": 0, "rm": 1}[sub]
	if *org == "" || fs.NArg() != want {
		fmt.Fprintf(stderr, "error: lucy vault %s needs --org%s\nhelp: run `lucy vault --help`\n", sub,
			map[bool]string{true: " and a credential name", false: ""}[want == 1])
		return 2
	}
	if e := vault.CheckLabel(*org); e != "" {
		fmt.Fprintf(stderr, "error: --org %q %s\nhelp: run `lucy vault --help`\n", *org, e)
		return 2
	}
	name := fs.Arg(0)
	if want == 1 {
		if e := vault.CheckLabel(name); e != "" {
			fmt.Fprintf(stderr, "error: the credential name %q %s\nhelp: run `lucy vault --help`\n", name, e)
			return 2
		}
	}
	var value []byte
	if sub == "set" {
		if stdinIsTerminal() {
			fmt.Fprint(stderr, "error: lucy vault set reads the value from stdin, not a terminal, where it would show as you type\nhelp: pipe it in, for example `<your password manager> | lucy vault set --org <org> <name>`\n")
			return 2
		}
		var err error
		if value, err = io.ReadAll(io.LimitReader(stdin, vault.MaxValueBytes+3)); err != nil {
			fmt.Fprintf(stderr, "error: reading the value: %v\n", err)
			return 1
		}
		defer clear(value)
		value = bytes.TrimSuffix(value, []byte("\n"))
		value = bytes.TrimSuffix(value, []byte("\r"))
		if len(value) == 0 || len(value) > vault.MaxValueBytes {
			fmt.Fprintf(stderr, "error: a credential is 1 to %d bytes\nhelp: pipe the value in on stdin\n", vault.MaxValueBytes)
			return 2
		}
	}
	v, err := openVault(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer v.Close()
	switch sub {
	case "set":
		if err := v.Set(*org, name, value); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "set: %s/%s\n", *org, name)
	case "ls":
		entries, err := v.Names(*org)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		if len(entries) == 0 {
			fmt.Fprintf(stdout, "credentials[0]: %s holds none\n", *org)
			return 0
		}
		fmt.Fprintf(stdout, "credentials[%d]{name,updated}:\n", len(entries))
		for _, e := range entries {
			fmt.Fprintf(stdout, "  %s,%s\n", e.Name, e.Updated.Format(time.RFC3339))
		}
	case "rm":
		if err := v.Remove(*org, name); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "removed: %s/%s\n", *org, name)
	}
	return 0
}

// orgVault gives a gateway its own org's credentials and nobody else's.
type orgVault struct {
	v   *vault.Vault
	org string
}

func (o orgVault) Credential(ctx context.Context, org, name string) ([]byte, error) {
	if org != o.org {
		return nil, fmt.Errorf("vault: the %s gateway asked for org %s's credential", o.org, org)
	}
	return o.v.Credential(ctx, org, name)
}
