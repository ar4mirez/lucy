package server

import (
	"context"
	"errors"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/capability"
)

func capabilityInfo(c capability.Capability) api.CapabilityInfo {
	routes := c.Routes
	if routes == nil {
		routes = []string{}
	}
	return api.CapabilityInfo{ID: c.ID, Org: c.Org, Run: c.Run, Fence: c.Fence, Routes: routes, Issued: c.Issued.UTC(),
		Expires: c.Expires.UTC(), Revoked: c.Revoked, RevokedReason: c.RevokedReason}
}

func (s *Server) capabilities(ctx context.Context, p *api.CapabilitiesParams) (api.CapabilitiesList, error) {
	out := api.CapabilitiesList{Capabilities: []api.CapabilityInfo{}}
	if s.opts.Capabilities == nil {
		return out, nil
	}
	list, err := s.opts.Capabilities.List(ctx, p.All)
	if err != nil {
		return api.CapabilitiesList{}, err
	}
	for _, c := range list {
		out.Capabilities = append(out.Capabilities, capabilityInfo(c))
	}
	return out, nil
}

func (s *Server) capabilityRevoke(ctx context.Context, p *api.RevokeParams) (api.CapabilityInfo, error) {
	if s.opts.Capabilities == nil {
		return api.CapabilityInfo{}, api.NewError(api.CodeUnavailable, "this daemon issues no capabilities")
	}
	c, err := s.opts.Capabilities.Revoke(ctx, p.ID, "owner", p.Reason)
	if errors.Is(err, capability.ErrUnknown) {
		return api.CapabilityInfo{}, api.FieldError(api.CodeInvalidParams, "id", "no capability has this id; run `lucy capability ls`")
	}
	if err != nil {
		return api.CapabilityInfo{}, err
	}
	return capabilityInfo(c), nil
}
