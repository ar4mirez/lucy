package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
)

func writePool(t *testing.T, o Options, specYAML string) {
	t.Helper()
	doc := "apiVersion: ai-sdlc.io/v1alpha1\nkind: WorktreePool\nmetadata: { name: pool, namespace: acme }\nspec: " + specYAML + "\n"
	if err := os.WriteFile(filepath.Join(o.ResourcesDir, "pool.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func flightList(t *testing.T, c interface {
	Call(ctx context.Context, method string, params, result any) error
}) []api.FlightInfo {
	t.Helper()
	var list api.FlightList
	if err := c.Call(context.Background(), api.MethodFlights, api.NoParams{}, &list); err != nil {
		t.Fatal(err)
	}
	return list.Flights
}

// An org's WorktreePool that lucy cannot accept holds the org's Flights, naming the field, and
// lucy's own pool defaults never stand in for it; one it accepts changes nothing
// (LUCY-SPEC-017). flights.list reports each Flight with its conditions.
func TestAnInvalidWorktreePoolHoldsItsOrgsFlights(t *testing.T) {
	for name, c := range map[string]struct {
		pool, reason, why string
	}{
		"rootDir":  {"{ rootDir: /srv/worktrees }", ReasonWorktreePoolInvalid, "spec.rootDir"},
		"accepted": {"{ layout: workspace-scoped, cleanup: { onMerge: true } }", ReasonDispatched, ""},
	} {
		t.Run(name, func(t *testing.T) {
			dir := shortTempDir(t)
			o := opts(dir)
			o.TowerPollInterval = 20 * time.Millisecond
			o.Dispatch = true
			writeFlights(t, o, "spec: {}")
			writePool(t, o, c.pool)
			cl := startClient(t, o)
			d := dispatched(t, cl, "acme", "fix-login")
			if d.Reason != c.reason || !strings.Contains(d.Message, c.why) {
				t.Fatalf("the Dispatched condition %+v, want %s naming %q", d, c.reason, c.why)
			}
			if c.reason == ReasonWorktreePoolInvalid && queued(t, cl) != 0 {
				t.Fatal("a held Flight's run was queued")
			}
			fs := flightList(t, cl)
			var got *api.FlightInfo
			for i := range fs {
				if fs[i].Org == "acme" && fs[i].Name == "fix-login" {
					got = &fs[i]
				}
			}
			if got == nil || got.Goal != "fix the login page" || got.Shape != "ship" || got.Runway != "web" ||
				got.Ready.Status != "True" || got.Dispatched.Reason != c.reason {
				t.Fatalf("flights.list: %+v", fs)
			}
			if len(fs) != 2 || fs[0].Name != "draft" {
				t.Fatalf("flights.list lists %d flights, sorted %+v; want draft and fix-login", len(fs), fs)
			}
		})
	}
}
