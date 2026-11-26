package eventstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data", "lucy")
	path := filepath.Join(dir, DefaultFile)
	s := openTest(t, path)
	mustAppend(t, s, "s", NoStream, taskCreated{Title: "t"})

	for _, d := range []string{dir, filepath.Dir(dir)} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", d, got)
		}
	}
	// The WAL and shared-memory files take the database file's mode.
	for _, f := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", f, got)
		}
	}
}

func TestOpenRefusesInsecureLocations(t *testing.T) {
	ctx := context.Background()
	opts := Options{Registry: testRegistry(t)}

	open := func(path string) error {
		s, err := Open(ctx, path, opts)
		if err == nil {
			s.Close()
		}
		return err
	}

	t.Run("group-readable dir", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lucy")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := open(filepath.Join(dir, DefaultFile)); !errors.Is(err, ErrInsecurePath) {
			t.Fatalf("err = %v, want ErrInsecurePath", err)
		}
	})

	t.Run("world-readable file", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lucy")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, DefaultFile)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := open(path); !errors.Is(err, ErrInsecurePath) {
			t.Fatalf("err = %v, want ErrInsecurePath", err)
		}
	})

	t.Run("symlinked file", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lucy")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "elsewhere.db")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, DefaultFile)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := open(link); !errors.Is(err, ErrInsecurePath) {
			t.Fatalf("err = %v, want ErrInsecurePath", err)
		}
	})

	t.Run("inside a git work tree", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := open(filepath.Join(repo, "state", DefaultFile)); !errors.Is(err, ErrInsecurePath) {
			t.Fatalf("err = %v, want ErrInsecurePath", err)
		}
		if _, err := os.Stat(filepath.Join(repo, "state", DefaultFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("database file created inside the repo: %v", err)
		}
	})

	t.Run("query characters", func(t *testing.T) {
		if err := open(filepath.Join(t.TempDir(), "lucy?mode=memory")); !errors.Is(err, ErrInsecurePath) {
			t.Fatalf("err = %v, want ErrInsecurePath", err)
		}
	})

	t.Run("root", func(t *testing.T) {
		geteuid = func() int { return 0 }
		defer func() { geteuid = os.Geteuid }()
		if err := open(filepath.Join(t.TempDir(), "lucy", DefaultFile)); !errors.Is(err, ErrRunAsRoot) {
			t.Fatalf("err = %v, want ErrRunAsRoot", err)
		}
	})

	t.Run("no registry", func(t *testing.T) {
		if _, err := Open(ctx, filepath.Join(t.TempDir(), DefaultFile), Options{}); err == nil {
			t.Fatal("Open without a registry succeeded")
		}
	})
}

func TestDataDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	if d, err := DataDir(); err != nil || d != filepath.FromSlash("/xdg/data/lucy") {
		t.Fatalf("DataDir() = %q, %v", d, err)
	}
	if p, err := DefaultPath(); err != nil || p != filepath.FromSlash("/xdg/data/lucy/lucy.db") {
		t.Fatalf("DefaultPath() = %q, %v", p, err)
	}
	t.Setenv("XDG_DATA_HOME", "relative/dir")
	if _, err := DataDir(); !errors.Is(err, ErrInsecurePath) {
		t.Fatalf("relative XDG_DATA_HOME: err = %v, want ErrInsecurePath", err)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/u")
	want := "/home/u/.local/share/lucy"
	if runtime.GOOS == "darwin" {
		want = "/home/u/Library/Application Support/lucy"
	}
	if d, err := DataDir(); err != nil || d != want {
		t.Fatalf("DataDir() = %q, %v; want %q", d, err, want)
	}
}

func TestWALAndDurabilitySettings(t *testing.T) {
	s := openTest(t, "")
	ctx := context.Background()
	var mode string
	if err := s.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
	}
	// synchronous: 2 is FULL. Checked on the writer, which every commit goes through.
	var sync int
	if err := s.writer.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&sync); err != nil || sync != 2 {
		t.Fatalf("synchronous = %d, %v; want 2 (FULL)", sync, err)
	}
	var fk int
	if err := s.writer.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v; want 1", fk, err)
	}
}

func TestReaderIsReadOnly(t *testing.T) {
	s := openTest(t, "")
	mustAppend(t, s, "s", NoStream, taskCreated{Title: "t"})
	rows, err := s.QueryContext(context.Background(), `INSERT INTO streams (id, version) VALUES ('x', 1) RETURNING id`)
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
	}
	if err == nil {
		t.Fatal("write through the read-only pool succeeded")
	}
}

func TestCloseTwice(t *testing.T) {
	s := openTest(t, "")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
