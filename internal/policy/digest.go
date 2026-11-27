package policy

// This file is part of the rule set: its source is hashed into RuleSetSHA256, so any change to
// which facts an approval binds to is a new rule set version (see engine.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// ActionDigest is the SHA-256 of the action a request asks for: its actor, kind and target, but
// not its inputs. An approval names this digest, so it approves one actor doing one thing to one
// object, however the request is later justified.
func ActionDigest(r Request) string {
	data, err := json.Marshal(struct {
		Actor  Actor  `json:"actor"`
		Kind   Kind   `json:"kind"`
		Target Target `json:"target"`
	}{r.Actor, r.Kind, r.Target})
	if err != nil {
		return "" // cannot happen for these field types; an empty digest matches no approval
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// FactsDigest is the SHA-256 of the facts an approval of a kind of action binds to, the facts the
// approver saw: for a merge the pull request's head SHA, base ref and base SHA; for a delete the
// symlink-resolved path; for a credential use the credential's scope; for a force-push, branch
// delete or history rewrite the project's default branch; for a container operation the live
// sessions in the org's container, so a confirmation lapses when a session starts or ends. Other
// kinds bind to no facts. It is the
// SHA-256 of the JSON object {"kind": kind, "facts": {...}} with exactly those fields, so it is
// deterministic and an approval made under one set of facts never matches another.
func FactsDigest(k Kind, f Facts) string {
	bound := map[string]string{}
	switch k {
	case KindMerge:
		bound = map[string]string{"head_sha": f.HeadSHA, "base_ref": f.BaseRef, "base_sha": f.BaseSHA}
	case KindDelete:
		bound = map[string]string{"resolved_path": f.ResolvedPath}
	case KindCredentialUse:
		bound = map[string]string{"credential_scope": f.CredentialScope}
	case KindForcePush, KindBranchDelete, KindHistoryRewrite:
		bound = map[string]string{"default_branch": f.DefaultBranch}
	case KindContainer:
		bound = map[string]string{"live_sessions": strings.Join(f.LiveSessions, "\n")}
	}
	data, err := json.Marshal(struct {
		Kind  Kind              `json:"kind"`
		Facts map[string]string `json:"facts"`
	}{k, bound})
	if err != nil {
		return "" // cannot happen for these field types; an empty digest matches no approval
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
