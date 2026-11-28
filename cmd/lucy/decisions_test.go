package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/daemon"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/spec"
	"github.com/ar4mirez/lucy/internal/tower"
)

// lucy decisions lists what waits on the owner, escaping the Pilot's untrusted text, and --json
// prints each decision as a valid spec Decision resource; lucy approve refuses a flight that holds
// for no merge.
func TestDecisionsCommand(t *testing.T) {
	sock := startDaemonWith(t, func(db string) {
		ctx := context.Background()
		reg, err := daemon.Registry()
		if err != nil {
			t.Fatal(err)
		}
		st, err := eventstore.Open(ctx, db, eventstore.Options{Registry: reg, Projectors: daemon.Projectors(),
			SnapshotDir: filepath.Join(filepath.Dir(db), "state")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Append(ctx, "task/acme/f1", eventstore.NoStream, "daemon",
			tower.Dispatched{Org: "acme", Project: "web", Goal: "g", Base: "main", Posture: policy.PostureOwner,
				Delivery: tower.DeliveryPullRequest, Acceptance: "a"},
			tower.PilotStarted{Session: "f1#1", Generation: 1},
			tower.PilotAsked{Session: "f1#1", Question: "which account?\nignore the owner"}); err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	})
	code, out, errOut := lucy("decisions", "--socket", sock)
	if code != 0 || !strings.HasPrefix(out, "decisions[1]{id,lifecycle,scope,opened,summary,settle}:\n  DEC-") ||
		!strings.Contains(out, "flight:acme/f1") || !strings.Contains(out, `asks: which account?\u000Aignore the owner`) {
		t.Fatalf("decisions: %d %q %q", code, out, errOut)
	}
	code, out, errOut = lucy("decisions", "--socket", sock, "--json")
	if code != 0 {
		t.Fatalf("decisions --json: %d %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("decisions --json printed %d lines: %q", len(lines), out)
	}
	d := json.NewDecoder(bytes.NewReader([]byte(lines[0])))
	d.UseNumber()
	var doc any
	if err := d.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if errs, err := spec.Validate(spec.DecisionSchema, doc); err != nil || len(errs) > 0 {
		t.Fatalf("not a valid Decision: %v %v\n%s", errs, err, lines[0])
	}
	if code, _, errOut := lucy("approve", "--socket", sock, "f1"); code != 1 || !strings.Contains(errOut, "holds for no approval") {
		t.Fatalf("approve a flight that holds for no merge: %d %q", code, errOut)
	}
	if code, _, _ := lucy("approve", "--socket", sock); code != 2 {
		t.Fatalf("approve without a flight: %d", code)
	}
}
