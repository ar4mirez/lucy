package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
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
	// AcceptProposal answers the flight's question, as the owner, with the supervisor's proposed
	// answer the owner saw: the proposal named by proposalID, for the question whose SHA-256 starts
	// with questionSHA256. It fails with ErrNotAsking, or ErrStaleProposal when the flight's
	// proposal or question is no longer the one named.
	AcceptProposal(ctx context.Context, flight, proposalID, questionSHA256 string) (tower.Status, error)
}

// ErrStaleProposal refuses to accept a proposal the owner was not shown: the flight's proposal or
// its question changed since, or it has none.
var ErrStaleProposal = errors.New("server: the flight's question or its proposal changed")

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

// AcceptProposal implements Questions: the owner's answer is the supervisor's proposed one.
func (q TowerQuestions) AcceptProposal(ctx context.Context, flight, proposalID, questionSHA256 string) (tower.Status, error) {
	q.Lock.Lock()
	defer q.Lock.Unlock()
	st, ok := q.Tower.Flight(flight)
	if !ok || st.State != tower.StateAsking {
		return tower.Status{}, ErrNotAsking
	}
	if st.ProposalID != proposalID || !strings.HasPrefix(questionDigest(st.Question), questionSHA256) {
		return tower.Status{}, ErrStaleProposal
	}
	if err := q.Tower.Answer(ctx, flight, st.Proposed); err != nil {
		return tower.Status{}, err
	}
	st, _ = q.Tower.Flight(flight)
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
			Question: f.Question, QuestionSHA256: questionDigest(f.Question), Since: f.Since.UTC(), ProposalID: f.ProposalID,
			ProposedBy: f.ProposedBy, Proposed: f.Proposed})
	}
	return out, nil
}

// questionDigest is the SHA-256, in hex, of a question exactly as questions.list shows it.
func questionDigest(q string) string {
	sum := sha256.Sum256([]byte(q))
	return hex.EncodeToString(sum[:])
}

// minQuestionDigest is the fewest hex characters of a question's digest that name it.
const minQuestionDigest = 16

func validQuestionDigest(d string) bool {
	if len(d) < minQuestionDigest || len(d) > sha256.Size*2 {
		return false
	}
	return strings.Trim(d, "0123456789abcdef") == ""
}

func (s *Server) answer(ctx context.Context, p *api.AnswerParams) (api.AnswerResult, error) {
	if s.opts.Questions == nil {
		return api.AnswerResult{}, api.NewError(api.CodeUnavailable,
			"this daemon hosts no Tower yet, so no flight can be answered; the Tower arrives with m1 slice 7a")
	}
	var st tower.Status
	var err error
	switch {
	case p.AcceptProposal != "" && p.Answer != "":
		return api.AnswerResult{}, api.FieldError(api.CodeInvalidParams, "answer", "give an answer or accept the proposal, not both")
	case p.AcceptProposal == "" && p.QuestionSHA256 != "":
		return api.AnswerResult{}, api.FieldError(api.CodeInvalidParams, "question_sha256", "question_sha256 goes with accept_proposal only")
	case p.AcceptProposal != "" && !validQuestionDigest(p.QuestionSHA256):
		return api.AnswerResult{}, api.FieldError(api.CodeInvalidParams, "question_sha256",
			"name the question you saw: its question_sha256 from `lucy questions`, at least 16 lowercase hex characters")
	case p.AcceptProposal != "":
		st, err = s.opts.Questions.AcceptProposal(ctx, p.Flight, p.AcceptProposal, p.QuestionSHA256)
	case p.Answer == "":
		return api.AnswerResult{}, api.FieldError(api.CodeInvalidParams, "answer", "an answer is required, unless accept_proposal is set")
	default:
		st, err = s.opts.Questions.Answer(ctx, p.Flight, p.Answer)
	}
	if errors.Is(err, ErrStaleProposal) {
		return api.AnswerResult{}, api.FieldError(api.CodeInvalidParams, "accept_proposal",
			"the question or its suggestion changed since you saw it; run `lucy questions` and accept the one shown now")
	}
	if errors.Is(err, ErrNotAsking) {
		return api.AnswerResult{}, api.FieldError(api.CodeInvalidParams, "flight", "the flight is not asking; run `lucy questions` to see the open ones")
	}
	if err != nil {
		return api.AnswerResult{}, err
	}
	return api.AnswerResult{Flight: st.ID, State: string(st.State)}, nil
}
