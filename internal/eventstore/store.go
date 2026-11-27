// Package eventstore is lucy's state store: one SQLite database whose append-only event log is the
// source of truth, with every other table a projection of that log (LUCY-EVT-001, LUCY-EVT-006).
//
//   - Events are typed, versioned envelopes around a JSON payload (envelope.go). Payload types are
//     registered; field names that look like secrets are refused, and payloads are size-capped
//     (LUCY-EVT-005).
//   - Appends use optimistic concurrency per stream and run the registered projectors in the same
//     transaction, so an event and the state change it describes commit together (LUCY-EVT-002).
//   - Projections can be rebuilt from the log at any time; a projector whose version changed is
//     rebuilt when the store opens.
//   - Consumers read past a cursor and ack with the generation they claimed; a stale generation's
//     ack is refused (LUCY-EVT-003).
//   - The schema moves only forward, through numbered, checksummed migrations (LUCY-EVT-004).
//
// The driver is modernc.org/sqlite, a pure-Go SQLite, so release builds stay CGO_ENABLED=0. The
// database runs in WAL mode with synchronous=FULL (and fullfsync on macOS): a commit that returned
// is on disk. The file is 0600 in a 0700 directory owned by the user, never inside a git work tree,
// and lucy refuses to open it as root (LUCY-SEC-001).
//
// The audit hash chain (LUCY-SPEC-009) is not part of this package: internal/audit is a projector
// over the events this package writes.
package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Errors returned by Open.
var (
	ErrInsecurePath = errors.New("eventstore: insecure database path")
	ErrRunAsRoot    = errors.New("eventstore: lucy does not run as root")
)

// DefaultFile is the database file name inside lucy's data directory.
const DefaultFile = "lucy.db"

// Options configures a Store.
type Options struct {
	// Registry holds the payload types the store may append and decode. Required.
	Registry *Registry
	// Projectors maintain state tables from the log. Each runs inside every append transaction.
	Projectors []Projector
	// Now is the clock stamped on events; time.Now when nil. Tests and the simulation harness
	// pass a fake clock.
	Now func() time.Time
	// SnapshotDir receives the snapshot taken before a schema upgrade; StateDir when empty. It is
	// created 0700 and checked like the database's directory.
	SnapshotDir string
}

// Store is an open event store. It is safe for concurrent use.
type Store struct {
	path       string
	writer     *sql.DB // one connection; every write goes through it
	reader     *sql.DB // read-only connections for queries outside a transaction
	reg        *Registry
	projectors []Projector
	now        func() time.Time
	snapDir    string // "" means StateDir

	closeOnce sync.Once
	closeErr  error
}

// geteuid is swapped in tests.
var geteuid = os.Geteuid

// DataDir returns lucy's data directory: $XDG_DATA_HOME/lucy, else ~/.local/share/lucy on Linux and
// ~/Library/Application Support/lucy on macOS.
func DataDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		if !filepath.IsAbs(d) {
			return "", fmt.Errorf("%w: XDG_DATA_HOME %q is not absolute", ErrInsecurePath, d)
		}
		return filepath.Join(d, "lucy"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "lucy"), nil
	}
	return filepath.Join(home, ".local", "share", "lucy"), nil
}

// StateDir returns lucy's state directory, which holds logs and database snapshots:
// $XDG_STATE_HOME/lucy, else ~/.local/state/lucy on Linux and
// ~/Library/Application Support/lucy/state on macOS.
func StateDir() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		if !filepath.IsAbs(d) {
			return "", fmt.Errorf("%w: XDG_STATE_HOME %q is not absolute", ErrInsecurePath, d)
		}
		return filepath.Join(d, "lucy"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "lucy", "state"), nil
	}
	return filepath.Join(home, ".local", "state", "lucy"), nil
}

// DefaultPath returns the database path inside DataDir.
func DefaultPath() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, DefaultFile), nil
}

// Open opens or creates the database at path, applies pending migrations and rebuilds any
// projection whose projector version changed.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if opts.Registry == nil {
		return nil, errors.New("eventstore: Options.Registry is required")
	}
	if err := checkProjectors(opts.Projectors); err != nil {
		return nil, err
	}
	if geteuid() == 0 {
		return nil, ErrRunAsRoot
	}
	abs, err := preparePath(path)
	if err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	s := &Store{path: abs, reg: opts.Registry, projectors: opts.Projectors, now: now, snapDir: opts.SnapshotDir}
	if s.writer, err = sql.Open("sqlite", dsn(abs, false)); err != nil {
		return nil, err
	}
	s.writer.SetMaxOpenConns(1)
	if err := s.initWriter(ctx); err != nil {
		_ = s.writer.Close() // the init error is what matters
		return nil, err
	}
	if s.reader, err = sql.Open("sqlite", dsn(abs, true)); err != nil {
		_ = s.writer.Close() // the open error is what matters
		return nil, err
	}
	if err := s.syncProjections(ctx); err != nil {
		_ = s.Close() // the sync error is what matters
		return nil, err
	}
	return s, nil
}

func (s *Store) initWriter(ctx context.Context) error {
	var mode string
	if err := s.writer.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		return fmt.Errorf("eventstore: open %s: %w", s.path, err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("eventstore: open %s: journal_mode is %q, want wal", s.path, mode)
	}
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	return migrate(ctx, s.writer, ms, s.now, s.snapshot)
}

// snapshot is migrate's pre-upgrade hook: it copies the database at schema version into the
// snapshot directory.
func (s *Store) snapshot(ctx context.Context, db *sql.DB, version int) error {
	dir := s.snapDir
	if dir == "" {
		d, err := StateDir()
		if err != nil {
			return fmt.Errorf("eventstore: pre-migration snapshot: %w", err)
		}
		dir = d
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := prepareDir(dir); err != nil {
		return err
	}
	return writeSnapshot(ctx, db, dir, filepath.Base(s.path), version, s.now())
}

// dsn builds the driver's connection string. Pragmas are per connection, so they ride on the DSN
// and apply to every connection the pool opens.
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "fullfsync(1)")
	q.Add("_pragma", "checkpoint_fullfsync(1)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	return path + "?" + q.Encode()
}

// preparePath makes path absolute, creates its directory 0700 if needed and the file 0600, and
// refuses a location another user could read or that sits inside a git work tree.
func preparePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty path", ErrInsecurePath)
	}
	if strings.ContainsAny(path, "?#") {
		return "", fmt.Errorf("%w: %q contains '?' or '#'", ErrInsecurePath, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := prepareDir(filepath.Dir(abs)); err != nil {
		return "", err
	}
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- the database path the owner chose; its dir is checked above, its owner and mode below
	if err != nil {
		return "", fmt.Errorf("eventstore: create %s: %w", abs, err)
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	for _, p := range []string{abs, abs + "-wal", abs + "-shm"} {
		if err := checkOwned(p, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return abs, nil
}

// prepareDir creates dir 0700 if needed and refuses it if another user could read it or it sits
// inside a git work tree.
func prepareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("eventstore: create %s: %w", dir, err)
	}
	if err := checkOwned(dir, true); err != nil {
		return err
	}
	if repo, ok := insideGitWorkTree(dir); ok {
		return fmt.Errorf("%w: %s is inside the git work tree %s; state never lives in a repo", ErrInsecurePath, dir, repo)
	}
	return nil
}

// checkOwned refuses a path that is a symlink (files only), is not owned by the current user, or
// grants any group or other permission.
func checkOwned(p string, isDir bool) error {
	var fi os.FileInfo
	var err error
	if isDir {
		fi, err = os.Stat(p)
	} else {
		fi, err = os.Lstat(p)
	}
	if err != nil {
		return err
	}
	switch {
	case isDir && !fi.IsDir():
		return fmt.Errorf("%w: %s is not a directory", ErrInsecurePath, p)
	case !isDir && !fi.Mode().IsRegular():
		return fmt.Errorf("%w: %s is not a regular file", ErrInsecurePath, p)
	case fi.Mode().Perm()&0o077 != 0:
		want := "0600"
		if isDir {
			want = "0700"
		}
		return fmt.Errorf("%w: %s has mode %04o; want %s (chmod %s %s)", ErrInsecurePath, p, fi.Mode().Perm(), want, want, p)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != geteuid() {
		return fmt.Errorf("%w: %s is owned by uid %d, not %d", ErrInsecurePath, p, st.Uid, geteuid())
	}
	return nil
}

// insideGitWorkTree reports the nearest ancestor of dir that holds a .git entry. The walk stops below
// the home directory, so a dotfiles repository at ~ does not count; project repositories do.
func insideGitWorkTree(dir string) (string, bool) {
	home, _ := os.UserHomeDir()
	for d := dir; ; {
		if home != "" && d == filepath.Clean(home) {
			return "", false
		}
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

// Path is the database file's absolute path.
func (s *Store) Path() string { return s.path }

// Registry returns the registry the store was opened with, for decoding the events it returns.
func (s *Store) Registry() *Registry { return s.reg }

// Close closes the store. It is safe to call more than once.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		var errs []error
		if s.reader != nil {
			errs = append(errs, s.reader.Close())
		}
		errs = append(errs, s.writer.Close())
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

// QueryContext runs a read-only query, typically over a projection's tables. The connection is
// query_only, so a write through it fails.
func (s *Store) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.reader.QueryContext(ctx, query, args...)
}

// QueryRowContext runs a read-only query that returns at most one row.
func (s *Store) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.reader.QueryRowContext(ctx, query, args...)
}
