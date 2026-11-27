package server

import (
	"context"
	"errors"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/gateway"
)

// The gateway methods serve an org's credential gateway process: its routes from the daemon's own
// config, and the recording of every request it handles, so the decision behind each credential use
// is the daemon's and every request is in the audit chain (LUCY-CRED-001, LUCY-CRED-004).

func (s *Server) noGateway() *api.Error {
	return api.NewError(api.CodeUnavailable, "this daemon records no gateway requests; restart it with the event store")
}

func (s *Server) gatewayRoutes(_ context.Context, p *api.GatewayRoutesParams) (api.GatewayRoutes, error) {
	out := api.GatewayRoutes{Org: p.Org, Routes: []api.GatewayRoute{}}
	if s.opts.Gateways == nil {
		return out, nil
	}
	routes, _ := s.opts.Gateways.Routes(p.Org)
	for _, r := range routes {
		ar := api.GatewayRoute{Name: r.Name, Upstream: r.Upstream, Credential: r.Credential, Scope: r.Scope, Header: r.Header,
			Prefix: r.Prefix, Allow: []api.GatewayRule{}}
		for _, rule := range r.Allow {
			ar.Allow = append(ar.Allow, api.GatewayRule{Methods: rule.Methods, Path: rule.Path})
		}
		out.Routes = append(out.Routes, ar)
	}
	return out, nil
}

func gatewayUse(p *api.GatewayUseParams) gateway.Use {
	return gateway.Use{Route: p.Route, Method: p.Method, Path: p.Path, Upstream: p.Upstream, Capability: p.Capability}
}

func gatewayError(err error) error {
	if errors.Is(err, gateway.ErrUnknownRoute) {
		return api.FieldError(api.CodeInvalidParams, "route", "the daemon's config has no such route for the org, or not with that upstream")
	}
	return err
}

func (s *Server) gatewayVerify(ctx context.Context, p *api.GatewayUseParams) (api.GatewayVerified, error) {
	if s.opts.Gateways == nil {
		return api.GatewayVerified{}, s.noGateway()
	}
	ok, err := s.opts.Gateways.Verify(ctx, p.Org, gatewayUse(p))
	if err != nil {
		return api.GatewayVerified{}, gatewayError(err)
	}
	return api.GatewayVerified{Verified: ok}, nil
}

func (s *Server) gatewayUse(ctx context.Context, p *api.GatewayUseParams) (api.GatewayDecision, error) {
	if s.opts.Gateways == nil {
		return api.GatewayDecision{}, s.noGateway()
	}
	d, err := s.opts.Gateways.Decide(ctx, p.Org, gatewayUse(p))
	if err != nil {
		return api.GatewayDecision{}, gatewayError(err)
	}
	return api.GatewayDecision{Decision: d.Seq, Cleared: d.Cleared, Rule: d.Rule, Reason: d.Reason}, nil
}

func (s *Server) gatewayDeny(ctx context.Context, p *api.GatewayDenyParams) (api.GatewayRecorded, error) {
	if s.opts.Gateways == nil {
		return api.GatewayRecorded{}, s.noGateway()
	}
	if err := s.opts.Gateways.Deny(ctx, p.Org, gateway.Denied{Route: p.Route, Method: p.Method, Path: p.Path, Reason: p.Reason}); err != nil {
		return api.GatewayRecorded{}, err
	}
	return api.GatewayRecorded{Recorded: true}, nil
}

func (s *Server) gatewayComplete(ctx context.Context, p *api.GatewayCompleteParams) (api.GatewayRecorded, error) {
	if s.opts.Gateways == nil {
		return api.GatewayRecorded{}, s.noGateway()
	}
	c := gateway.Completed{DecisionSeq: p.Decision, Status: p.Status, Scrubbed: p.Scrubbed, Error: p.Error}
	if err := s.opts.Gateways.Complete(ctx, p.Org, c); err != nil {
		return api.GatewayRecorded{}, err
	}
	return api.GatewayRecorded{Recorded: true}, nil
}
