package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
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

// A Pipeline whose timeout would approve a merge stops the daemon at start, naming it.
func TestAnAutoApprovingPipelineIsRefusedAtStart(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	if err := os.MkdirAll(o.ResourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writePipeline(t, o, "    - { name: pilot }\n    - { name: merge, approval: { required: true, timeout: PT1H, onTimeout: auto-approve } }\n")
	err := Run(context.Background(), o)
	if !errors.Is(err, pipeline.ErrAutoApprove) || !strings.Contains(err.Error(), "acme/ship") {
		t.Fatalf("start: %v, want the auto-approving Pipeline acme/ship refused", err)
	}
}
