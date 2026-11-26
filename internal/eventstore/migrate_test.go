package eventstore

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/schema.sql, append new migrations to testdata/migrations.sum and create missing upgrade fixtures")

func fixedNow() time.Time { return time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC) }

// migrateRaw creates a database at path with only the first n migrations applied, the way an
// older lucy would have left it.
func migrateRaw(t testing.TB, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(context.Background(), db, ms[:n], fixedNow, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

// schemaOf returns the database's schema, excluding SQLite's internal tables and the projection
// tables the test projector creates.
func schemaOf(t testing.TB, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%' AND name NOT LIKE 'tally_%' AND sql IS NOT NULL ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, stmt string
		if err := rows.Scan(&typ, &name, &stmt); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "-- %s %s\n%s;\n\n", typ, name, stmt)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func freshSchema(t testing.TB) string {
	s := openTest(t, "")
	return schemaOf(t, s.writer)
}

// TestSchemaGolden pins the full schema. A migration that changes it must update
// testdata/schema.sql (go test ./internal/eventstore -run TestSchemaGolden -update), so every schema
// change is visible in review.
func TestSchemaGolden(t *testing.T) {
	got := freshSchema(t)
	golden := filepath.Join("testdata", "schema.sql")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Fatalf("schema differs from %s; run go test ./internal/eventstore -run TestSchemaGolden -update and review the diff\n--- got ---\n%s", golden, got)
	}
}

// TestMigrationsLocked keeps migrations forward-only: once a migration's checksum is recorded in
// testdata/migrations.sum it may never change or disappear. -update only appends new migrations.
func TestMigrationsLocked(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	sumPath := filepath.Join("testdata", "migrations.sum")
	locked := map[int]string{}
	var order []int
	if f, err := os.Open(sumPath); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var v int
			var name, sum string
			if _, err := fmt.Sscanf(sc.Text(), "%04d %s %s", &v, &name, &sum); err != nil {
				t.Fatalf("%s: bad line %q", sumPath, sc.Text())
			}
			locked[v] = name + " " + sum
			order = append(order, v)
		}
		f.Close()
	} else if !*update {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	for i, v := range order {
		if v != i+1 {
			t.Fatalf("%s: versions must run 1, 2, 3, ...; line %d is %04d", sumPath, i+1, v)
		}
	}
	if len(order) > len(ms) {
		t.Fatalf("migration %04d is recorded in %s but its file is gone; migrations are never removed", len(ms)+1, sumPath)
	}
	for _, m := range ms[:len(order)] {
		if got := m.name + " " + m.checksum; locked[m.version] != got {
			t.Fatalf("migration %04d_%s.sql changed after it was locked (%s != %s); add a new migration instead",
				m.version, m.name, got, locked[m.version])
		}
	}
	if pending := ms[len(order):]; len(pending) > 0 {
		if !*update {
			t.Fatalf("migration %04d_%s.sql is not in %s; run with -update to lock it", pending[0].version, pending[0].name, sumPath)
		}
		f, err := os.OpenFile(sumPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pending {
			fmt.Fprintf(f, "%04d %s %s\n", m.version, m.name, m.checksum)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFreshDatabaseIsAtLatest(t *testing.T) {
	s := openTest(t, "")
	ms, _ := loadMigrations()
	applied, err := appliedMigrations(context.Background(), s.writer)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(ms) || len(ms) != SchemaVersion() || SchemaVersion() < 2 {
		t.Fatalf("applied %d of %d migrations (SchemaVersion %d)", len(applied), len(ms), SchemaVersion())
	}
	for i, a := range applied {
		if a.checksum != ms[i].checksum {
			t.Fatalf("migration %d checksum %s, want %s", a.version, a.checksum, ms[i].checksum)
		}
	}
	if snaps := snapshots(t, s.snapDir); len(snaps) != 0 {
		t.Fatalf("a fresh database must not get a pre-migration snapshot: %v", snaps)
	}
}

// TestUpgradeFromEveryVersion builds a database at each earlier schema version, writes events
// into it, upgrades a copy of it, and checks that the result matches a fresh database and keeps
// every event (LUCY-EVT-004).
func TestUpgradeFromEveryVersion(t *testing.T) {
	want := freshSchema(t)
	for k := 1; k < SchemaVersion(); k++ {
		t.Run(fmt.Sprintf("from=v%d", k), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "lucy")
			old := filepath.Join(dir, "old.db")
			migrateRaw(t, old, k)
			db := rawDB(t, old)
			for i := range 3 {
				if _, err := db.Exec(`INSERT INTO events (stream, stream_version, type, type_version, envelope_version, occurred_at, actor, payload)
					VALUES ('task-1', ?, 'task.created', 1, 1, ?, 'old-lucy', ?)`,
					i+1, int64(i), fmt.Sprintf(`{"title":"t%d","org":"o"}`, i)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT INTO streams (id, version) VALUES ('task-1', 3)`); err != nil {
				t.Fatal(err)
			}
			db.Close()

			path := filepath.Join(dir, DefaultFile)
			copyFile(t, old, path)
			s := openTest(t, path, &tally{})
			if got := schemaOf(t, s.writer); got != want {
				t.Fatalf("upgraded schema differs from fresh:\n%s\nwant\n%s", got, want)
			}
			evs, err := s.ReadStream(context.Background(), "task-1", 0, 0)
			if err != nil || len(evs) != 3 {
				t.Fatalf("events after upgrade = %d, %v", len(evs), err)
			}
			if p, err := DecodeAs[taskCreated](testRegistry(t), evs[2]); err != nil || p.Title != "t2" {
				t.Fatalf("decode after upgrade = %+v, %v", p, err)
			}
			if got := tallyRows(t, s); got["task-1"] != 3 {
				t.Fatalf("projection after upgrade = %v", got)
			}
			mustAppend(t, s, "task-1", 3, taskNoted{Note: "after upgrade"})

			if _, err := os.Stat(path + fmt.Sprintf(".pre-v%d.bak", k)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a snapshot was written next to the database")
			}
			snaps := snapshots(t, s.snapDir)
			if len(snaps) != 1 || !strings.HasPrefix(filepath.Base(snaps[0]), fmt.Sprintf("%s.pre-v%d.", DefaultFile, k)) ||
				!strings.HasSuffix(snaps[0], ".seq3.bak") {
				t.Fatalf("snapshots = %v, want one lucy.db.pre-v%d.<time>.seq3.bak", snaps, k)
			}
			snap := snaps[0]
			fi, err := os.Stat(snap)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Fatalf("snapshot mode = %04o, want 0600", fi.Mode().Perm())
			}
			sdb := rawDB(t, snap)
			applied, err := appliedMigrations(context.Background(), sdb)
			if err != nil || len(applied) != k {
				t.Fatalf("snapshot is at version %d, %v; want %d", len(applied), err, k)
			}
		})
	}
}

// TestUpgradeFromFixtures opens databases written by earlier schema versions, kept as SQL dumps in
// testdata/upgrade/vN.sql. They are generated once, when version N is current, and never rewritten:
// they are what an older lucy actually left on disk.
func TestUpgradeFromFixtures(t *testing.T) {
	for v := 1; v <= SchemaVersion(); v++ {
		fixture := filepath.Join("testdata", "upgrade", fmt.Sprintf("v%d.sql", v))
		if _, err := os.Stat(fixture); errors.Is(err, os.ErrNotExist) {
			if !*update {
				if v < SchemaVersion() {
					t.Errorf("%s is missing; fixtures for released versions are never deleted", fixture)
				}
				continue
			}
			writeFixture(t, fixture, v)
		}
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			body, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "lucy", DefaultFile)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			db := rawDB(t, path)
			if _, err := db.Exec(string(body)); err != nil {
				t.Fatalf("load %s: %v", fixture, err)
			}
			db.Close()
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}

			s := openTest(t, path, &tally{})
			if got, want := schemaOf(t, s.writer), freshSchema(t); got != want {
				t.Fatalf("schema after upgrading %s differs from fresh", fixture)
			}
			evs, err := s.ReadAfter(context.Background(), 0, 0)
			if err != nil || len(evs) != len(fixtureEvents) {
				t.Fatalf("events = %d, %v; want %d", len(evs), err, len(fixtureEvents))
			}
			reg := testRegistry(t)
			for i, e := range evs {
				p, err := reg.Decode(e)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(p, fixtureEvents[i].p) || e.Stream != fixtureEvents[i].stream {
					t.Fatalf("event %d = %s %+v, want %s %+v", i, e.Stream, p, fixtureEvents[i].stream, fixtureEvents[i].p)
				}
			}
			v, err := s.StreamVersion(context.Background(), "task-1")
			if err != nil || v != 2 {
				t.Fatalf("task-1 version = %d, %v", v, err)
			}
			mustAppend(t, s, "task-1", 2, taskNoted{Note: "after upgrade"})
			if _, err := s.Claim(context.Background(), "sup"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

var fixtureEvents = []struct {
	stream string
	p      Payload
}{
	{"task-1", taskCreated{Title: "first", Org: "ar4mirez"}},
	{"task-2", taskCreated{Title: "second", Org: "ar4mirez"}},
	{"task-1", taskNoted{Note: "a note", Tokens: 42}},
}

// writeFixture dumps a database at schema version v holding fixtureEvents as plain SQL.
func writeFixture(t *testing.T, fixture string, v int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lucy", "fixture.db")
	migrateRaw(t, path, v)
	db := rawDB(t, path)
	reg := testRegistry(t)
	versions := map[string]int64{}
	for i, fe := range fixtureEvents {
		_, data, err := reg.encode(fe.p)
		if err != nil {
			t.Fatal(err)
		}
		versions[fe.stream]++
		if _, err := db.Exec(`INSERT INTO events (stream, stream_version, type, type_version, envelope_version, occurred_at, actor, payload)
			VALUES (?, ?, ?, ?, 1, ?, 'fixture', ?)`,
			fe.stream, versions[fe.stream], fe.p.EventType(), fe.p.EventVersion(), fixedNow().Add(time.Duration(i)*time.Second).UnixNano(), string(data)); err != nil {
			t.Fatal(err)
		}
	}
	for stream, n := range versions {
		if _, err := db.Exec(`INSERT INTO streams (id, version) VALUES (?, ?)`, stream, n); err != nil {
			t.Fatal(err)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- A lucy database at schema version %d, generated by TestUpgradeFromFixtures -update.\n", v)
	b.WriteString("-- Never edit or regenerate: it stands for what that version left on disk.\n\n")
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY CASE type WHEN 'table' THEN 0 ELSE 1 END, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var typ, name, stmt string
		if err := rows.Scan(&typ, &name, &stmt); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s;\n\n", stmt)
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	rows.Close()
	for _, table := range append(tables, "sqlite_sequence") {
		dumpRows(t, db, table, &b)
	}
	if err := os.MkdirAll(filepath.Dir(fixture), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func dumpRows(t *testing.T, db *sql.DB, table string, b *strings.Builder) {
	t.Helper()
	// table comes from sqlite_master of a database this test built, never from input.
	rows, err := db.Query(`SELECT * FROM "` + table + `" ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		lits := make([]string, len(vals))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
				lits[i] = "NULL"
			case int64:
				lits[i] = fmt.Sprint(x)
			case string:
				lits[i] = "'" + strings.ReplaceAll(x, "'", "''") + "'"
			default:
				t.Fatalf("dump %s: unsupported value %T", table, v)
			}
		}
		fmt.Fprintf(b, "INSERT INTO %s (%s) VALUES (%s);\n", table, strings.Join(cols, ", "), strings.Join(lits, ", "))
	}
	b.WriteString("\n")
}

func TestRefusesNewerSchema(t *testing.T) {
	s := openTest(t, "")
	path := s.Path()
	if _, err := s.writer.Exec(`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, 'from_the_future', 'x', 0)`, SchemaVersion()+1); err != nil {
		t.Fatal(err)
	}
	s.Close()
	_, err := Open(context.Background(), path, Options{Registry: testRegistry(t)})
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
}

func TestRefusesChangedMigration(t *testing.T) {
	s := openTest(t, "")
	path := s.Path()
	if _, err := s.writer.Exec(`UPDATE schema_migrations SET checksum = 'edited' WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	_, err := Open(context.Background(), path, Options{Registry: testRegistry(t)})
	if !errors.Is(err, ErrMigrationChanged) {
		t.Fatalf("err = %v, want ErrMigrationChanged", err)
	}
}

func TestRefusesGappedHistory(t *testing.T) {
	s := openTest(t, "")
	path := s.Path()
	if _, err := s.writer.Exec(`DELETE FROM schema_migrations WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	_, err := Open(context.Background(), path, Options{Registry: testRegistry(t)})
	if !errors.Is(err, ErrMigrationHistory) {
		t.Fatalf("err = %v, want ErrMigrationHistory", err)
	}
}

func TestReadMigrationsValidates(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"bad name": {"m/1_init.sql": {Data: []byte("SELECT 1;")}},
		"gap":      {"m/0001_a.sql": {Data: []byte("SELECT 1;")}, "m/0003_c.sql": {Data: []byte("SELECT 1;")}},
		"no one":   {"m/0002_b.sql": {Data: []byte("SELECT 1;")}},
	}
	for name, fsys := range cases {
		if _, err := readMigrations(fsys, "m"); err == nil {
			t.Errorf("%s: readMigrations succeeded", name)
		}
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lucy", DefaultFile)
	migrateRaw(t, path, 1)
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ms, _ := loadMigrations()
	broken := append([]migration{}, ms[:1]...)
	broken = append(broken, migration{version: 2, name: "broken", body: "CREATE TABLE half (x INTEGER); SELECT * FROM missing_table;", checksum: "x"})
	if err := migrate(context.Background(), db, broken, fixedNow, nil); err == nil {
		t.Fatal("broken migration succeeded")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'half'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("half-applied migration left table behind: %d, %v", n, err)
	}
	applied, _ := appliedMigrations(context.Background(), db)
	if len(applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(applied))
	}
}

// snapshots lists the pre-migration snapshots in dir.
func snapshots(t testing.TB, dir string) []string {
	t.Helper()
	got, err := filepath.Glob(filepath.Join(dir, "*.bak"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// eventTitles reads the task.created titles in a database file, in log order.
func eventTitles(t testing.TB, path string) []string {
	t.Helper()
	db := rawDB(t, path)
	defer db.Close()
	rows, err := db.Query(`SELECT json_extract(payload, '$.title') FROM events WHERE type = 'task.created' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSnapshotAfterRestoreIsNew follows the rollback runbook twice: upgrade (snapshot A), restore A
// and keep writing with the older schema, upgrade again (snapshot B). B must be a new file holding
// the events written after the restore; A must be left as it was.
func TestSnapshotAfterRestoreIsNew(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lucy")
	snapDir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, DefaultFile)
	insert := func(seq int, title string) {
		db := rawDB(t, path)
		if _, err := db.Exec(`INSERT INTO events (stream, stream_version, type, type_version, envelope_version, occurred_at, actor, payload)
			VALUES ('task-1', ?, 'task.created', 1, 1, 0, 'old-lucy', json_object('title', ?, 'org', 'o'))`, seq, title); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO streams (id, version) VALUES ('task-1', ?) ON CONFLICT (id) DO UPDATE SET version = excluded.version`, seq); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	migrateRaw(t, path, 1)
	insert(1, "before")

	openWithSnapshots(t, path, snapDir).Close()
	first := snapshots(t, snapDir)
	if len(first) != 1 {
		t.Fatalf("snapshots after first upgrade = %v", first)
	}
	a := first[0]

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(path+suffix, path+suffix+".aside"); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	copyFile(t, a, path)
	insert(2, "after restore")

	openWithSnapshots(t, path, snapDir).Close()
	second := snapshots(t, snapDir)
	if len(second) != 2 {
		t.Fatalf("snapshots after second upgrade = %v, want 2", second)
	}
	b := second[0]
	if b == a {
		b = second[1]
	}
	if got := eventTitles(t, b); !reflect.DeepEqual(got, []string{"before", "after restore"}) {
		t.Fatalf("snapshot B events = %q", got)
	}
	if got := eventTitles(t, a); !reflect.DeepEqual(got, []string{"before"}) {
		t.Fatalf("snapshot A events = %q, want it unchanged", got)
	}
}

func TestSnapshotDirIsChecked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lucy", DefaultFile)
	migrateRaw(t, path, 1)
	snapDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), path, Options{Registry: testRegistry(t), SnapshotDir: snapDir})
	if !errors.Is(err, ErrInsecurePath) {
		t.Fatalf("err = %v, want ErrInsecurePath for a 0755 snapshot dir", err)
	}
	applied, err := appliedMigrations(context.Background(), rawDB(t, path))
	if err != nil || len(applied) != 1 {
		t.Fatalf("applied = %d, %v; the upgrade must not run without its snapshot", len(applied), err)
	}
}

func copyFile(t testing.TB, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
