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
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/pipeline"
	"github.com/ar4mirez/lucy/internal/tower"
)

func writePipeline(t *testing.T, o Options, stages string) {
	t.Helper()
	doc := "apiVersion: ai-sdlc.io/v1alpha1\nkind: Pipeline\nmetadata: { name: ship, namespace: acme }\n" +
		"spec:\n  triggers: [{ event: flight.dispatched }]\n  providers: { forge: { type: github } }\n  stages:\n" + stages
	if err := os.WriteFile(filepath.Join(o.ResourcesDir, "pipeline.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A dispatched ship Flight is flown on its org's ship Pipeline (LUCY-SPEC-014): the dispatch
// records what the Tower takes from it, and a Flight of an org without one gets lucy's own.
func TestFlightsAreFlownOnTheirOrgsPipeline(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	o.TowerPollInterval = 20 * time.Millisecond
	o.Dispatch = true
	writeFlights(t, o, "spec: {}")
	writePipeline(t, o, "    - { name: pilot, onFailure: { strategy: abort } }\n"+
		"    - { name: merge, approval: { required: true, timeout: PT1H, onTimeout: abort } }\n")
	c := startClient(t, o)
	ctx := context.Background()
	var got *tower.Pipeline
	for deadline := time.Now().Add(10 * time.Second); got == nil; time.Sleep(20 * time.Millisecond) {
		page, err := c.Events(ctx, api.EventsParams{})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			if e.Type == "task.dispatched" && e.Stream == "task/acme/fix-login" {
				var d tower.Dispatched
				if err := json.Unmarshal(e.Payload, &d); err != nil {
					t.Fatal(err)
				}
				got = d.Pipeline
				if got == nil {
					t.Fatal("the flight was dispatched with no Pipeline")
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the flight was not dispatched in 10 s")
		}
	}
	want := tower.Pipeline{Name: "acme/ship", MaxRestarts: 0, ApprovalRequired: true, ApprovalTimeout: time.Hour, OnTimeout: tower.OnTimeoutAbort}
	if *got != want {
		t.Fatalf("dispatched on %+v, want %+v", *got, want)
	}
}

// writeOtherOrg adds org beta, with a runway and a Ready ship Flight and no Pipeline of its own.
func writeOtherOrg(t *testing.T, o Options) {
	t.Helper()
	for name, doc := range map[string]string{
		"beta-airspace.yaml": "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: beta, namespace: beta }\nspec: {}\n",
		"beta-runway.yaml":   "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: api, namespace: beta }\nspec: {}\n",
		"beta-flight.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: fix-api, namespace: beta }\n" +
			"spec: { shape: ship, goal: fix the api, runway: api, delivery: pull-request, autonomy: owner, acceptance: the api test passes }\n",
	} {
		if err := os.WriteFile(filepath.Join(o.ResourcesDir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// dispatchedOn waits for the task.dispatched event of the flight stream and returns its Pipeline.
func dispatchedOn(t *testing.T, c *client.Client, stream string) tower.Pipeline {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		page, err := c.Events(context.Background(), api.EventsParams{})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			if e.Type == "task.dispatched" && e.Stream == stream {
				var d tower.Dispatched
				if err := json.Unmarshal(e.Payload, &d); err != nil {
					t.Fatal(err)
				}
				if d.Pipeline == nil {
					t.Fatalf("%s was dispatched with no Pipeline", stream)
				}
				return *d.Pipeline
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not dispatched in 10 s", stream)
		}
	}
}

// An org Pipeline lucy cannot fly, whether the spec loader refuses it (a schema typo) or Parse
// does (a setting lucy does not act on, or a timeout that would approve a merge), holds that
// org's ship Flight as PipelineInvalid, naming the Pipeline: the daemon starts, the Flight is
// never dispatched, never on lucy's built-in Pipeline, and another org's Flight beside it is
// dispatched on lucy/ship as usual (LUCY-SPEC-014).
func TestAnInvalidOrgPipelineHoldsItsOrgsFlights(t *testing.T) {
	for name, tc := range map[string]struct{ stages, why string }{
		"schema typo": {"    - { name: pilot }\n    - { name: merge, approval: { required: true, tierOverride: bogus } }\n", "SchemaViolation"},
		"unsupported": {"    - { name: pilot, timeout: PT30M }\n", pipeline.ErrUnsupported.Error()},
		"auto-approve": {"    - { name: pilot }\n    - { name: merge, approval: { required: true, timeout: PT1H, onTimeout: auto-approve } }\n",
			pipeline.ErrAutoApprove.Error()},
	} {
		t.Run(name, func(t *testing.T) {
			o := opts(shortTempDir(t))
			o.TowerPollInterval = 20 * time.Millisecond
			o.Dispatch = true
			writeFlights(t, o, "spec: {}")
			writeOtherOrg(t, o)
			writePipeline(t, o, tc.stages)
			c := startClient(t, o)
			if got := dispatchedOn(t, c, "task/beta/fix-api"); got.Name != "lucy/ship" {
				t.Errorf("beta's Flight dispatched on %+v, want lucy/ship", got)
			}
			d := dispatched(t, c, "acme", "fix-login")
			if d.Status != "False" || d.Reason != ReasonPipelineInvalid || !strings.Contains(d.Message, "acme/ship") || !strings.Contains(d.Message, tc.why) {
				t.Fatalf("acme's Dispatched condition: %+v, want PipelineInvalid naming acme/ship and %q", d, tc.why)
			}
			page, err := c.Runs(context.Background(), api.RunsParams{})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range page.Runs {
				if r.Org == "acme" {
					t.Errorf("a run was queued for the held Flight: %+v", r)
				}
			}
			events, err := c.Events(context.Background(), api.EventsParams{})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range events.Events {
				if e.Stream == "task/acme/fix-login" {
					t.Errorf("the held Flight has an event: %s %s", e.Type, e.Payload)
				}
			}
		})
	}
}
