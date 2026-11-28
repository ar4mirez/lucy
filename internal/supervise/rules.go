package supervise

import (
	"context"
	"fmt"
	"regexp"

	"github.com/ar4mirez/lucy/internal/tower"
)

// Rule is one of an org's own rules: when Match finds the question, propose Propose, or, when
// Owner, send the question to the owner with no model call.
type Rule struct {
	Match   *regexp.Regexp
	Propose string
	Owner   bool
}

// Outcome is what the rules decided about a question.
type Outcome int

// The rules' outcomes.
const (
	Unmatched Outcome = iota // no rule matched: the models may be asked
	Proposed                 // a rule proposed its answer
	ToOwner                  // a rule sent the question to the owner, with no model call
)

// safety are the built-in safety rules, asked before the org's own: a question about any of these
// goes to the owner, never to a model, and no rule of the org can propose an answer to it. An org's
// rules can only add to them.
var safety = []struct {
	name string
	re   *regexp.Regexp
}{
	{"a secret or a credential", regexp.MustCompile(`(?i)\b(passwords?|passphrases?|passwd|secrets?|credentials?|tokens?|api[ _-]?keys?|private[ _-]?keys?|ssh[ _-]?keys?|access[ _-]?keys?|keys? ?pairs?|log[ -]?ins?|sign[ -]?ins?|2fa|mfa|otp|one[ -]?time (codes?|passwords?)|cookies?|session ids?|certificates?)\b`)},
	{"a payment", regexp.MustCompile(`(?i)\b(payments?|pay|paid|paying|credit[ -]?cards?|card numbers?|billing|invoices?|purchases?|subscriptions?|charges?|refunds?|bank|wire transfers?)\b`)},
	{"an irreversible or production change", regexp.MustCompile(`(?i)\b(prod|production|live (sites?|servers?|databases?|data|traffic)|deploy(s|ed|ing|ments?)?|releases?|delete[sd]?|deleting|drop(s|ped|ping)?|truncate|wipe[sd]?|destroy(s|ed|ing)?|purge[sd]?|force[ -]?push(es|ed|ing)?|rewrite (the )?history|reset --hard|rm -rf|irreversible|cannot be undone|can't be undone)\b`)},
}

// Limits on an org's rules, as the Airspace schema caps them.
const (
	maxRules   = 64
	maxMatch   = 512
	maxPropose = 4096
)

// parseRules reads an Airspace's spec.supervisor.rules, which admission already checked. It returns
// why they could not be read, if they could not, rather than a partial list: with a rule missing a
// question could reach a rule or model it was meant to be kept from.
func parseRules(v any) ([]Rule, string) {
	if v == nil {
		return nil, ""
	}
	list, ok := v.([]any)
	if !ok || len(list) > maxRules {
		return nil, "spec.supervisor.rules is not a list of at most 64 rules"
	}
	out := make([]Rule, 0, len(list))
	for i, item := range list {
		m, _ := item.(map[string]any)
		match, _ := m["match"].(string)
		propose, _ := m["propose"].(string)
		owner, _ := m["owner"].(bool)
		if match == "" || len(match) > maxMatch || len(propose) > maxPropose || (propose == "") == !owner {
			return nil, fmt.Sprintf("spec.supervisor.rules[%d] needs a match and either propose or owner: true", i)
		}
		re, err := regexp.Compile("(?i)" + match)
		if err != nil {
			return nil, fmt.Sprintf("spec.supervisor.rules[%d].match is not an RE2 regular expression", i)
		}
		out = append(out, Rule{Match: re, Propose: propose, Owner: owner})
	}
	return out, ""
}

// Rules asks the built-in safety rules, then the org's own, in order. The first that matches
// decides: a safety rule or an owner rule sends the question to the owner, recorded as
// supervisor.declined with no model call; a propose rule proposes its answer, as supervisor/rules.
// They call nothing outside the daemon and spend no tokens. An org that did not opt in is Unmatched,
// with no record.
func (s *Supervisor) Rules(ctx context.Context, q tower.Escalation) (tower.Proposal, Outcome, error) {
	cfg, ok := s.config(q.Org)
	if !ok {
		return tower.Proposal{}, Unmatched, nil
	}
	if cfg.Invalid != "" {
		return tower.Proposal{}, ToOwner, s.decline(ctx, q, "rules: the org's rules could not be read ("+cfg.Invalid+"); it needs the owner")
	}
	for _, r := range safety {
		if r.re.MatchString(q.Question) {
			return tower.Proposal{}, ToOwner, s.decline(ctx, q, "rules: it is about "+r.name+"; it needs the owner")
		}
	}
	for i, r := range cfg.Rules {
		if !r.Match.MatchString(q.Question) {
			continue
		}
		if r.Owner {
			return tower.Proposal{}, ToOwner, s.decline(ctx, q, fmt.Sprintf("rules: the org's rule %d sends it to the owner", i+1))
		}
		return tower.Proposal{By: ByRules, Answer: r.Propose}, Proposed, nil
	}
	return tower.Proposal{}, Unmatched, nil
}
