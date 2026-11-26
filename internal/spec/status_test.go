package spec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func openStatusStore(t *testing.T, c *clock) *eventstore.Store {
	t.Helper()
	reg, err := eventstore.NewRegistry(Payloads()...)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "state") // created 0700 by the store
	s, err := eventstore.Open(context.Background(), filepath.Join(dir, "lucy.db"), eventstore.Options{
		Registry: reg, Projectors: []eventstore.Projector{StatusProjector{}}, Now: c.now, SnapshotDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func load(t *testing.T, dir string) Set {
	t.Helper()
	set, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func statusByID(t *testing.T, s *eventstore.Store) map[string]ResourceStatus {
	t.Helper()
	sts, err := Statuses(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ResourceStatus{}
	for _, st := range sts {
		out[st.id()] = st
	}
	return out
}

// TestStatusRecorded checks the spec/status split: status goes to the event store as Conditions,
// is recorded only when it changes, keeps lastTransitionTime while a condition holds, and is
// never written into the config directory (LUCY-SPEC-003, LUCY-SPEC-007).
func TestStatusRecorded(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)}
	store := openStatusStore(t, c)
	dir := t.TempDir()
	writeFile(t, dir, "airspace.yaml", airspace)
	writeFile(t, dir, "pilot.yaml", "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p1, namespace: acme }\nspec: { role: captain }\n")
	writeFile(t, dir, "broken.yaml", "a: [1\n")
	sum, err := Record(ctx, store, "daemon", load(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Changed != 2 || sum.Removed != 0 {
		t.Fatalf("first load: %+v", sum)
	}
	st := statusByID(t, store)
	a, p := st["acme/Airspace/acme"], st["acme/Pilot/p1"]
	if a.Conditions[0].Status != True || a.SpecSHA256 == "" || a.Conditions[0].LastTransitionTime != "2026-11-01T10:00:00Z" {
		t.Fatalf("airspace status %+v", a)
	}
	if p.Conditions[0].Status != False || p.Conditions[0].Reason != ReasonSchemaViolation || p.SpecSHA256 != "" {
		t.Fatalf("pilot status %+v", p)
	}
	// Violation messages can quote the file's values; the log keeps only where they are.
	if strings.Contains(p.Conditions[0].Message, "captain") || !strings.Contains(p.Conditions[0].Message, "/spec/role") {
		t.Fatalf("recorded message %q", p.Conditions[0].Message)
	}

	// An unchanged load records only its summary.
	c.t = c.t.Add(time.Hour)
	if sum, err = Record(ctx, store, "daemon", load(t, dir)); err != nil || sum.Changed != 0 || sum.Removed != 0 {
		t.Fatalf("unchanged load: %+v %v", sum, err)
	}

	// A spec change re-records the resource but keeps the condition's transition time; fixing
	// the pilot moves its condition to True, now.
	writeFile(t, dir, "airspace.yaml", strings.Replace(airspace, "Acme", "Acme Inc", 1))
	writeFile(t, dir, "pilot.yaml", "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p1, namespace: acme }\nspec: { role: worker }\n")
	c.t = c.t.Add(time.Hour)
	if sum, err = Record(ctx, store, "daemon", load(t, dir)); err != nil || sum.Changed != 2 {
		t.Fatalf("changed load: %+v %v", sum, err)
	}
	st = statusByID(t, store)
	if a2 := st["acme/Airspace/acme"]; a2.SpecSHA256 == a.SpecSHA256 || a2.Conditions[0].LastTransitionTime != "2026-11-01T10:00:00Z" {
		t.Fatalf("airspace after spec change %+v", a2)
	}
	if p2 := st["acme/Pilot/p1"]; p2.Conditions[0].Status != True || p2.Conditions[0].LastTransitionTime != "2026-11-01T12:00:00Z" {
		t.Fatalf("pilot after fix %+v", p2)
	}

	// A resource taken out of config is removed from status.
	if err := os.Remove(filepath.Join(dir, "pilot.yaml")); err != nil {
		t.Fatal(err)
	}
	if sum, err = Record(ctx, store, "daemon", load(t, dir)); err != nil || sum.Removed != 1 {
		t.Fatalf("removal: %+v %v", sum, err)
	}
	if _, ok := statusByID(t, store)["acme/Pilot/p1"]; ok {
		t.Fatal("removed pilot still has status")
	}

	// Rebuilding the projection from the log gives the same state.
	want := statusByID(t, store)
	if err := store.Rebuild(ctx, StatusProjector{}.Name()); err != nil {
		t.Fatal(err)
	}
	if got := statusByID(t, store); len(got) != len(want) || got["acme/Airspace/acme"].Conditions[0] != want["acme/Airspace/acme"].Conditions[0] {
		t.Fatalf("rebuilt %+v, want %+v", got, want)
	}

	// The file without an identity is a problem in the summary, not a status.
	evs, err := store.ReadStream(ctx, StatusStream, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := eventstore.DecodeAs[LoadedEvent](store.Registry(), evs[len(evs)-1])
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Problems) != 1 || ev.Problems[0].Source != "broken.yaml" || ev.Problems[0].Conditions[0].Reason != ReasonDecodeError {
		t.Fatalf("loaded event %+v", ev)
	}

}

// TestStatusDuplicates checks a duplicate never overwrites the admitted resource's status.
func TestStatusDuplicates(t *testing.T) {
	ctx := context.Background()
	store := openStatusStore(t, &clock{t: time.Now()})
	dir := t.TempDir()
	writeFile(t, dir, "a.yaml", "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p1, namespace: acme }\nspec: { role: captain }\n")
	writeFile(t, dir, "b.yaml", "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p1, namespace: acme }\nspec: { role: worker }\n")
	writeFile(t, dir, "c.yaml", "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p1, namespace: acme }\nspec: { role: reviewer }\n")
	before := snapshotDir(t, dir)
	if _, err := Record(ctx, store, "daemon", load(t, dir)); err != nil {
		t.Fatal(err)
	}
	if after := snapshotDir(t, dir); len(after) != len(before) || after["a.yaml"] != before["a.yaml"] || after["b.yaml"] != before["b.yaml"] {
		t.Fatalf("recording status changed the config directory: %v", after)
	}
	st := statusByID(t, store)["acme/Pilot/p1"]
	if st.Source != "b.yaml#1" || st.Conditions[0].Status != True {
		t.Fatalf("status %+v, want the admitted b.yaml#1", st)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		out[e.Name()] = readFile(t, dir, e.Name())
	}
	return out
}
