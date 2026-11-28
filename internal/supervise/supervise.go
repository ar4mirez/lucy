// Package supervise is lucy's model-backed supervisor (LUCY-RUN-010): rules first, then a small
// model, then a large one, then the owner. It is a tower.Supervisor, so it only proposes: a
// proposal is recorded and shown beside a Pilot's question, never reaches the Pilot and never
// closes the question; the owner answers (corpus rule channel-05).
//
// Two keys turn it on, both required: the org opts in through its Airspace's spec.supervisor (the
// gateway route its model calls go through, the small and the large model, a daily token budget),
// and the daemon arms it with --supervise, off by default. Every model call goes through the org's
// credential gateway, with a short-lived supervisor capability bound to that one route; the daemon
// holds no key. A call sends only the question and the flight context it needs (org, project, goal),
// never a secret or anything from the vault. Each call is recorded with its tokens
// (supervisor.called), counted against the org's budget for the UTC day; a spent budget, a refusal
// or an error records supervisor.declined and leaves the question to the owner.
package supervise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/capability"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/spec"
	"github.com/ar4mirez/lucy/internal/tower"
)

// By names this supervisor on the proposals it makes.
const By = "supervisor/model"

// Config is one org's opt-in, from its Airspace.
type Config struct {
	Org         string
	Route       string // the org's gateway route for model calls
	Small       string // the model asked first
	Large       string // the model asked when the small one is not confident
	DailyTokens int64  // the most tokens it may spend for the org in a UTC day
}

// Configs reads the orgs' opt-ins from their admitted Airspaces.
func Configs(rs []spec.Resource) map[string]Config {
	out := map[string]Config{}
	for _, r := range rs {
		if r.APIVersion != spec.LucyAPIVersion || r.Kind != "Airspace" {
			continue
		}
		s, ok := r.Spec["supervisor"].(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { v, _ := s[k].(string); return v }
		var n int64
		if num, ok := s["dailyTokens"].(json.Number); ok {
			n, _ = num.Int64()
		}
		out[r.Metadata.Namespace] = Config{Org: r.Metadata.Namespace, Route: str("route"), Small: str("smallModel"), Large: str("largeModel"), DailyTokens: n}
	}
	return out
}

// GatewayTransport returns, for each org, the transport that reaches its credential gateway's
// socket, dir/<org>.sock, as `lucy gateway` listens on by default.
func GatewayTransport(dir string) func(org string) http.RoundTripper {
	return func(org string) http.RoundTripper {
		sock := filepath.Join(dir, org+".sock")
		return &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
			Proxy: nil,
		}
	}
}

// Supervisor is the model-backed tower.Supervisor.
type Supervisor struct {
	Store  *eventstore.Store
	Issuer *capability.Issuer
	// Transport reaches an org's gateway: GatewayTransport in the daemon. Tests inject one that
	// reaches a fake model server; no test calls a real model.
	Transport func(org string) http.RoundTripper
	Now       func() time.Time

	mu   sync.Mutex
	orgs map[string]Config
}

var _ tower.Supervisor = (*Supervisor)(nil)

// SetOrgs replaces the orgs that opted in, as a config load or reload reads them.
func (s *Supervisor) SetOrgs(orgs map[string]Config) {
	s.mu.Lock()
	s.orgs = orgs
	s.mu.Unlock()
}

func (s *Supervisor) config(org string) (Config, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.orgs[org]
	return c, ok
}

func (s *Supervisor) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Day is the UTC day a budget is counted in.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// ProposeAnswer implements tower.Supervisor: the small model first, the large one when the small is
// not confident, and the owner when neither is, or when the budget is spent, a call is refused or
// fails. An org that did not opt in is left to the owner without a call or a record.
func (s *Supervisor) ProposeAnswer(ctx context.Context, q tower.Escalation) (tower.Proposal, bool, error) {
	cfg, ok := s.config(q.Org)
	if !ok {
		return tower.Proposal{}, false, nil
	}
	for _, tier := range []struct{ name, model string }{{"small", cfg.Small}, {"large", cfg.Large}} {
		used, err := Used(ctx, s.Store, q.Org, Day(s.now()))
		if err != nil {
			return tower.Proposal{}, false, err
		}
		if used >= cfg.DailyTokens {
			return tower.Proposal{}, false, s.decline(ctx, q, fmt.Sprintf("the daily token budget of %d is spent", cfg.DailyTokens))
		}
		r, err := s.call(ctx, cfg, tier.model, q)
		if r.tokens > 0 {
			if rerr := s.record(ctx, q.Org, Called{Flight: q.Flight, Tier: tier.name, Model: tier.model,
				InputTokens: r.input, OutputTokens: r.output, Day: Day(s.now())}); rerr != nil {
				return tower.Proposal{}, false, rerr
			}
		}
		if err != nil {
			return tower.Proposal{}, false, s.decline(ctx, q, "the "+tier.name+" model's call failed: "+capText(err.Error(), 256))
		}
		if r.confident && strings.TrimSpace(r.answer) != "" {
			return tower.Proposal{By: By, Answer: capText(r.answer, 4096)}, true, nil
		}
	}
	return tower.Proposal{}, false, s.decline(ctx, q, "neither model was confident")
}

func (s *Supervisor) decline(ctx context.Context, q tower.Escalation, reason string) error {
	return s.record(ctx, q.Org, Declined{Flight: q.Flight, Reason: reason, Day: Day(s.now())})
}

func (s *Supervisor) record(ctx context.Context, org string, p eventstore.Payload) error {
	_, err := s.Store.Append(ctx, Stream(org), eventstore.AnyVersion, "daemon", p)
	return err
}

// The prompt sends the model only the question and the flight context it needs.
const system = "You suggest an answer to a question that a coding agent (a Pilot) asked while working on a task. " +
	"The owner decides; your answer is only a suggestion shown to them. Reply with one JSON object and nothing else: " +
	`{"answer": "<your suggested answer>", "confident": true|false}. ` +
	"Set confident to true only when the answer follows from the task's goal and the question alone. " +
	"Never ask for, invent or include a password, token, key or other secret."

type result struct {
	answer        string
	confident     bool
	input, output int64
	tokens        int64
}

const (
	maxResponse = 1 << 20
	callTimeout = 2 * time.Minute
	maxTokens   = 512
)

func (s *Supervisor) call(ctx context.Context, cfg Config, model string, q tower.Escalation) (result, error) {
	token, _, err := s.Issuer.IssueSupervisor(ctx, cfg.Org, cfg.Route, 0)
	if err != nil {
		return result{}, err
	}
	body, err := json.Marshal(map[string]any{
		"model": model, "max_tokens": maxTokens, "system": system,
		"messages": []map[string]any{{"role": "user", "content": fmt.Sprintf(
			"Org: %s\nProject: %s\nThe task's goal: %s\nThe Pilot's question: %s", q.Org, q.Project, q.Goal, q.Question)}},
	})
	if err != nil {
		return result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gateway/"+cfg.Route+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("X-Lucy-Capability", token)
	resp, err := (&http.Client{Transport: s.Transport(cfg.Org), Timeout: callTimeout}).Do(req)
	if err != nil {
		return result{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return result{}, fmt.Errorf("the gateway or the model answered %s", resp.Status)
	}
	var m struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return result{}, errors.New("the model's response is not a Messages API response")
	}
	clamp := func(n int64) int64 { return max(0, min(n, 1<<30)) }
	r := result{input: clamp(m.Usage.Input), output: clamp(m.Usage.Output)}
	r.tokens = r.input + r.output
	var text strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	var a struct {
		Answer    string `json:"answer"`
		Confident bool   `json:"confident"`
	}
	t := strings.TrimSpace(text.String())
	t = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(t, "```json"), "```"), "```")
	if err := json.Unmarshal([]byte(strings.TrimSpace(t)), &a); err != nil {
		return r, nil // not the JSON asked for: not confident
	}
	r.answer, r.confident = a.Answer, a.Confident
	return r, nil
}

func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
