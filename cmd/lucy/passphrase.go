package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/vault"
)

// The vault passphrase derives the key that wraps every org key (D14). lucy reads it from a file
// it is pointed at, or a systemd credential named lucy-vault, or else asks once on the terminal
// with echo off; never from an argument or the environment, where a process list or a child
// would see it.

// systemdCredential is the name of the vault passphrase among a systemd unit's credentials
// (LoadCredential=lucy-vault:<path>), read from $CREDENTIALS_DIRECTORY when nothing else names one.
const systemdCredential = "lucy-vault"

// promptPassphrase asks for the passphrase on the terminal, twice when confirm is set; swapped in
// tests.
var promptPassphrase = promptTTY

// unlockVault unlocks v with the passphrase from file, or a systemd credential, or the terminal.
// vaultDir is the vault's directory, inside which (as inside lucy's data directory) a passphrase
// file is refused.
func unlockVault(v *vault.Vault, file, vaultDir string) error {
	has, err := v.HasPassphrase()
	if err != nil {
		return err
	}
	pass, err := vaultPassphrase(file, vaultDir, !has)
	if err != nil {
		return err
	}
	defer clear(pass)
	return v.Unlock(pass)
}

func vaultPassphrase(file, vaultDir string, confirm bool) ([]byte, error) {
	if file == "" {
		if d := os.Getenv("CREDENTIALS_DIRECTORY"); d != "" && filepath.IsAbs(d) {
			if c := filepath.Join(d, systemdCredential); fileExists(c) {
				file = c
			}
		}
	}
	if file != "" {
		return readPassphraseFile(file, vaultDir)
	}
	return promptPassphrase(confirm)
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// readPassphraseFile reads a passphrase file: a plain file, not a symlink, that only its owner
// (this user, or root for a service manager's credential) can read, outside the vault and lucy's
// data directory. One trailing newline is dropped.
func readPassphraseFile(file, vaultDir string) ([]byte, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("the vault passphrase file: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.Mode().IsRegular():
		return nil, fmt.Errorf("the vault passphrase file %s is not a plain file (a symlink is refused)", abs)
	case fi.Mode().Perm()&0o077 != 0:
		return nil, fmt.Errorf("the vault passphrase file %s has mode %04o; only its owner may read it (chmod 0400 %s)", abs, fi.Mode().Perm(), abs)
	case !ok || (int(st.Uid) != os.Geteuid() && st.Uid != 0):
		return nil, fmt.Errorf("the vault passphrase file %s belongs to another user", abs)
	}
	dirs := []string{vaultDir}
	if data, err := eventstore.DataDir(); err == nil {
		dirs = append(dirs, data)
	}
	real, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if rd, err := filepath.EvalSymlinks(d); err == nil {
			d = rd
		}
		if real == d || strings.HasPrefix(real, d+string(filepath.Separator)) {
			return nil, fmt.Errorf("the vault passphrase file %s is inside %s; keep it outside the vault and lucy's data directory", abs, d)
		}
	}
	f, err := os.Open(abs) // #nosec G304 -- the passphrase file the owner named, checked above
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	b, err := io.ReadAll(io.LimitReader(f, vault.MaxPassphrase+3))
	if err != nil {
		return nil, err
	}
	return trimLine(b), nil
}

// trimLine drops one trailing newline, "\n" or "\r\n".
func trimLine(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}

// promptTTY asks for the passphrase on the controlling terminal with echo off, so it works with
// stdin piped (as lucy vault set reads its value).
func promptTTY(confirm bool) ([]byte, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("no vault passphrase: name a file with --passphrase-file (or a systemd credential lucy-vault), or run lucy on a terminal")
	}
	defer tty.Close() //nolint:errcheck // a terminal
	read := func(prompt string) ([]byte, error) {
		fmt.Fprint(tty, prompt)
		b, err := readNoEcho(tty)
		fmt.Fprintln(tty)
		return b, err
	}
	pass, err := read("lucy vault passphrase: ")
	if err != nil || !confirm {
		return pass, err
	}
	again, err := read("the same passphrase again: ")
	defer clear(again)
	if err != nil {
		clear(pass)
		return nil, err
	}
	if !bytes.Equal(pass, again) {
		clear(pass)
		return nil, errors.New("the two passphrases differ; nothing was changed")
	}
	return pass, nil
}

// readNoEcho reads one line from the terminal tty with echo turned off, restoring it after.
func readNoEcho(tty *os.File) ([]byte, error) {
	fd := int(tty.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, fmt.Errorf("the terminal: %w", err)
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &quiet); err != nil {
		return nil, fmt.Errorf("the terminal: %w", err)
	}
	defer unix.IoctlSetTermios(fd, ioctlSetTermios, old) //nolint:errcheck // best effort: put echo back
	line, err := bufio.NewReader(io.LimitReader(tty, vault.MaxPassphrase+3)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		clear(line)
		return nil, err
	}
	return trimLine(line), nil
}
