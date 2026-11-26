package corpus

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ar4mirez/lucy/internal/audit"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

// approvalRules are the rules whose clearance must rest on an approval.
var approvalRules = map[string]bool{"delete.project": true, "git.owner": true}

// finalTypes end a flight's stream.
var finalTypes = map[string]bool{"task.takeoff": true, "task.merged_outside": true, "task.grounded": true}

// invariants hold for every scenario, whatever it does:
//
//  1. the audit chain verifies;
//  2. every takeoff cites a cleared merge Clearance recorded before it, for the same pull request
//     and head, and every merge lucy's account performed rests on such a Clearance;
//  3. nothing that needs the owner clears without an approval: a merge outside the green posture
//     or touching the fixed list, a delete in a project, a destructive git action; each approval
//     used is one the decision was offered (the engine checks its action and facts digests), and
//     an approval recorded on a flight is the owner's own event;
//  4. every cleared privileged action had a trusted input;
//  5. a flight's stream ends at its first final event, and a flight merged outside lucy never
//     records a takeoff;
//  6. each flight's Pilot sessions are generations 1, 2, 3, ... and a new one starts only after
//     the previous one exited: never two live sessions for one flight;
//  7. every answer a Pilot received was the owner's recorded answer.
func invariants(ctx context.Context, e *Env) []error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf("invariant: "+format, args...)) }
	if _, err := audit.VerifyStore(ctx, e.World.Store); err != nil {
		fail("audit chain: %v", err)
	}
	reg := e.World.Store.Registry()
	decisions := map[int64]policy.DecisionRecord{}
	approvals := map[string]string{} // "stream#version" -> actor
	streams := map[string][]eventstore.Event{}
	err := e.World.Store.Replay(ctx, 0, func(ev eventstore.Event) error {
		switch {
		case ev.Stream == policy.Stream:
			rec, err := eventstore.DecodeAs[policy.DecisionRecord](reg, ev)
			if err != nil {
				return err
			}
			decisions[ev.Seq] = rec
		case strings.HasPrefix(ev.Stream, "task/"):
			streams[ev.Stream] = append(streams[ev.Stream], ev)
			if ev.Type == (tower.Approved{}).EventType() {
				approvals[fmt.Sprintf("%s#%d", ev.Stream, ev.StreamVersion)] = ev.Actor
			}
		}
		return nil
	})
	if err != nil {
		return append(errs, err)
	}
	clearedFor := func(seq int64, before int64, repo string, pr int, head string) bool {
		r, ok := decisions[seq]
		return ok && seq < before && r.Outcome == policy.Cleared && r.Kind == policy.KindMerge &&
			r.Target.Org+"/"+r.Target.Project == repo && r.Target.Resource == "pr/"+strconv.Itoa(pr) && r.Facts.HeadSHA == head
	}
	for seq, r := range decisions {
		if r.Outcome != policy.Cleared {
			continue
		}
		if policy.Privileged(r.Kind) && !slices.ContainsFunc(r.Inputs, func(in policy.InputRecord) bool { return in.Trusted }) {
			fail("decision %d cleared a privileged %s with no trusted input", seq, r.Kind)
		}
		needs := approvalRules[r.Rule] || r.Kind == policy.KindMerge && (r.Facts.Posture != policy.PostureGreen || len(r.Facts.Exceptions) > 0)
		if needs && len(r.ApprovalsUsed) == 0 {
			fail("decision %d cleared %s by %s without an approval", seq, r.Kind, r.Rule)
		}
		for _, id := range r.ApprovalsUsed {
			if !slices.Contains(r.Facts.Approvals, id) {
				fail("decision %d used approval %s it was not offered", seq, id)
			}
			if strings.HasPrefix(id, "task/") && approvals[id] != tower.ActorOwner {
				fail("decision %d used approval %s, which is not an approval event by the owner", seq, id)
			}
		}
	}
	for stream, evs := range streams {
		final, outside := false, false
		gen, live := 0, false
		for _, ev := range evs {
			if final {
				fail("%s: %s at seq %d after the flight ended", stream, ev.Type, ev.Seq)
			}
			switch ev.Type {
			case "task.takeoff":
				t, err := eventstore.DecodeAs[tower.Takeoff](reg, ev)
				if err != nil {
					return append(errs, err)
				}
				if outside || !clearedFor(t.DecisionSeq, ev.Seq, repoOf(stream, evs, reg), t.PR, t.HeadSHA) {
					fail("%s: takeoff at seq %d does not rest on a cleared merge decision for its head", stream, ev.Seq)
				}
			case "task.merged_outside":
				outside = true
			case "task.pilot_started":
				p, err := eventstore.DecodeAs[tower.PilotStarted](reg, ev)
				if err != nil {
					return append(errs, err)
				}
				if live || p.Generation != gen+1 {
					fail("%s: Pilot generation %d started at seq %d while generation %d was live=%v", stream, p.Generation, ev.Seq, gen, live)
				}
				gen, live = p.Generation, true
			case "task.pilot_exited":
				live = false
			}
			final = finalTypes[ev.Type]
		}
	}
	lucy := e.World.Forge.Identity
	for _, m := range e.World.Forge.Merges() {
		if m.By != lucy {
			continue
		}
		ok := false
		for seq := range decisions {
			if clearedFor(seq, 1<<62, m.Repo, m.PR, m.HeadSHA) {
				ok = true
			}
		}
		if !ok {
			fail("the forge merged %s#%d at %s as lucy with no cleared decision for that head", m.Repo, m.PR, m.HeadSHA)
		}
	}
	started := map[string][]int{}
	for _, r := range e.World.Runtime.Started {
		started[r.Flight] = append(started[r.Flight], r.Generation)
	}
	for flight, gens := range started {
		for i, g := range gens {
			if g != i+1 {
				fail("flight %s: the runtime started generations %v", flight, gens)
				break
			}
		}
	}
	answered := 0
	for _, evs := range streams {
		for _, ev := range evs {
			if ev.Type == (tower.PilotAnswered{}).EventType() {
				answered++
				if ev.Actor != tower.ActorOwner {
					fail("an answer at seq %d was recorded by %s, not the owner", ev.Seq, ev.Actor)
				}
			}
		}
	}
	delivered := 0
	for _, as := range e.World.Runtime.Answers {
		delivered += len(as)
	}
	if delivered != answered {
		fail("%d answers reached Pilots, %d were the owner's recorded answers", delivered, answered)
	}
	return errs
}

// repoOf is the repository of a flight's stream, from its dispatched event.
func repoOf(stream string, evs []eventstore.Event, reg *eventstore.Registry) string {
	if len(evs) > 0 {
		if d, err := eventstore.DecodeAs[tower.Dispatched](reg, evs[0]); err == nil {
			return d.Org + "/" + d.Project
		}
	}
	return stream
}
