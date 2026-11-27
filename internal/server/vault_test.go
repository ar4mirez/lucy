package server

import (
	"os"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/vault"
)

// A vault change is recorded by name, op and phase only; anything else is refused.
func TestVaultRecord(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	for _, p := range []api.VaultRecordParams{
		{Org: "acme", Name: "github", Op: "set", Phase: "requested"},
		{Org: "acme", Name: "github", Op: "set", Phase: "applied"},
		{Org: "acme", Name: "npm", Op: "remove", Phase: "failed", Error: "vault: no such credential"},
	} {
		if r, err := f.client.VaultRecord(ctx, p); err != nil || r.Seq == 0 {
			t.Fatalf("%+v: %+v, %v", p, r, err)
		}
	}
	evs, err := f.store.ReadStream(ctx, vault.Stream("acme"), 0, 10)
	if err != nil || len(evs) != 3 {
		t.Fatalf("events %v, %v", evs, err)
	}
	c, err := eventstore.DecodeAs[vault.Changed](f.store.Registry(), evs[2])
	if err != nil || c.Name != "npm" || c.Op != "remove" || c.Phase != "failed" || c.Error == "" {
		t.Fatalf("%+v, %v", c, err)
	}
	for _, c := range []struct {
		p     api.VaultRecordParams
		field string
	}{
		{api.VaultRecordParams{Org: "Acme", Name: "github", Op: "set", Phase: "requested"}, "org"},
		{api.VaultRecordParams{Org: "acme", Name: "../x", Op: "set", Phase: "requested"}, "name"},
		{api.VaultRecordParams{Org: "acme", Name: "github", Op: "read", Phase: "requested"}, "op"},
		{api.VaultRecordParams{Org: "acme", Name: "github", Op: "set", Phase: "done"}, "phase"},
		{api.VaultRecordParams{Org: "acme", Name: "github", Op: "set", Phase: "applied", Error: "x"}, "error"},
	} {
		_, err := f.client.VaultRecord(ctx, c.p)
		if ae := wantCode(t, err, api.CodeInvalidParams); ae.Data == nil || ae.Data.Field != c.field {
			t.Errorf("%+v: %+v", c.p, ae)
		}
	}
}
