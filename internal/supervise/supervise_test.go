package supervise_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/capability"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/supervise"
	"github.com/ar4mirez/lucy/internal/tower"
)

// The oracle is LUCY-RUN-010 and decision 073: rules, then a small model, then a large one, then the
// owner; every call through the org's gateway route with a supervisor capability; only the question
// and the flight's org, project and goal are sent; tokens count against the org's daily budget, and
// a spent budget, a refusal or an error records supervisor.declined and proposes nothing.

var ctx = context.Background()

// fakeModel is a fake Messages API: no real model is called anywhere in these tests.
type fakeModel struct {
	mu       sync.Mutex
	replies  map[string]string // model -> the text it answers with
	status   int               // non-zero: every call answers with this status
	usage    int64             // input and output tokens each call reports
	requests []seen
}

type seen struct {
	Path, Capability, Model, Prompt, System string
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var m struct {
		Model    string `json:"model"`
		System   string `json:"system"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &m)
	f.mu.Lock()
	defer f.mu.Unlock()
	prompt := ""
	if len(m.Messages) > 0 {
		prompt = m.Messages[0].Content
	}
	f.requests = append(f.requests, seen{Path: r.URL.Path, Capability: r.Header.Get("X-Lucy-Capability"), Model: m.Model, Prompt: prompt, System: m.System})
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"content": []map[string]any{{"type": "text", "text": f.replies[m.Model]}},
		"usage":   map[string]any{"input_tokens": f.usage, "output_tokens": f.usage},
	})
}

type fixture struct {
	store *eventstore.Store
	iss   *capability.Issuer
	sup   *supervise.Supervisor
	model *fakeModel
	now   time.Time
}

func open(t *testing.T, budget int64) *fixture {
	t.Helper()
	dir := t.TempDir()
	reg, err := eventstore.NewRegistry(append(append(runner.Payloads(), capability.Payloads()...), supervise.Payloads()...)...)
	if err != nil {
		t.Fatal(err)
	}
	st, err := eventstore.Open(ctx, filepath.Join(dir, "lucy", "lucy.db"), eventstore.Options{Registry: reg,
		Projectors:  []eventstore.Projector{runner.Projector{}, capability.Projector{}, supervise.Projector{}},
		SnapshotDir: filepath.Join(dir, "lucy", "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{store: st, model: &fakeModel{replies: map[string]string{}, usage: 10}, now: time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)}
	srv := httptest.NewServer(f.model)
	t.Cleanup(srv.Close)
	f.iss = &capability.Issuer{Store: st, Leases: runner.NewBroker(st, nil), Now: func() time.Time { return f.now }}
	f.sup = &supervise.Supervisor{Store: st, Issuer: f.iss, Models: true, Now: func() time.Time { return f.now },
		Transport: func(string) http.RoundTripper { return rewrite{to: srv.URL} }}
	f.sup.SetOrgs(map[string]supervise.Config{"acme": {Org: "acme", Route: "models", Small: "small-1", Large: "large-1", DailyTokens: budget}})
	return f
}

// rewrite sends what the supervisor addresses to its gateway to the fake model server instead.
type rewrite struct{ to string }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	u := *req.URL
	u.Scheme, u.Host = "http", strings.TrimPrefix(r.to, "http://")
	out := req.Clone(req.Context())
	out.URL, out.Host = &u, ""
	return http.DefaultTransport.RoundTrip(out)
}

var question = tower.Escalation{Org: "acme", Flight: "f1", Project: "web", Goal: "fix the login page", Posture: "green", Question: "which region, eu-west or us-east?"}

func (f *fixture) types(t *testing.T) []string {
	t.Helper()
	evs, err := f.store.ReadAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if strings.HasPrefix(e.Stream, supervise.StreamPrefix) {
			out = append(out, e.Type)
		}
	}
	return out
}

func TestTheSmallModelProposesWhenConfident(t *testing.T) {
	f := open(t, 1000)
	f.model.replies["small-1"] = `{"answer": "staging", "confident": true}`
	p, ok, err := f.sup.ProposeAnswer(ctx, question)
	if err != nil || !ok || p.By != supervise.By || p.Answer != "staging" {
		t.Fatalf("proposal %+v, %v, %v", p, ok, err)
	}
	if len(f.model.requests) != 1 {
		t.Fatalf("calls %+v, want the small model only", f.model.requests)
	}
	r := f.model.requests[0]
	if r.Path != "/models/v1/messages" || r.Model != "small-1" || !strings.HasPrefix(r.Capability, capability.Prefix) {
		t.Fatalf("the call %+v: want the org's model route, with a capability", r)
	}
	for _, want := range []string{"acme", "web", "fix the login page", "which region, eu-west or us-east?"} {
		if !strings.Contains(r.Prompt, want) {
			t.Errorf("the prompt %q lacks %q", r.Prompt, want)
		}
	}
	c, err := f.iss.Verify(ctx, r.Capability, "acme", "models")
	if err != nil || !c.IsSupervisor() || c.Run != "supervisor/acme" {
		t.Fatalf("the call's capability %+v, %v: want a supervisor capability for acme's model route", c, err)
	}
	if got := strings.Join(f.types(t), ","); got != "supervisor.called" {
		t.Fatalf("events %s", got)
	}
	if used, _ := supervise.Used(ctx, f.store, "acme", "2026-11-28"); used != 20 {
		t.Fatalf("tokens used %d, want 20", used)
	}
}

func TestTheLargeModelIsAskedWhenTheSmallIsNotConfident(t *testing.T) {
	f := open(t, 1000)
	f.model.replies["small-1"] = `{"answer": "maybe staging", "confident": false}`
	f.model.replies["large-1"] = `{"answer": "production", "confident": true}`
	p, ok, err := f.sup.ProposeAnswer(ctx, question)
	if err != nil || !ok || p.Answer != "production" {
		t.Fatalf("proposal %+v, %v, %v", p, ok, err)
	}
	if len(f.model.requests) != 2 || f.model.requests[1].Model != "large-1" {
		t.Fatalf("calls %+v", f.model.requests)
	}
	if used, _ := supervise.Used(ctx, f.store, "acme", "2026-11-28"); used != 40 {
		t.Fatalf("tokens used %d, want 40", used)
	}
}

func TestAFencedReplyIsNotConfident(t *testing.T) {
	f := open(t, 1000)
	fenced := "```json\n{\"answer\": \"staging\", \"confident\": true}\n```"
	f.model.replies["small-1"] = fenced
	f.model.replies["large-1"] = fenced
	if p, ok, err := f.sup.ProposeAnswer(ctx, question); err != nil || ok {
		t.Fatalf("proposal %+v, %v, %v", p, ok, err)
	}
	if len(f.model.requests) != 2 || f.model.requests[1].Model != "large-1" {
		t.Fatalf("calls %+v: a fenced reply from the small model asks the large one", f.model.requests)
	}
	if got := strings.Join(f.types(t), ","); got != "supervisor.called,supervisor.called,supervisor.declined" {
		t.Fatalf("events %s", got)
	}
}

func TestNeitherConfidentLeavesItToTheOwner(t *testing.T) {
	f := open(t, 1000)
	f.model.replies["small-1"] = "I am not sure."
	f.model.replies["large-1"] = `{"answer": "", "confident": true}`
	if p, ok, err := f.sup.ProposeAnswer(ctx, question); err != nil || ok {
		t.Fatalf("proposal %+v, %v, %v", p, ok, err)
	}
	if got := strings.Join(f.types(t), ","); got != "supervisor.called,supervisor.called,supervisor.declined" {
		t.Fatalf("events %s", got)
	}
}

func TestASpentBudgetCallsNoModel(t *testing.T) {
	f := open(t, 30)
	f.model.replies["small-1"] = `{"answer": "staging", "confident": true}`
	if _, ok, err := f.sup.ProposeAnswer(ctx, question); err != nil || !ok {
		t.Fatalf("first: %v, %v", ok, err)
	}
	f.model.replies["small-1"] = `{"answer": "staging", "confident": false}`
	// 20 spent of 30: the small model is asked (now 40), and the large one is not.
	if _, ok, err := f.sup.ProposeAnswer(ctx, question); err != nil || ok {
		t.Fatalf("second: %v, %v", ok, err)
	}
	if len(f.model.requests) != 2 {
		t.Fatalf("calls %d, want 2: none past the budget", len(f.model.requests))
	}
	if _, ok, err := f.sup.ProposeAnswer(ctx, question); err != nil || ok || len(f.model.requests) != 2 {
		t.Fatalf("third: %v, %v, %d calls", ok, err, len(f.model.requests))
	}
	evs, _ := f.store.ReadAfter(ctx, 0, 1000)
	var reasons []string
	for _, e := range evs {
		if e.Type == "supervisor.declined" {
			var d supervise.Declined
			_ = json.Unmarshal(e.Payload, &d)
			reasons = append(reasons, d.Reason)
		}
	}
	if len(reasons) != 2 || !strings.Contains(reasons[0], "budget") || !strings.Contains(reasons[1], "budget") {
		t.Fatalf("declined %q", reasons)
	}
	// The next UTC day has its own budget.
	f.now = f.now.Add(24 * time.Hour)
	f.model.replies["small-1"] = `{"answer": "staging", "confident": true}`
	if _, ok, err := f.sup.ProposeAnswer(ctx, question); err != nil || !ok {
		t.Fatalf("the next day: %v, %v", ok, err)
	}
}

func TestConcurrentQuestionsShareOneBudget(t *testing.T) {
	f := open(t, 20)
	f.model.replies["small-1"] = `{"answer": "staging", "confident": true}`
	const n = 5
	var wg sync.WaitGroup
	proposed := make(chan bool, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := question
			q.Flight = fmt.Sprintf("f%d", i)
			_, ok, err := f.sup.ProposeAnswer(ctx, q)
			if err != nil {
				t.Error(err)
			}
			proposed <- ok
		}()
	}
	wg.Wait()
	close(proposed)
	var count int
	for ok := range proposed {
		if ok {
			count++
		}
	}
	f.model.mu.Lock()
	calls := len(f.model.requests)
	f.model.mu.Unlock()
	if calls != 1 || count != 1 {
		t.Fatalf("%d calls and %d proposals, want one of each within a budget for one call", calls, count)
	}
	evs, _ := f.store.ReadAfter(ctx, 0, 1000)
	var declined int
	for _, e := range evs {
		if e.Type == "supervisor.declined" {
			var d supervise.Declined
			_ = json.Unmarshal(e.Payload, &d)
			if !strings.Contains(d.Reason, "budget") {
				t.Errorf("declined for %q, want the budget", d.Reason)
			}
			declined++
		}
	}
	if declined != n-1 {
		t.Fatalf("declined %d, want %d", declined, n-1)
	}
}

func TestARefusedOrFailedCallIsDeclined(t *testing.T) {
	f := open(t, 1000)
	f.model.status = http.StatusUnauthorized // as the gateway refuses a capability
	if _, ok, err := f.sup.ProposeAnswer(ctx, question); err != nil || ok {
		t.Fatalf("%v, %v", ok, err)
	}
	if got := strings.Join(f.types(t), ","); got != "supervisor.declined" {
		t.Fatalf("events %s: a refused call spends no tokens", got)
	}
}

func TestAnOrgThatDidNotOptInIsNotSupervised(t *testing.T) {
	f := open(t, 1000)
	q := question
	q.Org = "beta"
	if _, ok, err := f.sup.ProposeAnswer(ctx, q); err != nil || ok || len(f.model.requests) != 0 || len(f.types(t)) != 0 {
		t.Fatalf("%v, %v, %d calls", ok, err, len(f.model.requests))
	}
}
