package server

import (
	"context"
	"errors"
	"strings"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/shadow"
)

func (s *Server) shadowImport(ctx context.Context, p *api.ShadowTask) (api.ShadowImported, error) {
	if s.opts.Shadow == nil {
		return api.ShadowImported{}, api.NewError(api.CodeInvalidParams, "this daemon records no shadow imports")
	}
	t := shadow.Task{Org: p.Org, ID: p.Task, Class: p.Class, Goal: p.Goal, Repository: p.Repository, Posture: p.Posture}
	for _, a := range p.Actions {
		t.Actions = append(t.Actions, shadow.Action{At: a.At.UTC(), Action: a.Action, Text: a.Text, By: a.By, Checks: a.Checks})
	}
	caller, _ := api.CallerFrom(ctx)
	out, err := s.opts.Shadow.Import(ctx, caller.Actor(), t)
	switch {
	case errors.Is(err, shadow.ErrUnknownOrg):
		return api.ShadowImported{}, api.FieldError(api.CodeInvalidParams, "org", "the daemon's config has no admitted Airspace for this org")
	case errors.Is(err, shadow.ErrConflict):
		return api.ShadowImported{}, api.FieldError(api.CodeInvalidParams, "task",
			"a task with this id was imported before with other contents; give it a new id")
	case errors.Is(err, shadow.ErrRefused):
		return api.ShadowImported{}, api.NewError(api.CodeInvalidParams, strings.TrimPrefix(err.Error(), "shadow: refused: "))
	case err != nil:
		return api.ShadowImported{}, err
	}
	return api.ShadowImported{Org: t.Org, Task: t.ID, Outcome: string(out)}, nil
}

func (s *Server) shadowList(ctx context.Context, p *api.ShadowListParams) (api.ShadowList, error) {
	rows, err := shadow.List(ctx, s.opts.Store, p.Org)
	if err != nil {
		return api.ShadowList{}, err
	}
	out := api.ShadowList{Tasks: []api.ShadowTaskInfo{}}
	for _, r := range rows {
		out.Tasks = append(out.Tasks, api.ShadowTaskInfo{Org: r.Org, Task: r.Task, Class: r.Class, Posture: r.Posture, Actions: r.Actions, Seq: r.Seq})
	}
	return out, nil
}
