package server

import (
	"context"
	"errors"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/decision"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

// Approvals lets the owner approve a flight's merge: the hosted Tower (TowerQuestions).
type Approvals interface {
	// Approve records the owner's approval of the merge the flight holds for, or fails with
	// ErrNotHolding when the flight holds for none.
	Approve(ctx context.Context, flight string) (tower.Status, error)
}

// ErrNotHolding refuses an approval of a flight that holds for no merge.
var ErrNotHolding = errors.New("server: the flight holds for no approval")

// Approve implements Approvals: the owner's approval, recorded as the owner's.
func (q TowerQuestions) Approve(ctx context.Context, flight string) (tower.Status, error) {
	q.Lock.Lock()
	defer q.Lock.Unlock()
	if st, ok := q.Tower.Flight(flight); !ok || st.State != tower.StateHolding {
		return tower.Status{}, ErrNotHolding
	}
	if err := q.Tower.Approve(ctx, flight, policy.ByOwner); err != nil {
		return tower.Status{}, err
	}
	st, _ := q.Tower.Flight(flight)
	return st, nil
}

func (s *Server) catalog(ctx context.Context, p *api.CatalogParams) (api.CatalogList, error) {
	rows, err := decision.List(ctx, s.opts.Store, p.All)
	if err != nil {
		return api.CatalogList{}, err
	}
	out := api.CatalogList{Decisions: []api.CatalogDecision{}}
	for _, r := range rows {
		d := api.CatalogDecision{ID: r.ID(), Kind: r.Kind, Org: r.Org, Subject: r.Subject, Scope: r.Scope(), Lifecycle: r.Lifecycle,
			Summary: r.Summary, Body: r.Body, Ref: r.Ref, Opened: r.Opened, Updated: r.Updated, AnsweredBy: r.AnsweredBy,
			AnsweredAt: r.AnsweredAt, ProposalID: r.ProposalID, ProposedBy: r.ProposedBy, Proposed: r.Proposed, ProposalTruncated: r.ProposalTruncated}
		if r.SupersededBy > 0 {
			d.SupersededBy = decision.Row{Seq: r.SupersededBy}.ID()
		}
		for _, o := range decision.Options(r.Kind, r.Org, r.Subject) {
			d.Options = append(d.Options, api.CatalogOption{ID: o.ID, Description: o.Description})
		}
		out.Decisions = append(out.Decisions, d)
	}
	return out, nil
}

func (s *Server) approve(ctx context.Context, p *api.ApproveParams) (api.ApproveResult, error) {
	if s.opts.Approvals == nil {
		return api.ApproveResult{}, api.NewError(api.CodeUnavailable, "this daemon hosts no Tower")
	}
	st, err := s.opts.Approvals.Approve(ctx, p.Flight)
	if errors.Is(err, ErrNotHolding) {
		return api.ApproveResult{}, api.NewError(api.CodeInvalidParams, "flight "+p.Flight+" holds for no approval; lucy decisions lists what does")
	}
	if err != nil {
		return api.ApproveResult{}, err
	}
	return api.ApproveResult{Flight: st.ID, State: string(st.State)}, nil
}

func (s *Server) flights(_ context.Context, _ *api.NoParams) (api.FlightList, error) {
	out := api.FlightList{Flights: []api.FlightInfo{}}
	if s.opts.Flights != nil {
		out.Flights = append(out.Flights, s.opts.Flights()...)
	}
	return out, nil
}

func (s *Server) reload(ctx context.Context, _ *api.NoParams) (api.ReloadResult, error) {
	if s.opts.Reload == nil {
		return api.ReloadResult{}, api.NewError(api.CodeUnavailable, "this daemon reloads no config")
	}
	return s.opts.Reload(ctx)
}
