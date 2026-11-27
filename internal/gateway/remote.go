package gateway

import (
	"context"
	"errors"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
)

// Remote is a Recorder in a gateway process: it records through the daemon's API, so the policy
// decision behind each credential use is the daemon's and every request lands in its audit chain.
type Remote struct {
	Client *client.Client
}

var _ Recorder = Remote{}

// Routes reads org's routes from the daemon's config (gateway.routes).
func (r Remote) Routes(ctx context.Context, org string) ([]Route, error) {
	res, err := r.Client.GatewayRoutes(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]Route, 0, len(res.Routes))
	for _, ar := range res.Routes {
		rt := Route{Name: ar.Name, Upstream: ar.Upstream, Credential: ar.Credential, Scope: ar.Scope, Header: ar.Header, Prefix: ar.Prefix}
		for _, rule := range ar.Allow {
			rt.Allow = append(rt.Allow, Rule{Methods: rule.Methods, Path: rule.Path})
		}
		out = append(out, rt)
	}
	return out, nil
}

// Decide implements Recorder (gateway.use).
func (r Remote) Decide(ctx context.Context, org string, u Use) (Decision, error) {
	d, err := r.Client.GatewayUse(ctx, api.GatewayUseParams{Org: org, Route: u.Route, Method: u.Method, Path: u.Path, Upstream: u.Upstream})
	var ae *api.Error
	if errors.As(err, &ae) && ae.Code == api.CodeInvalidParams && ae.Data != nil && ae.Data.Field == "route" {
		return Decision{}, errors.Join(ErrUnknownRoute, err)
	}
	if err != nil {
		return Decision{}, err
	}
	return Decision{Seq: d.Decision, Cleared: d.Cleared, Rule: d.Rule, Reason: d.Reason}, nil
}

// Deny implements Recorder (gateway.deny).
func (r Remote) Deny(ctx context.Context, org string, d Denied) error {
	return r.Client.GatewayDeny(ctx, api.GatewayDenyParams{Org: org, Route: d.Route, Method: d.Method, Path: d.Path, Reason: capText(d.Reason)})
}

// Complete implements Recorder (gateway.complete).
func (r Remote) Complete(ctx context.Context, org string, c Completed) error {
	return r.Client.GatewayComplete(ctx, api.GatewayCompleteParams{Org: org, Decision: c.DecisionSeq, Status: c.Status,
		Scrubbed: c.Scrubbed, Error: capText(c.Error)})
}
