package server

import (
	"context"
	"encoding/json"
	"os"
	"slices"

	"github.com/ar4mirez/lucy/internal/api"
)

// bindAll binds every API method to its implementation. New checks this matches the API table.
func (s *Server) bindAll() map[string]handler {
	return map[string]handler{
		api.MethodDiscover: bind(s.discover),
		api.MethodHello:    bind(s.hello),
		api.MethodHealth:   bind(s.health),
		api.MethodStatus:   bind(s.status),
		api.MethodEvents:   bind(s.events),
		api.MethodShutdown: bind(s.shutdown),
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
