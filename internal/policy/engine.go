package policy

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/ar4mirez/lucy/internal/pathguard"
)

// RuleSetVersion is the version of the rule set. Bump it whenever any non-test source file of this
// package changes; the committed testdata/ruleset.sum, checked by TestRuleSetVersioned, records the
// SHA-256 of every version and refuses a changed rule set under an old version.
const RuleSetVersion = 2

//go:embed *.go
var ruleSources embed.FS

// ruleSourceNames lists the embedded non-test source files of this package, sorted by name.
func ruleSourceNames() []string {
	entries, err := fs.ReadDir(ruleSources, ".")
	if err != nil {
		panic("policy: rule sources missing from the binary: " + err.Error()) // a build error
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), "_test.go") {
			names = append(names, e.Name())
		}
	}
	return names
}

// ruleSetSHA256 is computed once from every non-test source file of this package, in sorted order,
// and the path guard's version.
var ruleSetSHA256 = func() string {
	h := sha256.New()
	fmt.Fprintf(h, "lucy policy rule set\nversion %d\npathguard %d\n", RuleSetVersion, pathguard.Version)
	for _, name := range ruleSourceNames() {
		data, err := ruleSources.ReadFile(name)
		if err != nil {
			panic("policy: rule source missing from the binary: " + name) // a build error
		}
		fmt.Fprintf(h, "%s %d\n", name, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}()

// RuleSet identifies the rule set this build evaluates.
type RuleSet struct {
	Version int
	SHA256  string
	Rules   []RuleInfo
}

// RuleInfo describes one rule.
type RuleInfo struct {
	ID      string
	Summary string
}

// CurrentRuleSet returns this build's rule set: its version, hash and rules in evaluation order.
func CurrentRuleSet() RuleSet {
	rs := RuleSet{Version: RuleSetVersion, SHA256: ruleSetSHA256}
	for _, r := range rules {
		rs.Rules = append(rs.Rules, RuleInfo{r.ID, r.Summary})
	}
	return rs
}

// Evaluate decides a request against the facts. It is pure: no I/O, no clock, no shared state.
// It is fail-safe: a request or facts it cannot evaluate, and any panic inside a rule, are a deny.
func Evaluate(req Request, facts Facts) (d Decision) {
	d = Decision{RuleSetVersion: RuleSetVersion, RuleSetSHA256: ruleSetSHA256}
	defer func() {
		if r := recover(); r != nil {
			d.Outcome, d.Rule, d.Reason, d.ApprovalsUsed = Deny, "engine.error", "evaluation failed; failing safe", nil
		}
	}()
	d.ActionSHA256, d.FactsSHA256 = ActionDigest(req), FactsDigest(req.Kind, facts)
	for _, in := range req.Inputs {
		if !Trusted(in.Source) {
			d.Tainted = true
		}
	}
	c := &evalCtx{req: req, facts: facts, action: d.ActionSHA256, bound: d.FactsSHA256}
	for _, r := range rules {
		v, ok := r.eval(c)
		if !ok {
			continue
		}
		if v.outcome != Allow && v.outcome != Deny && v.outcome != RequireCaptain {
			return failSafe(d, "rule "+r.ID+" returned outcome "+strconv.Quote(string(v.outcome)))
		}
		d.Outcome, d.Rule, d.Reason, d.ApprovalsUsed = v.outcome, r.ID, v.reason, nil
		if v.outcome == Allow {
			d.ApprovalsUsed = v.approvals
		}
		return d
	}
	return failSafe(d, "no rule decided")
}

// Refuse returns the fail-safe deny for a request the daemon cannot evaluate, for example because
// it could not gather the facts. It names the engine.error rule, like any evaluation failure.
func Refuse(req Request, why string) Decision {
	d := Decision{RuleSetVersion: RuleSetVersion, RuleSetSHA256: ruleSetSHA256, ActionSHA256: ActionDigest(req),
		FactsSHA256: FactsDigest(req.Kind, Facts{})}
	for _, in := range req.Inputs {
		d.Tainted = d.Tainted || !Trusted(in.Source)
	}
	return failSafe(d, why)
}

func failSafe(d Decision, why string) Decision {
	d.Outcome, d.Rule, d.Reason, d.ApprovalsUsed = Deny, "engine.error", why+"; failing safe", nil
	return d
}
