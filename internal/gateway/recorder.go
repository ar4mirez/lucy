package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/ar4mirez/lucy/internal/capability"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

// Use is a credential use a gateway asks to have decided and recorded before it forwards a
// request: which route, and what the worker asked for. The recorder takes the route's credential,
// scope and upstream from its own trusted config, never from the gateway.
type Use struct {
	Route    string
	Method   string
	Path     string // the gateway's recorded form, policy.RecordedResource of the worker's path; recorded as given
	Upstream string // the host the gateway will call, which must be the route's
	// Capability is the token the worker presented (LUCY-CRED-003). The recorder verifies it
	// before anything else; it is never recorded, only its id.
	Capability string
}

// Verifier checks a capability token for a use of an org's route. *capability.Issuer is one.
type Verifier interface {
	Verify(ctx context.Context, token, org, route string) (capability.Capability, error)
}

// Decision is what a recorder decided on a Use.
type Decision struct {
	Seq     int64 // the policy decision record
	Cleared bool
	Rule    string
	Reason  string
}

// Recorder decides and records a gateway's requests in the daemon's log, so every request is in
// the audit chain (LUCY-CRED-004). StoreRecorder is the daemon's; Remote reaches it over the API
// from a gateway process.
type Recorder interface {
	// Verify checks the capability a Use carries, before the gateway reads any credential or looks
	// up any upstream; ok is false when it is refused, and the refusal is recorded as
	// gateway.denied. Decide checks it again.
	Verify(ctx context.Context, org string, u Use) (ok bool, err error)
	// Decide decides the credential's use through the policy engine and records the decision and a
	// gateway.forwarded (or, when not cleared, gateway.denied) event, in one transaction.
	Decide(ctx context.Context, org string, u Use) (Decision, error)
	// Deny records a request refused before a credential was involved. Its Path is the gateway's
	// recorded form, recorded as given.
	Deny(ctx context.Context, org string, d Denied) error
	// Complete records how a forwarded request ended.
	Complete(ctx context.Context, org string, c Completed) error
}

// ErrUnknownRoute is a Use whose route the recorder's config does not have for the org, or
// whose upstream is not the route's.
var ErrUnknownRoute = errors.New("gateway: no such route for the org")

// StoreRecorder is the daemon's Recorder, over its event store and the gateway routes it loaded
// from config. It is safe for concurrent use.
type StoreRecorder struct {
	Store *eventstore.Store // must register Payloads() and policy.DecisionRecord
	// Capabilities verifies the token each request carries. Nil refuses every request.
	Capabilities Verifier

	mu     sync.RWMutex
	routes map[string]map[string]Route // org -> name -> route
}

// SetRoutes replaces the routes the recorder knows, by org: the daemon's loaded config.
func (s *StoreRecorder) SetRoutes(byOrg map[string][]Route) error {
	m := map[string]map[string]Route{}
	for org, rs := range byOrg {
		if e := orgLabel(org); e != "" {
			return fmt.Errorf("gateway: org %q %s", org, e)
		}
		m[org] = map[string]Route{}
		for _, r := range rs {
			if _, err := checkRoute(r); err != nil {
				return err
			}
			if _, dup := m[org][r.Name]; dup {
				return fmt.Errorf("gateway: org %s: route %q is defined twice", org, r.Name)
			}
			m[org][r.Name] = r
		}
	}
	s.mu.Lock()
	s.routes = m
	s.mu.Unlock()
	return nil
}

// Routes returns org's routes, sorted by name, and whether the org has a gateway config at all.
func (s *StoreRecorder) Routes(org string) ([]Route, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.routes[org]
	if !ok {
		return nil, false
	}
	out := make([]Route, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Route) int { return strings.Compare(a.Name, b.Name) })
	return out, true
}

func (s *StoreRecorder) route(org, name string) (route, bool) {
	s.mu.RLock()
	r, ok := s.routes[org][name]
	s.mu.RUnlock()
	if !ok {
		return route{}, false
	}
	rt, err := checkRoute(r)
	return rt, err == nil
}

// actor is the org's gateway, as the policy engine and the log name it.
func actor(org string) policy.Actor {
	return policy.Actor{ID: "org/" + org + "/gateway", Role: policy.RoleGateway, Org: org}
}

// Verify implements Recorder.
func (s *StoreRecorder) Verify(ctx context.Context, org string, u Use) (bool, error) {
	rt, err := s.known(org, u)
	if err != nil {
		return false, err
	}
	_, refused, err := s.capability(ctx, org, rt, u)
	return err == nil && refused == "", err
}

// known returns the route a Use names, which must be org's with the Use's upstream.
func (s *StoreRecorder) known(org string, u Use) (route, error) {
	if e := orgLabel(org); e != "" {
		return route{}, fmt.Errorf("gateway: org %q %s", org, e)
	}
	rt, ok := s.route(org, u.Route)
	if !ok || rt.upstream.Host != u.Upstream {
		return route{}, fmt.Errorf("%w: %s/%s", ErrUnknownRoute, org, u.Route)
	}
	return rt, nil
}

// capability verifies the token a Use carries: without a valid one for this org, route and a live
// run, nothing is decided and the refusal is recorded (LUCY-CRED-003). refused says why it was
// refused, empty when it was not.
func (s *StoreRecorder) capability(ctx context.Context, org string, rt route, u Use) (c capability.Capability, refused string, err error) {
	var cerr error
	if s.Capabilities == nil {
		cerr = errors.New("this daemon verifies no capabilities")
	} else {
		c, cerr = s.Capabilities.Verify(ctx, u.Capability, org, rt.Name)
	}
	if cerr == nil {
		return c, "", nil
	}
	refused = "capability refused: " + capabilityReason(cerr)
	d := Denied{Route: rt.Name, Method: u.Method, Path: u.Path, Reason: refused, Run: c.Run, Capability: c.ID}
	if _, err := s.Store.Append(ctx, Stream(org), eventstore.AnyVersion, actor(org).ID, d); err != nil {
		return c, refused, err
	}
	return c, refused, nil
}

// Decide implements Recorder.
func (s *StoreRecorder) Decide(ctx context.Context, org string, u Use) (Decision, error) {
	rt, err := s.known(org, u)
	if err != nil {
		return Decision{}, err
	}
	a := actor(org)
	c, refused, err := s.capability(ctx, org, rt, u)
	if err != nil {
		return Decision{}, err
	}
	if refused != "" {
		return Decision{Rule: "capability", Reason: refused}, nil
	}
	req := policy.Request{Actor: a, Kind: policy.KindCredentialUse,
		Target: policy.Target{Org: org, Resource: rt.Credential},
		Inputs: []policy.Input{{Source: policy.SourceConfig, Ref: "route/" + rt.Name}, {Source: policy.SourceTask, Ref: "run/" + c.Run},
			{Source: policy.SourceAgent, Ref: "gateway/" + rt.Name}}}
	facts := policy.Facts{CredentialScope: rt.Scope}
	d := policy.Evaluate(req, facts)
	out := Decision{Cleared: d.Outcome == policy.Cleared, Rule: d.Rule, Reason: d.Reason}
	err = s.Store.Update(ctx, func(tx *eventstore.Tx) error {
		evs, err := tx.Append(ctx, policy.Stream, eventstore.AnyVersion, a.ID, policy.NewRecord(req, facts, d))
		if err != nil {
			return err
		}
		out.Seq = evs[0].Seq
		var p eventstore.Payload = Forwarded{Route: rt.Name, Method: u.Method, Path: u.Path, Upstream: rt.upstream.Host, DecisionSeq: out.Seq,
			Run: c.Run, Capability: c.ID}
		if !out.Cleared {
			p = Denied{Route: rt.Name, Method: u.Method, Path: u.Path, Reason: "the policy engine did not clear the credential: " + d.Rule,
				Run: c.Run, Capability: c.ID}
		}
		_, err = tx.Append(ctx, Stream(org), eventstore.AnyVersion, a.ID, p)
		return err
	})
	if err != nil {
		return Decision{}, err // unrecorded, so it authorizes nothing
	}
	return out, nil
}

// Deny implements Recorder.
func (s *StoreRecorder) Deny(ctx context.Context, org string, d Denied) error {
	if e := orgLabel(org); e != "" {
		return fmt.Errorf("gateway: org %q %s", org, e)
	}
	d.Reason = capText(d.Reason)
	_, err := s.Store.Append(ctx, Stream(org), eventstore.AnyVersion, actor(org).ID, d)
	return err
}

// Complete implements Recorder.
func (s *StoreRecorder) Complete(ctx context.Context, org string, c Completed) error {
	if e := orgLabel(org); e != "" {
		return fmt.Errorf("gateway: org %q %s", org, e)
	}
	c.Error = capText(c.Error)
	_, err := s.Store.Append(ctx, Stream(org), eventstore.AnyVersion, actor(org).ID, c)
	return err
}

// capText caps text a gateway sends for the log.
func capText(s string) string {
	return clip(s, 256)
}

// capabilityReason says why a capability was refused, in words that never hold the token.
func capabilityReason(err error) string {
	for _, e := range []error{capability.ErrInvalid, capability.ErrRevoked, capability.ErrExpired, capability.ErrScope, capability.ErrRunEnded} {
		if errors.Is(err, e) {
			return strings.TrimPrefix(e.Error(), "capability: ")
		}
	}
	return "it could not be checked"
}
