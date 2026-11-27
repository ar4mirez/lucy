// Package seccheck holds tests for lucy's own security scanning (LUCY-SEC-002,
// docs/runbooks/security-scans.md): every gosec suppression in the code names one rule and a
// reason and is listed in the reviewed baseline (docs/security/reviews/sast-baseline.md); the
// scanners run with no exclusion flags; and CI and `mise run security` pin the same versions.
package seccheck
