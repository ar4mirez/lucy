// Package workflowcheck holds a test that enforces lucy's GitHub Actions rules (SECURITY.md,
// docs/runbooks/release.md): actions pinned by full commit SHA, read-only default permissions,
// write scopes only on the release job, and releases triggered only by v* tags.
package workflowcheck
