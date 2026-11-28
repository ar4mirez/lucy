package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/tower"
)

// tightenings lists each task.posture_tightened in the log as from>to.
func tightenings(t *testing.T, c interface {
	Events(ctx context.Context, p api.EventsParams) (api.EventsPage, error)
}) []string {
	t.Helper()
	page, err := c.Events(context.Background(), api.EventsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range page.Events {
		if e.Type == "task.posture_tightened" {
			var p tower.PostureTightened
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			got = append(got, string(p.From)+">"+string(p.To))
		}
	}
	return got
}

// config.reload applies a tightening without a restart (LUCY-POL-005): editing a Runway to a
// stricter level tightens its flight in flight at once; an edit the schema refuses is not
// applied, but still moves the flight to owner; a promotion never loosens it.
func TestReloadTightensWithoutARestart(t *testing.T) {
	for name, c := range map[string]struct {
		runway  string
		applied bool
		want    []string
	}{
		"a stricter level":            {"spec: { autonomyPolicy: standard, autonomyLevel: 0 }", true, []string{"green>owner"}},
		"an invalid edit is owner":    {"spec: { autonomyPolicy: standard, autonomyLevel: -1 }", false, []string{"green>owner"}},
		"the same level changes none": {"spec: { autonomyPolicy: standard, autonomyLevel: 1 }", true, nil},
	} {
		t.Run(name, func(t *testing.T) {
			dir := shortTempDir(t)
			o := opts(dir)
			o.TowerPollInterval = 20 * time.Millisecond
			o.Dispatch = true
			writeFlights(t, o, "spec: { autonomyPolicy: standard, autonomyLevel: 1 }")
			for file, doc := range map[string]string{
				"policy.yaml": standardPolicy,
				"ready.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: fix-login, namespace: acme }\n" +
					"spec: { shape: ship, goal: fix the login page, runway: web, delivery: pull-request, acceptance: the login test passes }\n",
			} {
				if err := os.WriteFile(filepath.Join(o.ResourcesDir, file), []byte(doc), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cl := startClient(t, o)
			if d := dispatched(t, cl, "acme", "fix-login"); d.Reason != ReasonDispatched {
				t.Fatalf("the Dispatched condition %+v", d)
			}
			runway := "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\n" + c.runway + "\n"
			if err := os.WriteFile(filepath.Join(o.ResourcesDir, "runway.yaml"), []byte(runway), 0o600); err != nil {
				t.Fatal(err)
			}
			var r api.ReloadResult
			if err := cl.Call(context.Background(), api.MethodReload, api.NoParams{}, &r); err != nil {
				t.Fatal(err)
			}
			if r.Applied != c.applied || r.Tightened != len(c.want) {
				t.Fatalf("reload %+v, want applied %v and %d tightened", r, c.applied, len(c.want))
			}
			if !c.applied && (len(r.Refused) != 1 || !strings.Contains(r.Refused[0], "runway.yaml")) {
				t.Fatalf("the refused resources %v", r.Refused)
			}
			if got := tightenings(t, cl); strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("tightenings %v, want %v", got, c.want)
			}
			if got := listedAutonomy(t, cl); len(c.want) > 0 && got != "owner" {
				t.Fatalf("flights.list's autonomy %q after the tightening, want owner", got)
			}
		})
	}
}
