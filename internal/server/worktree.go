package server

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/worktree"
)

func worktreeSlot(s worktree.SlotInfo) api.WorktreeSlot {
	out := api.WorktreeSlot{Slot: s.Org + "/" + s.Repo + "/" + strconv.Itoa(s.Slot), Org: s.Org, Repo: s.Repo, Number: s.Slot,
		State: s.State, Run: s.Run, Branch: s.Branch, Head: s.Head, Reason: s.Reason, Path: s.Path}
	if s.State == worktree.StateLeased {
		out.Prep = s.Prep
	}
	return out
}

func (s *Server) worktreeList(ctx context.Context, p *api.WorktreeListParams) (api.WorktreeList, error) {
	out := api.WorktreeList{Slots: []api.WorktreeSlot{}}
	if s.opts.Worktrees == nil {
		return out, nil
	}
	slots, err := s.opts.Worktrees.List(ctx, p.Org)
	if err != nil {
		return api.WorktreeList{}, err
	}
	for _, sl := range slots {
		out.Slots = append(out.Slots, worktreeSlot(sl))
	}
	return out, nil
}

func (s *Server) worktreeDiscard(ctx context.Context, p *api.WorktreeDiscardParams) (api.WorktreeSlot, error) {
	if s.opts.Worktrees == nil {
		return api.WorktreeSlot{}, api.NewError(api.CodeUnavailable, "this daemon has no worktree pool")
	}
	if p.Confirm != p.Slot {
		return api.WorktreeSlot{}, api.FieldError(api.CodeInvalidParams, "confirm", "repeat the slot exactly, as <org>/<repo>/<number>")
	}
	parts := strings.Split(p.Slot, "/")
	n := 0
	if len(parts) == 3 {
		n, _ = strconv.Atoi(parts[2])
	}
	if n < 1 || strconv.Itoa(n) != parts[len(parts)-1] {
		return api.WorktreeSlot{}, api.FieldError(api.CodeInvalidParams, "slot", "name a slot as <org>/<repo>/<number>, as `lucy worktree ls` shows it")
	}
	caller, _ := api.CallerFrom(ctx)
	sl, err := s.opts.Worktrees.Discard(ctx, parts[0], parts[1], n, caller.Actor())
	switch {
	case errors.Is(err, worktree.ErrNotHeld):
		return api.WorktreeSlot{}, api.FieldError(api.CodeInvalidParams, "slot", "no such slot; run `lucy worktree ls`")
	case errors.Is(err, worktree.ErrInUse):
		return api.WorktreeSlot{}, api.FieldError(api.CodeInvalidParams, "slot", "only a quarantined or broken slot is discarded; this one is free or leased")
	case errors.Is(err, worktree.ErrInvalid):
		return api.WorktreeSlot{}, api.FieldError(api.CodeInvalidParams, "slot", "name a slot as <org>/<repo>/<number>, as `lucy worktree ls` shows it")
	case err != nil:
		return api.WorktreeSlot{}, err
	}
	return worktreeSlot(sl), nil
}
