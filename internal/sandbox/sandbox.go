// Package sandbox is lucy's runtime seam for org containers: the ai-sdlc Sandbox binding
// (LUCY-SPEC-016, LUCY-SBX-002). A Provider runs an org's container; berth is the first one
// (internal/sandbox/berth), reached only through its documented API (D3a). The core never talks to
// a provider directly: it asks the Sandbox, which decides every operation that would stop the work
// in a container through the policy engine first.
//
// Starting, restarting or stopping an org's container stops every session running in it. So the
// Sandbox lists the runs live in that container, asks the policy engine with those sessions as
// facts, records the decision, and only on a clearance calls the provider. With no live session
// the operation is cleared; with live sessions it is holding until the owner confirms stopping
// exactly those sessions, recorded through the daemon's own log, and a confirmation lapses when a
// session starts or ends (LUCY-SBX-003). A cleared restart or down first holds the container in
// the runner broker, in one transaction with checking that its live sessions are still the ones
// decided on, so no session is placed in it until the provider is done; the hold is released
// after, and expires by itself if the daemon dies first.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

// Op is an operation on an org's container. Each stops the work running in it.
type Op string

// Operations, named as policy.ContainerOps names them.
const (
	OpUp      Op = "up"      // start the org: its container is (re)created
	OpRestart Op = "restart" // recreate the running container, applying pending changes
	OpDown    Op = "down"    // stop the org and remove its container
)

// Valid reports whether o is a known operation.
func (o Op) Valid() bool { return policy.ContainerOps[string(o)] }

// Status is an org's container as its provider reports it.
type Status struct {
	Org       string `json:"org"`
	Running   bool   `json:"running"`
	Container string `json:"container,omitempty"` // the provider's name for it
}

// Provider errors. A provider wraps one of these so the core can tell them apart.
var (
	ErrNotFound    = errors.New("sandbox: no such org")
	ErrRefused     = errors.New("sandbox: the provider refused the operation")
	ErrState       = errors.New("sandbox: the org is not in a state for this")
	ErrUnsupported = errors.New("sandbox: the provider does not offer this")
	ErrUnavailable = errors.New("sandbox: the provider is unavailable")
)

// Provider runs org containers. It does what it is asked without asking anything itself: only a
// Sandbox calls Do, after the policy engine cleared the operation and the decision was recorded.
// A provider is either code inside lucy or a client of an external product's API, checked by a
// versioned contract test.
type Provider interface {
	// Type is the provider's type, as an AdapterBinding names it ("berth").
	Type() string
	// Status reads an org's container. It changes nothing.
	Status(ctx context.Context, org string) (Status, error)
	// Do runs op on the org's container and returns when it has ended.
	Do(ctx context.Context, org string, op Op) error
}

// Sessions lists the sessions live in an org's container, the runner broker's leased runs placed
// there, and holds the container so that no new session is placed in it (runner.Broker).
type Sessions interface {
	// LiveInContainer lists the sessions live in org's container, sorted.
	LiveInContainer(ctx context.Context, org string) ([]string, error)
	// HoldContainer holds org's container for ttl if the sessions live in it are exactly live, in
	// one step; held is false otherwise.
	HoldContainer(ctx context.Context, org string, live []string, ttl time.Duration) (held bool, err error)
	// ReleaseContainer ends the hold.
	ReleaseContainer(ctx context.Context, org string) error
}

// HoldTTL bounds a restart or down: the provider is given this long, and the container's hold
// expires after it, so a daemon that dies mid-operation leaves no hold behind for good.
const HoldTTL = 15 * time.Minute

// recordTimeout bounds what is recorded after the provider ran, which is recorded even when the
// request's context has ended: by then the provider may have acted.
const recordTimeout = 10 * time.Second

// ErrSessionsChanged is Request's error when a session started or ended in the container while
// the request was being decided. Nothing was recorded or done; ask again.
var ErrSessionsChanged = errors.New("sandbox: the container's live sessions changed while deciding; ask again")

// ActorOwner is the actor of the owner's confirmations; ActorDaemon of the rest.
const (
	ActorOwner  = "owner"
	ActorDaemon = "daemon"
)

// StreamPrefix starts the stream of each org's container operations: "sandbox/<org>".
const StreamPrefix = "sandbox/"

// Stream is the stream of org's container operations.
func Stream(org string) string { return StreamPrefix + org }

// Sandbox is the guarded front of a Provider. It is safe for concurrent use: one operation runs
// at a time, so two requests can never both act on the same container on one decision.
//
// The store must register Payloads() and policy.DecisionRecord.
type Sandbox struct {
	store    *eventstore.Store
	provider Provider
	sessions Sessions

	mu sync.Mutex
}

// New returns a Sandbox in front of provider. Every argument is required.
func New(store *eventstore.Store, provider Provider, sessions Sessions) (*Sandbox, error) {
	if store == nil || provider == nil || sessions == nil {
		return nil, errors.New("sandbox: New needs a store, a provider and the live sessions")
	}
	return &Sandbox{store: store, provider: provider, sessions: sessions}, nil
}

// Status reads an org's container from the provider. A read needs no decision.
func (s *Sandbox) Status(ctx context.Context, org string) (Status, error) {
	if err := checkOrg(org); err != nil {
		return Status{}, err
	}
	return s.provider.Status(ctx, org)
}

// Requester is who asks for an operation and what the request rests on: the daemon on its own
// account (an input from the daemon), or the owner through the API (an input from the owner).
type Requester struct {
	Actor  policy.Actor
	Inputs []policy.Input
}

// Daemon is the daemon asking on its own account, for the reason ref names.
func Daemon(ref string) Requester {
	return Requester{Actor: policy.Actor{ID: ActorDaemon, Role: policy.RoleDaemon},
		Inputs: []policy.Input{{Source: policy.SourceDaemon, Ref: ref}}}
}

// Result is what a request came to.
type Result struct {
	Outcome      policy.Outcome `json:"outcome"`
	Rule         string         `json:"rule"`
	Reason       string         `json:"reason"`
	DecisionSeq  int64          `json:"decision_seq"`  // the policy decision record
	LiveSessions []string       `json:"live_sessions"` // the sessions the decision was made on
	NeedsOwner   bool           `json:"needs_owner"`   // the owner may confirm stopping LiveSessions (Confirm)
	Acted        bool           `json:"acted"`         // the provider ran the operation
}

// Request decides op on org's container and, when cleared, runs it. The decision is recorded
// before the provider is called; an unrecorded decision authorizes nothing. A holding result
// names the live sessions the owner is asked to confirm stopping (Confirm); the requester asks
// again once the owner has. A cleared restart or down holds the container from before the
// decision is recorded until the provider is done (Sessions.HoldContainer). A provider failure
// after a clearance is recorded and returned.
func (s *Sandbox) Request(ctx context.Context, org string, op Op, by Requester) (_ Result, err error) {
	if err := checkOrg(org); err != nil {
		return Result{}, err
	}
	if !op.Valid() {
		return Result{}, fmt.Errorf("sandbox: unknown operation %q", op)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	live, err := s.sessions.LiveInContainer(ctx, org)
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: listing the sessions live in %s's container: %w", org, err)
	}
	h, err := s.history(ctx, org)
	if err != nil {
		return Result{}, err
	}
	req := policy.Request{Actor: by.Actor, Kind: policy.KindContainer, Target: policy.Target{Org: org, Resource: string(op)},
		Inputs: by.Inputs}
	facts := policy.Facts{SessionsKnown: true, LiveSessions: live, Approvals: h.unspent()}
	d := policy.Evaluate(req, facts)
	// Live sessions are what stops it, whether it is holding or a confirmation the owner gave
	// earlier no longer matches them: either way the owner's confirmation of these sessions would
	// clear it.
	needsOwner := d.Outcome != policy.Cleared && d.Rule == ruleLiveSessions && len(live) > 0
	res := Result{Outcome: d.Outcome, Rule: d.Rule, Reason: d.Reason, LiveSessions: live, NeedsOwner: needsOwner}
	if res.LiveSessions == nil {
		res.LiveSessions = []string{}
	}
	doCtx := ctx
	if d.Outcome == policy.Cleared && op != OpUp {
		var cancel context.CancelFunc
		doCtx, cancel = context.WithTimeout(ctx, HoldTTL) // ends before the hold does
		defer cancel()
		held, herr := s.sessions.HoldContainer(ctx, org, live, HoldTTL)
		if herr != nil {
			return Result{}, fmt.Errorf("sandbox: holding %s's container: %w", org, herr)
		}
		if !held {
			return Result{}, fmt.Errorf("%w: %s", ErrSessionsChanged, org)
		}
		defer func() {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
			defer cancel()
			if rerr := s.sessions.ReleaseContainer(rctx, org); rerr != nil {
				err = errors.Join(err, fmt.Errorf("sandbox: releasing %s's container: %w", org, rerr))
			}
		}()
	}
	err = s.store.Update(ctx, func(tx *eventstore.Tx) error {
		evs, err := tx.Append(ctx, policy.Stream, eventstore.AnyVersion, by.Actor.ID, policy.NewRecord(req, facts, d))
		if err != nil {
			return err
		}
		res.DecisionSeq = evs[0].Seq
		_, err = tx.Append(ctx, Stream(org), h.version, by.Actor.ID, Requested{
			Op: op, Outcome: d.Outcome, Rule: d.Rule, DecisionSeq: res.DecisionSeq, LiveSessions: live,
			ActionSHA256: d.ActionSHA256, FactsSHA256: d.FactsSHA256, ApprovalsUsed: d.ApprovalsUsed, NeedsOwner: needsOwner,
		})
		return err
	})
	if err != nil {
		return Result{}, err // unrecorded, so it authorizes nothing
	}
	if d.Outcome != policy.Cleared {
		return res, nil
	}
	actErr := s.provider.Do(doCtx, org, op)
	done := Acted{Op: op, DecisionSeq: res.DecisionSeq}
	if actErr != nil {
		done.Error = capText(actErr.Error())
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	if _, err := s.store.Append(rctx, Stream(org), eventstore.AnyVersion, ActorDaemon, done); err != nil {
		return res, errors.Join(actErr, err)
	}
	if actErr != nil {
		return res, actErr
	}
	res.Acted = true
	return res, nil
}

// Confirm records the owner's confirmation of the latest request for op on org's container that
// waits on the owner (Result.NeedsOwner): stopping exactly the sessions that request named, as the owner was shown them. It
// approves nothing else: not another operation, not another container, and not the same one once
// a session has started or ended since (the policy engine then denies it). decision is the
// waiting decision the owner was shown (Held); when a newer request has replaced it, Confirm
// refuses with ErrStale, so the owner never confirms sessions they were not shown. The owner's
// word comes only through the API, which calls Confirm for the authenticated owner and nobody else.
func (s *Sandbox) Confirm(ctx context.Context, org string, op Op, decision int64) (policy.Approval, error) {
	if err := checkOrg(org); err != nil {
		return policy.Approval{}, err
	}
	if !op.Valid() {
		return policy.Approval{}, fmt.Errorf("sandbox: unknown operation %q", op)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.history(ctx, org)
	if err != nil {
		return policy.Approval{}, err
	}
	held, ok := h.holding[op]
	if !ok {
		return policy.Approval{}, fmt.Errorf("%w: %s of %s", ErrNothingHeld, op, org)
	}
	if held.DecisionSeq != decision {
		return policy.Approval{}, fmt.Errorf("%w: %s of %s now waits on decision %d", ErrStale, op, org, held.DecisionSeq)
	}
	a := policy.Approval{
		Decision:     fmt.Sprintf("%s#%d", Stream(org), h.version+1), // this event's own id
		By:           policy.ByOwner,
		ActionSHA256: held.ActionSHA256,
		FactsSHA256:  held.FactsSHA256,
	}
	if _, err := s.store.Append(ctx, Stream(org), h.version, ActorOwner, Confirmed{Op: op, Approval: a, DecisionSeq: held.DecisionSeq}); err != nil {
		return policy.Approval{}, err
	}
	return a, nil
}

// ruleLiveSessions is the policy rule that holds an operation for the owner.
const ruleLiveSessions = "container.live-sessions"

// Confirm's errors.
var (
	// ErrNothingHeld: no request for the operation waits on the owner.
	ErrNothingHeld = errors.New("sandbox: no request is holding for the owner")
	// ErrStale: the decision named is not the one waiting now.
	ErrStale = errors.New("sandbox: the decision confirmed is no longer the one waiting")
)

// history is what an org's container stream says: its version, the latest holding request per
// operation, the owner's confirmations no cleared request has retired, and when lucy last started
// the container (zero unless the latest operation lucy ran on it was an `up` that succeeded).
type history struct {
	version   int64
	holding   map[Op]Requested
	confirmed []Confirmed
	started   time.Time
}

func (h history) unspent() []policy.Approval {
	var out []policy.Approval
	for _, c := range h.confirmed {
		out = append(out, c.Approval)
	}
	if len(out) > policy.MaxFactItems {
		out = out[len(out)-policy.MaxFactItems:] // the newest; older ones are for sessions long gone
	}
	return out
}

// history reads org's container stream. A cleared request for an operation retires every earlier
// confirmation of that operation, used or not, so one confirmation stops the container once and a
// confirmation the sessions outlived is never held against later ones.
func (s *Sandbox) history(ctx context.Context, org string) (history, error) {
	h := history{version: eventstore.NoStream, holding: map[Op]Requested{}}
	for after := int64(0); ; {
		evs, err := s.store.ReadStream(ctx, Stream(org), after, 500)
		if err != nil {
			return history{}, err
		}
		for _, e := range evs {
			h.version, after = e.StreamVersion, e.StreamVersion
			switch e.Type {
			case Requested{}.EventType():
				r, err := eventstore.DecodeAs[Requested](s.store.Registry(), e)
				if err != nil {
					return history{}, err
				}
				delete(h.holding, r.Op)
				if r.NeedsOwner {
					h.holding[r.Op] = r
				}
				if r.Outcome == policy.Cleared {
					h.confirmed = slices.DeleteFunc(h.confirmed, func(c Confirmed) bool { return c.Op == r.Op })
				}
			case Confirmed{}.EventType():
				c, err := eventstore.DecodeAs[Confirmed](s.store.Registry(), e)
				if err != nil {
					return history{}, err
				}
				h.confirmed = append(h.confirmed, c)
				delete(h.holding, c.Op)
			case Acted{}.EventType():
				a, err := eventstore.DecodeAs[Acted](s.store.Registry(), e)
				if err != nil {
					return history{}, err
				}
				h.started = time.Time{}
				if a.Op == OpUp && a.Error == "" {
					h.started = e.Time
				}
			}
		}
		if len(evs) < 500 {
			return h, nil
		}
	}
}

// checkOrg refuses an org that is not a valid org address.
func checkOrg(org string) error {
	if !ValidOrg(org) {
		return fmt.Errorf("sandbox: %q is not an org name", org)
	}
	return nil
}

// ValidOrg reports whether org is an org address a provider can be asked about: a name of
// lowercase letters, digits and '-' that starts with a letter or digit, at most 63 bytes,
// optionally followed by "@host" in the same alphabet for an org on another machine.
func ValidOrg(org string) bool {
	name, host, hasHost := strings.Cut(org, "@")
	return label(name) && (!hasHost || label(host))
}

func label(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

// capText caps a provider's error text for the log.
func capText(s string) string {
	const max = 512
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
