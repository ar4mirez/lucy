package eventstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// Schema migrations are forward-only: each is a numbered SQL file in migrations/, applied once in
// its own transaction and recorded with its checksum. There are no down migrations. A database
// written by a newer lucy is refused rather than guessed at, and an applied migration whose file
// changed is refused, because the schema it describes is no longer the one on disk.

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Errors returned by schema migration.
var (
	ErrSchemaTooNew     = errors.New("eventstore: database schema is newer than this lucy")
	ErrMigrationChanged = errors.New("eventstore: an applied migration was changed")
	ErrMigrationHistory = errors.New("eventstore: migration history is not contiguous")
)

type migration struct {
	version  int
	name     string
	body     string
	checksum string
}

var migrationName = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.sql$`)

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY CHECK (version > 0),
    name       TEXT    NOT NULL,
    checksum   TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
) STRICT`

// loadMigrations reads the embedded migrations in version order. Versions must run 1, 2, 3, ...
func loadMigrations() ([]migration, error) {
	return readMigrations(migrationFiles, "migrations")
}

func readMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("eventstore: migration file %q must be named NNNN_name.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		ms = append(ms, migration{version: v, name: m[2], body: string(body), checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("eventstore: migrations must be numbered 1, 2, 3, ...: found %04d at position %d", m.version, i+1)
		}
	}
	return ms, nil
}

// SchemaVersion is the schema version this build migrates databases to.
func SchemaVersion() int {
	ms, err := loadMigrations()
	if err != nil {
		return 0
	}
	return len(ms)
}

type appliedMigration struct {
	version  int
	name     string
	checksum string
}

func appliedMigrations(ctx context.Context, db *sql.DB) ([]appliedMigration, error) {
	if _, err := db.ExecContext(ctx, createMigrationsTable); err != nil {
		return nil, fmt.Errorf("eventstore: create schema_migrations: %w", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []appliedMigration
	for rows.Next() {
		var a appliedMigration
		if err := rows.Scan(&a.version, &a.name, &a.checksum); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// checkHistory compares what the database says was applied with the migrations this build has and
// returns how many are already applied.
func checkHistory(applied []appliedMigration, ms []migration) (int, error) {
	for i, a := range applied {
		if a.version != i+1 {
			return 0, fmt.Errorf("%w: expected version %d, found %d", ErrMigrationHistory, i+1, a.version)
		}
		if a.version > len(ms) {
			return 0, fmt.Errorf("%w: database is at version %d, this build knows %d; use a newer lucy or restore a backup",
				ErrSchemaTooNew, applied[len(applied)-1].version, len(ms))
		}
		if m := ms[i]; a.checksum != m.checksum || a.name != m.name {
			return 0, fmt.Errorf("%w: %04d_%s.sql differs from the applied %04d_%s.sql", ErrMigrationChanged, m.version, m.name, a.version, a.name)
		}
	}
	return len(applied), nil
}

// migrate applies the pending migrations in ms to db, which must be the store's single-connection
// writer pool opened with immediate transactions. Before upgrading a database that already has a
// schema it calls snap, when not nil, with the current version, so a bad upgrade can be undone by
// restoring the snapshot with the older lucy.
func migrate(ctx context.Context, db *sql.DB, ms []migration, now func() time.Time,
	snap func(ctx context.Context, db *sql.DB, version int) error) error {
	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	done, err := checkHistory(applied, ms)
	if err != nil {
		return err
	}
	if done == len(ms) {
		return nil
	}
	if done > 0 && snap != nil {
		if err := snap(ctx, db, done); err != nil {
			return err
		}
	}
	for _, m := range ms[done:] {
		if err := applyMigration(ctx, db, m, now()); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration, at time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	// The body is an embedded migration file from this binary, not input.
	if _, err := tx.ExecContext(ctx, m.body); err != nil {
		return fmt.Errorf("eventstore: migration %04d_%s: %w", m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.version, m.name, m.checksum, at.UTC().UnixNano()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("eventstore: commit migration %04d_%s: %w", m.version, m.name, err)
	}
	return nil
}

// snapshotTime is the UTC timestamp in a snapshot's name.
const snapshotTime = "20060102T150405.000000000Z"

// writeSnapshot writes a consistent copy of db, at schema version, into dir as
// <base>.pre-v<version>.<UTC time>.seq<head>.bak, mode 0600. The copy is written to a temporary file
// in dir, synced, linked into place and the directory synced, so a snapshot is either complete or
// absent, is durable before any migration runs, and an existing file is never overwritten.
func writeSnapshot(ctx context.Context, db *sql.DB, dir, base string, version int, at time.Time) error {
	seq, err := head(ctx, db)
	if err != nil {
		return fmt.Errorf("eventstore: pre-migration snapshot: %w", err)
	}
	dst := filepath.Join(dir, fmt.Sprintf("%s.pre-v%d.%s.seq%d.bak", base, version, at.UTC().Format(snapshotTime), seq))
	f, err := os.CreateTemp(dir, ".snapshot-*.tmp")
	if err != nil {
		return fmt.Errorf("eventstore: pre-migration snapshot: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) //nolint:errcheck // best effort; gone already after a successful link
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return fmt.Errorf("eventstore: pre-migration snapshot: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("eventstore: pre-migration snapshot: %w", err)
	}
	if err := syncPath(tmp); err != nil {
		return fmt.Errorf("eventstore: pre-migration snapshot: %w", err)
	}
	if err := os.Link(tmp, dst); err != nil {
		return fmt.Errorf("eventstore: pre-migration snapshot: %w", err)
	}
	if err := syncPath(dir); err != nil {
		return fmt.Errorf("eventstore: pre-migration snapshot: %w", err)
	}
	return nil
}

// syncPath flushes a file's contents, or a directory's entries, to disk. On macOS Go's Sync issues
// F_FULLFSYNC.
func syncPath(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close() //nolint:errcheck // the sync error is what matters
		return err
	}
	return f.Close()
}
