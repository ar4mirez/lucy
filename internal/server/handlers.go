package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/audit"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/spec"
)

// bindAll binds every API method to its implementation. New checks this matches the API table.
func (s *Server) bindAll() map[string]handler {
	return map[string]handler{
		api.MethodDiscover:  bind(s.discover),
		api.MethodHello:     bind(s.hello),
		api.MethodHealth:    bind(s.health),
		api.MethodStatus:    bind(s.status),
		api.MethodEvents:    bind(s.events),
		api.MethodShutdown:  bind(s.shutdown),
		api.MethodEvaluate:  bind(s.evaluate),
		api.MethodDecisions: bind(s.decisions),
		api.MethodResources: bind(s.resources),
		api.MethodAudit:     bind(s.verifyAudit),
		api.MethodQueued:    bind(s.runnerQueued),
		api.MethodLease:     bind(s.runnerLease),
		api.MethodHeartbeat: bind(s.runnerHeartbeat),
		api.MethodSlot:      bind(s.runnerSlot),
		api.MethodFinish:    bind(s.runnerFinish),
		api.MethodPermit:    bind(s.runnerPermit),
		api.MethodQuestions: bind(s.questions),
		api.MethodDecList:   bind(s.catalog),
		api.MethodApprove:   bind(s.approve),
		api.MethodAnswer:    bind(s.answer),
		api.MethodRuns:      bind(s.runs),
		api.MethodHeld:      bind(s.sandboxHeld),
		api.MethodConfirm:   bind(s.sandboxConfirm),
		api.MethodGwRoutes:  bind(s.gatewayRoutes),
		api.MethodGwUse:     bind(s.gatewayUse),
		api.MethodGwVerify:  bind(s.gatewayVerify),
		api.MethodGwDeny:    bind(s.gatewayDeny),
		api.MethodGwDone:    bind(s.gatewayComplete),
		api.MethodCaps:      bind(s.capabilities),
		api.MethodRevoke:    bind(s.capabilityRevoke),
		api.MethodVaultRec:  bind(s.vaultRecord),
		api.MethodHostOrg:   bind(s.runnerHostOrg),
		api.MethodWtList:    bind(s.worktreeList),
		api.MethodWtDiscard: bind(s.worktreeDiscard),
	}
}

func (s *Server) discover(context.Context, *api.NoParams) (json.RawMessage, error) {
	return json.RawMessage(api.Document), nil
}

func (s *Server) hello(ctx context.Context, p *api.HelloParams) (api.Welcome, error) {
	chosen := 0
	for _, v := range p.APIVersions {
		if slices.Contains(api.SupportedVersions, v) && v > chosen {
			chosen = v
		}
	}
	if chosen == 0 {
		e := api.FieldError(api.CodeUnsupportedVersion, "api_versions", "no API version in common; see supported")
		e.Data.Supported = api.SupportedVersions
		return api.Welcome{}, e
	}
	caller, _ := api.CallerFrom(ctx)
	w := api.Welcome{
		APIVersion:    chosen,
		ServerVersion: s.opts.Version,
		Caller:        callerInfo(caller),
		Methods:       []string{},
	}
	for _, m := range api.Methods() {
		if caller.HasScope(m.Scope) {
			w.Methods = append(w.Methods, m.Name)
		}
	}
	return w, nil
}

func callerInfo(c api.Caller) api.CallerInfo {
	info := api.CallerInfo{Actor: c.Actor(), Transport: c.Transport, UID: c.UID, PID: c.PID, Device: c.Device, Scopes: []string{}}
	for _, sc := range c.Scopes {
		info.Scopes = append(info.Scopes, string(sc))
	}
	return info
}

func (s *Server) health(ctx context.Context, _ *api.NoParams) (api.Health, error) {
	if _, err := s.opts.Store.Head(ctx); err != nil {
		s.opts.Log.Error("health: event store did not answer", "err", err)
		return api.Health{}, api.NewError(api.CodeUnavailable, "the event store did not answer")
	}
	return api.Health{Status: "ok"}, nil
}

func (s *Server) status(ctx context.Context, _ *api.NoParams) (api.Status, error) {
	head, err := s.opts.Store.Head(ctx)
	if err != nil {
		return api.Status{}, err
	}
	st := api.Status{
		ServerVersion: s.opts.Version,
		APIVersion:    api.Version,
		PID:           os.Getpid(),
		StartedAt:     s.started.UTC(),
		UptimeSeconds: int64(s.opts.Now().Sub(s.started).Seconds()),
		EventHead:     head,
	}
	if s.opts.Refused != nil {
		st.RefusedConnections = s.opts.Refused()
	}
	return st, nil
}

func (s *Server) events(ctx context.Context, p *api.EventsParams) (api.EventsPage, error) {
	limit := p.Limit
	if limit == 0 {
		limit = api.DefaultEventsLimit
	}
	evs, err := s.opts.Store.ReadAfter(ctx, p.AfterSeq, limit+1)
	if err != nil {
		return api.EventsPage{}, err
	}
	head, err := s.opts.Store.Head(ctx)
	if err != nil {
		return api.EventsPage{}, err
	}
	page := api.EventsPage{Events: []api.EventInfo{}, Head: head, Next: p.AfterSeq}
	if len(evs) > limit {
		evs, page.More = evs[:limit], true
	}
	for _, e := range evs {
		page.Events = append(page.Events, api.EventInfo{
			Seq: e.Seq, Stream: e.Stream, StreamVersion: e.StreamVersion, Type: e.Type,
			TypeVersion: e.TypeVersion, Time: e.Time, Actor: e.Actor, Payload: e.Payload,
		})
		page.Next = e.Seq
	}
	return page, nil
}

func (s *Server) shutdown(context.Context, *api.NoParams) (api.ShutdownResult, error) {
	if s.opts.OnShutdown == nil {
		return api.ShutdownResult{}, api.NewError(api.CodeUnavailable, "this server cannot be stopped through the API")
	}
	s.opts.OnShutdown()
	return api.ShutdownResult{Stopping: true}, nil
}

// evaluate decides one action through the policy engine and records the decision before
// returning it (LUCY-POL-002, LUCY-POL-003). The actor is the caller: on the socket that is the
// local owner. Facts come only from the daemon (Options.PolicyFacts), never from the
// params; when they cannot be gathered the decision is a recorded denial.
func (s *Server) evaluate(ctx context.Context, p *api.EvaluateParams) (api.PolicyDecision, error) {
	caller, _ := api.CallerFrom(ctx)
	req := policy.Request{
		Actor:  policy.Actor{ID: caller.Actor(), Role: policy.RoleOwner},
		Kind:   policy.Kind(p.Kind),
		Target: policy.Target{Org: p.Org, Project: p.Project, Resource: p.Resource},
		Inputs: []policy.Input{{Source: policy.SourceOwner, Ref: "api/" + caller.Device}},
	}
	for _, in := range p.Inputs {
		req.Inputs = append(req.Inputs, policy.Input{Source: policy.Source(in.Source), Ref: in.Ref})
	}
	var facts policy.Facts
	var d policy.Decision
	if s.opts.PolicyFacts != nil {
		var err error
		if facts, err = s.opts.PolicyFacts(ctx, req); err != nil {
			s.opts.Log.Warn("policy facts unavailable; denying", "kind", p.Kind, "err", err)
			facts, d = policy.Facts{}, policy.Refuse(req, "the daemon could not gather the facts")
		}
	}
	if d.Outcome == "" {
		d = policy.Evaluate(req, facts)
	}
	rec := policy.NewRecord(req, facts, d)
	evs, err := s.opts.Store.Append(ctx, policy.Stream, eventstore.AnyVersion, caller.Actor(), rec)
	if err != nil {
		return api.PolicyDecision{}, err // unrecorded, so it authorizes nothing
	}
	return decisionInfo(evs[0], rec), nil
}

func (s *Server) decisions(ctx context.Context, p *api.DecisionsParams) (api.DecisionsPage, error) {
	limit := p.Limit
	if limit == 0 {
		limit = api.DefaultEventsLimit
	}
	evs, err := s.opts.Store.ReadStream(ctx, policy.Stream, p.After, limit+1)
	if err != nil {
		return api.DecisionsPage{}, err
	}
	page := api.DecisionsPage{Decisions: []api.PolicyDecision{}, Next: p.After}
	if len(evs) > limit {
		evs, page.More = evs[:limit], true
	}
	for _, e := range evs {
		rec, err := eventstore.DecodeAs[policy.DecisionRecord](s.opts.Store.Registry(), e)
		if err != nil {
			return api.DecisionsPage{}, err
		}
		page.Decisions = append(page.Decisions, decisionInfo(e, rec))
		page.Next = e.StreamVersion
	}
	return page, nil
}

func decisionInfo(e eventstore.Event, r policy.DecisionRecord) api.PolicyDecision {
	return api.PolicyDecision{
		Seq: e.Seq, ID: e.StreamVersion, Time: e.Time, Outcome: string(r.Outcome), Rule: r.Rule,
		Reason: r.Reason, RuleSetVersion: r.RuleSetVersion, RuleSetSHA256: r.RuleSetSHA256,
		ActionSHA256: r.ActionSHA256, FactsSHA256: r.FactsSHA256, Tainted: r.Tainted, ApprovalsUsed: r.ApprovalsUsed, Actor: r.Actor.ID,
		Role: string(r.Actor.Role), Kind: string(r.Kind), Org: r.Target.Org,
		Project: r.Target.Project, Resource: r.Target.Resource,
	}
}

// resources returns the loaded config resources with their status from the event store's
// resource_status projection: config holds the spec, the store holds the status (LUCY-SPEC-003).
func (s *Server) resources(ctx context.Context, p *api.ResourcesParams) (api.ResourcesPage, error) {
	limit := p.Limit
	if limit == 0 {
		limit = api.DefaultEventsLimit
	}
	all, err := spec.Statuses(ctx, s.opts.Store)
	if err != nil {
		return api.ResourcesPage{}, err
	}
	page := api.ResourcesPage{Resources: []api.ResourceInfo{}, Refused: []api.RefusedInfo{}, Next: p.Offset}
	if p.Offset == 0 {
		problems, truncated, err := spec.LatestProblems(ctx, s.opts.Store)
		if err != nil {
			return api.ResourcesPage{}, err
		}
		for _, pr := range problems {
			info := api.RefusedInfo{Source: pr.Source, Conditions: conditionInfos(pr.Conditions)}
			if c, ok := (spec.Result{Conditions: pr.Conditions}).Condition(spec.ConditionValid); ok {
				info.Reason = c.Reason
			}
			page.Refused = append(page.Refused, info)
		}
		page.RefusedTruncated = truncated
	}
	if p.Offset >= len(all) {
		return page, nil
	}
	all = all[p.Offset:]
	if len(all) > limit {
		all, page.More = all[:limit], true
	}
	for _, r := range all {
		page.Resources = append(page.Resources, api.ResourceInfo{
			APIVersion: r.APIVersion, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name,
			Source: r.Source, SpecSHA256: r.SpecSHA256, Conditions: conditionInfos(r.Conditions),
		})
	}
	page.Next = p.Offset + len(page.Resources)
	return page, nil
}

func conditionInfos(conds []spec.Condition) []api.ConditionInfo {
	out := []api.ConditionInfo{}
	for _, c := range conds {
		out = append(out, api.ConditionInfo{
			Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message,
			LastTransitionTime: c.LastTransitionTime,
		})
	}
	return out
}

// verifyAudit checks the audit chain against the event log. A broken chain is a result, not an
// error: the caller needs to know where it broke.
func (s *Server) verifyAudit(ctx context.Context, _ *api.NoParams) (api.AuditReport, error) {
	rep, err := audit.VerifyStore(ctx, s.opts.Store)
	var ve *audit.VerifyError
	switch {
	case errors.As(err, &ve):
		s.opts.Log.Warn("audit chain broken", "seq", ve.Seq, "reason", ve.Reason)
		return api.AuditReport{Entries: ve.Index - 1, BrokenAtSeq: ve.Seq, Reason: ve.Reason}, nil
	case err != nil:
		return api.AuditReport{}, err
	}
	return api.AuditReport{Verified: true, Entries: rep.Entries, Head: rep.Head}, nil
}
