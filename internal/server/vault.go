package server

import (
	"context"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/vault"
)

// vaultRecord records one phase of a change to an org's vault in the audit chain, by name only.
func (s *Server) vaultRecord(ctx context.Context, p *api.VaultRecordParams) (api.VaultRecorded, error) {
	if e := vault.CheckLabel(p.Org); e != "" {
		return api.VaultRecorded{}, api.FieldError(api.CodeInvalidParams, "org", "the org "+e)
	}
	if e := vault.CheckLabel(p.Name); e != "" {
		return api.VaultRecorded{}, api.FieldError(api.CodeInvalidParams, "name", "the credential name "+e)
	}
	if p.Op != vault.OpSet && p.Op != vault.OpRemove {
		return api.VaultRecorded{}, api.FieldError(api.CodeInvalidParams, "op", "the operation is set or remove")
	}
	switch p.Phase {
	case vault.PhaseRequested, vault.PhaseApplied:
		if p.Error != "" {
			return api.VaultRecorded{}, api.FieldError(api.CodeInvalidParams, "error", "only a failed change has an error")
		}
	case vault.PhaseFailed:
		if !vault.IsFailureReason(p.Error) {
			return api.VaultRecorded{}, api.FieldError(api.CodeInvalidParams, "error", "a failed change has one of the vault's fixed failure reasons")
		}
	default:
		return api.VaultRecorded{}, api.FieldError(api.CodeInvalidParams, "phase", "the phase is requested, applied or failed")
	}
	evs, err := s.opts.Store.Append(ctx, vault.Stream(p.Org), eventstore.AnyVersion, "owner",
		vault.Changed{Name: p.Name, Op: p.Op, Phase: p.Phase, Error: p.Error})
	if err != nil {
		return api.VaultRecorded{}, err
	}
	return api.VaultRecorded{Seq: evs[0].Seq}, nil
}
