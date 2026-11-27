package server

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/tower"
)

// Questions is the Tower's view of the Pilots' open questions (LUCY-RUN-013): a Pilot's question
// reaches the owner only as the Tower's record of it, and the owner's answer reaches the Pilot only
// through the Tower.
type Questions interface {
	// Open returns every flight that is asking, oldest question first.
	Open(ctx context.Context) ([]tower.Status, error)
	// Answer passes the owner's answer to the flight's Pilot. It fails with ErrNotAsking when the
	// flight is not asking.
	Answer(ctx context.Context, flight, answer string) (tower.Status, error)
}

// ErrNotAsking refuses an answer to a flight that has no open question.
var ErrNotAsking = errors.New("server: the flight is not asking")

// TowerQuestions adapts a Tower to Questions. A Tower is not safe for concurrent use, so every
// call holds Lock, which the Tower's own driver must hold around its steps too.
type TowerQuestions struct {
	Tower *tower.Tower
	Lock  *sync.Mutex
}

var _ Questions = TowerQuestions{}

// Open implements Questions.
func (q TowerQuestions) Open(context.Context) ([]tower.Status, error) {
	q.Lock.Lock()
	defer q.Lock.Unlock()
	var out []tower.Status
	for _, f := range q.Tower.Flights() {
		if f.State == tower.StateAsking {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b tower.Status) int { return a.Since.Compare(b.Since) })
	return out, nil
}

// Answer implements Questions.
func (q TowerQuestions) Answer(ctx context.Context, flight, answer string) (tower.Status, error) {
	q.Lock.Lock()
	defer q.Lock.Unlock()
	if st, ok := q.Tower.Flight(flight); !ok || st.State != tower.StateAsking {
		return tower.Status{}, ErrNotAsking
	}
	if err := q.Tower.Answer(ctx, flight, answer); err != nil {
		return tower.Status{}, err
	}
	st, _ := q.Tower.Flight(flight)
	return st, nil
}

func (s *Server) questions(ctx context.Context, _ *api.NoParams) (api.QuestionsList, error) {
	out := api.QuestionsList{Questions: []api.Question{}}
	if s.opts.Questions == nil {
		return out, nil // no Tower hosted yet, so no flight can be asking
	}
	open, err := s.opts.Questions.Open(ctx)
	if err != nil {
		return api.QuestionsList{}, err
	}
	for _, f := range open {
		out.Questions = append(out.Questions, api.Question{Flight: f.ID, Org: f.Org, Project: f.Project,
			Question: f.Question, Since: f.Since.UTC()})
	}
	return out, nil
}

func (s *Server) answer(ctx context.Context, p *api.AnswerParams) (api.AnswerResult, error) {
	if s.opts.Questions == nil {
		return api.AnswerResult{}, api.NewError(api.CodeUnavailable,
			"this daemon hosts no Tower yet, so no flight can be answered; the Tower arrives with m1 slice 7a")
	}
	st, err := s.opts.Questions.Answer(ctx, p.Flight, p.Answer)
	if errors.Is(err, ErrNotAsking) {
		return api.AnswerResult{}, api.FieldError(api.CodeInvalidParams, "flight", "the flight is not asking; run `lucy questions` to see the open ones")
	}
	if err != nil {
		return api.AnswerResult{}, err
	}
	return api.AnswerResult{Flight: st.ID, State: string(st.State)}, nil
}
