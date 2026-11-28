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
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

const standardPolicy = "apiVersion: ai-sdlc.io/v1alpha1\nkind: AutonomyPolicy\nmetadata: { name: standard, namespace: acme }\nspec:\n  levels:\n" +
	"    - { level: 0, name: L0, permissions: { read: ['*'], write: [], execute: [] }, guardrails: { requireApproval: all }, monitoring: continuous }\n" +
	"    - { level: 1, name: L1, permissions: { read: ['*'], write: [], execute: [] }, guardrails: { requireApproval: security-critical-only }, monitoring: continuous }\n" +
	"  promotionCriteria: {}\n  demotionTriggers:\n    - { trigger: critical-security-incident, action: demote-to-0, cooldown: 4w }\n"

// A Flight is flown under its Runway's posture, from the AutonomyPolicy level the Runway names,
// tightened by its own autonomy; one whose autonomy would loosen it is held (LUCY-POL-005).
func TestFlightsAreFlownUnderTheirRunwaysPosture(t *testing.T) {
	for name, c := range map[string]struct {
		level, autonomy string
		want            policy.Posture // "" when the Flight is held
	}{
		"the Runway's green":             {"1", "", policy.PostureGreen},
		"tightened to owner":             {"1", "autonomy: owner, ", policy.PostureOwner},
		"loosening the Runway's is held": {"0", "autonomy: green, ", ""},
		"the Runway's owner, unstated":   {"0", "", policy.PostureOwner},
	} {
		t.Run(name, func(t *testing.T) {
			dir := shortTempDir(t)
			o := opts(dir)
			o.TowerPollInterval = 20 * time.Millisecond
			o.Dispatch = true
			writeFlights(t, o, "spec: { autonomyPolicy: standard, autonomyLevel: "+c.level+" }")
			for file, doc := range map[string]string{
				"policy.yaml": standardPolicy,
				"ready.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: fix-login, namespace: acme }\n" +
					"spec: { shape: ship, goal: fix the login page, runway: web, delivery: pull-request, " + c.autonomy + "acceptance: the login test passes }\n",
			} {
				if err := os.WriteFile(filepath.Join(o.ResourcesDir, file), []byte(doc), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cl := startClient(t, o)
			d := dispatched(t, cl, "acme", "fix-login")
			if c.want == "" {
				if d.Reason != ReasonAutonomyInvalid || !strings.Contains(d.Message, "looser") {
					t.Fatalf("the Dispatched condition %+v, want AutonomyInvalid saying the Flight's is looser", d)
				}
				return
			}
			if d.Reason != ReasonDispatched {
				t.Fatalf("the Dispatched condition %+v", d)
			}
			page, err := cl.Events(context.Background(), api.EventsParams{})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range page.Events {
				if e.Type == "task.dispatched" {
					var p tower.Dispatched
					if err := json.Unmarshal(e.Payload, &p); err != nil {
						t.Fatal(err)
					}
					if p.Posture != c.want {
						t.Fatalf("dispatched under %q, want %q", p.Posture, c.want)
					}
					return
				}
			}
			t.Fatal("no task.dispatched")
		})
	}
}
