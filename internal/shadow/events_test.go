package shadow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/shadow"
)

func TestImportsAreRecordedOnceForAnAdmittedOrg(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := eventstore.NewRegistry(shadow.Payloads()...)
	if err != nil {
		t.Fatal(err)
	}
	st, err := eventstore.Open(ctx, filepath.Join(dir, "lucy", "lucy.db"), eventstore.Options{Registry: reg,
		Projectors: []eventstore.Projector{shadow.Projector{}}, SnapshotDir: filepath.Join(dir, "lucy", "state")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f, err := os.Open("testdata/tasks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := shadow.Parse(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	rec := &shadow.Recorder{Store: st, Known: func(org string) bool { return org == "acme" }}
	for _, tk := range tasks {
		if out, err := rec.Import(ctx, "owner", tk); err != nil || out != shadow.Recorded {
			t.Fatalf("import %s: %v, %v", tk.ID, out, err)
		}
	}
	if out, err := rec.Import(ctx, "owner", tasks[0]); err != nil || out != shadow.Unchanged {
		t.Fatalf("again: %v, %v", out, err)
	}
	changed := tasks[0]
	changed.Goal = "something else"
	if _, err := rec.Import(ctx, "owner", changed); !errors.Is(err, shadow.ErrConflict) {
		t.Fatalf("other contents: %v", err)
	}
	other := tasks[0]
	other.Org = "beta"
	if _, err := rec.Import(ctx, "owner", other); !errors.Is(err, shadow.ErrUnknownOrg) {
		t.Fatalf("another org: %v", err)
	}
	bad := tasks[0]
	bad.ID = "T 1"
	if _, err := rec.Import(ctx, "owner", bad); !errors.Is(err, shadow.ErrRefused) {
		t.Fatalf("an invalid task: %v", err)
	}
	rows, err := shadow.List(ctx, st, "acme")
	if err != nil || len(rows) != 2 || rows[0].Class != "bugfix" || rows[1].Task != "t-2" || rows[0].Actions != 5 {
		t.Fatalf("rows %+v, %v", rows, err)
	}
	head, _ := st.Head(ctx)
	if head != 2 {
		t.Fatalf("events %d, want one per task", head)
	}
}
