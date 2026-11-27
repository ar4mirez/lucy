// Package capability is lucy's capability tokens (LUCY-CRED-003): what a run presents to its
// org's credential gateway to use a route, scoped to (org, run, action, expiry).
//
// A token is 32 random bytes, issued by the daemon when a runner leases a run, and given only to
// that runner. The daemon keeps only its SHA-256: the log and the capabilities projection hold the
// digest, never the token. A token is valid only while all of these hold:
//
//   - it was issued and not revoked, and it is the org's that the gateway serves;
//   - the route asked for is one of the routes it was issued for (its actions);
//   - its own expiry has not passed (DefaultTTL after issue, and never past MaxTTL after its
//     lease's first token was issued);
//   - the run it was issued for is still leased under the same fence, and that lease has not
//     run out: so it dies with the run, and a token from a lease that was replaced is refused.
//
// Each heartbeat of a live lease renews the run's token (Renew): a fresh one, and the older ones
// revoked as rotated. The owner revokes one by its id (the first 16 hex digits of its digest) at
// any time.
package capability

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/runner"
)

// Prefix starts every token, so a secret scanner and a reader can tell one from other text.
const Prefix = "lcap_"

// Limits.
const (
	DefaultTTL = 5 * time.Minute // a token's own expiry, renewed by each heartbeat; the run's lease ends it sooner
	MaxTTL     = 4 * time.Hour   // no token outlives this after its lease's first token, whatever is asked
	MaxRoutes  = 64
	idHexLen   = 16
)

// Errors a verification refuses with. None names the token.
var (
	ErrInvalid  = errors.New("capability: not a token lucy issued")
	ErrRevoked  = errors.New("capability: revoked")
	ErrExpired  = errors.New("capability: expired")
	ErrScope    = errors.New("capability: not for this org or route")
	ErrRunEnded = errors.New("capability: its run is no longer leased under it")
)

// Capability is an issued token, as the daemon records it: never the token itself.
type Capability struct {
	ID            string    `json:"id"` // the first 16 hex digits of the token's SHA-256
	Org           string    `json:"org"`
	Run           string    `json:"run"`
	Fence         int64     `json:"fence"`
	Routes        []string  `json:"routes"`
	Issued        time.Time `json:"issued"`
	Start         time.Time `json:"start"` // when its lease's first token was issued
	Expires       time.Time `json:"expires"`
	Revoked       bool      `json:"revoked"`
	RevokedReason string    `json:"revoked_reason,omitempty"`
}

// Leases reads a run's lease: the runner broker.
type Leases interface {
	Run(ctx context.Context, run string) (runner.RunInfo, error)
}

// Issuer issues, verifies and revokes tokens over the daemon's store. The store must register
// Payloads() and the Projector.
type Issuer struct {
	Store  *eventstore.Store
	Leases Leases
	Now    func() time.Time // time.Now when nil
}

func (i *Issuer) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

// StreamPrefix starts each org's capability stream.
const StreamPrefix = "capability/"

// Stream is org's capability stream.
func Stream(org string) string { return StreamPrefix + org }

func digest(token string) [32]byte { return sha256.Sum256([]byte(token)) }

// ID returns a token's public id.
func ID(token string) string {
	d := digest(token)
	return hex.EncodeToString(d[:])[:idHexLen]
}

// Issue makes a token for org's run, leased under fence, for routes, expiring after ttl
// (DefaultTTL when zero, at most MaxTTL). It returns the token, which only the caller ever sees.
func (i *Issuer) Issue(ctx context.Context, org, run string, fence int64, routes []string, ttl time.Duration) (string, Capability, error) {
	if org == "" || run == "" || fence <= 0 {
		return "", Capability{}, errors.New("capability: Issue needs an org, a run and its fence")
	}
	if len(routes) > MaxRoutes {
		return "", Capability{}, fmt.Errorf("capability: more than %d routes", MaxRoutes)
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	ttl = min(ttl, MaxTTL)
	routes = slices.Clone(routes)
	slices.Sort(routes)
	routes = slices.Compact(routes)
	if routes == nil {
		routes = []string{}
	}
	now := i.now().UTC()
	token, c, ev, err := mint(Capability{Org: org, Run: run, Fence: fence, Routes: routes, Issued: now, Start: now, Expires: now.Add(ttl)})
	if err != nil {
		return "", Capability{}, err
	}
	if _, err := i.Store.Append(ctx, Stream(org), eventstore.AnyVersion, "daemon", ev); err != nil {
		return "", Capability{}, err
	}
	return token, c, nil
}

// mint makes a fresh token for c and the event that records its issue.
func mint(c Capability) (string, Capability, Issued, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", Capability{}, Issued{}, err
	}
	token := Prefix + base64.RawURLEncoding.EncodeToString(b)
	d := digest(token)
	c.ID = hex.EncodeToString(d[:])[:idHexLen]
	return token, c, Issued{Digest: hex.EncodeToString(d[:]), Run: c.Run, Fence: c.Fence, Routes: c.Routes,
		StartedAt: c.Start, ExpiresAt: c.Expires}, nil
}

// Renew issues run's lease under fence a fresh token, for the org and routes of the lease's newest
// token, and revokes every older token of the run (reason "rotated") in the same transaction. The
// new token expires DefaultTTL from now, but never later than MaxTTL after the lease's first token
// was issued. It refuses with ErrUnknown when the lease has no token, ErrRevoked when its newest
// token was revoked, and ErrExpired once the lease is MaxTTL old: none of these issues anything.
// The caller checks the lease is live.
func (i *Issuer) Renew(ctx context.Context, run string, fence int64) (string, Capability, error) {
	prev, _, err := scan(i.Store.QueryRowContext(ctx, `SELECT id, digest, org, run, fence, routes, issued_ns, start_ns, expires_ns, revoked,
		revoked_reason FROM capabilities WHERE run = ? AND fence = ? ORDER BY issued_ns DESC, rowid DESC LIMIT 1`, run, fence))
	if errors.Is(err, errNoRow) {
		return "", Capability{}, ErrUnknown
	}
	if err != nil {
		return "", Capability{}, err
	}
	if prev.Revoked {
		return "", Capability{}, ErrRevoked
	}
	now := i.now().UTC()
	end := prev.Start.Add(MaxTTL)
	if !now.Before(end) {
		return "", Capability{}, ErrExpired
	}
	expires := now.Add(DefaultTTL)
	if expires.After(end) {
		expires = end
	}
	token, c, ev, err := mint(Capability{Org: prev.Org, Run: run, Fence: fence, Routes: prev.Routes, Issued: now, Start: prev.Start,
		Expires: expires})
	if err != nil {
		return "", Capability{}, err
	}
	old, err := i.live(ctx, run, fence)
	if err != nil {
		return "", Capability{}, err
	}
	err = i.Store.Update(ctx, func(tx *eventstore.Tx) error {
		if _, err := tx.Append(ctx, Stream(c.Org), eventstore.AnyVersion, "daemon", ev); err != nil {
			return err
		}
		for _, o := range old {
			if _, err := tx.Append(ctx, Stream(o.Org), eventstore.AnyVersion, "daemon", Revoked{ID: o.ID, Reason: "rotated"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", Capability{}, err
	}
	return token, c, nil
}

// RevokeRun revokes every token of run issued under a fence at or below fence, for reason: its run
// finished or was stopped.
func (i *Issuer) RevokeRun(ctx context.Context, run string, fence int64, reason string) error {
	old, err := i.live(ctx, run, fence)
	if err != nil || len(old) == 0 {
		return err
	}
	return i.Store.Update(ctx, func(tx *eventstore.Tx) error {
		for _, o := range old {
			if _, err := tx.Append(ctx, Stream(o.Org), eventstore.AnyVersion, "daemon", Revoked{ID: o.ID, Reason: reason}); err != nil {
				return err
			}
		}
		return nil
	})
}

// live returns run's tokens not yet revoked, issued under a fence at or below fence.
func (i *Issuer) live(ctx context.Context, run string, fence int64) ([]Capability, error) {
	rows, err := i.Store.QueryContext(ctx, `SELECT id, org FROM capabilities WHERE run = ? AND fence <= ? AND revoked = 0 ORDER BY id`, run, fence)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []Capability
	for rows.Next() {
		var c Capability
		if err := rows.Scan(&c.ID, &c.Org); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Verify checks token for a use of route in org's gateway, now. It returns the capability it is,
// or why it is refused.
func (i *Issuer) Verify(ctx context.Context, token, org, route string) (Capability, error) {
	if !strings.HasPrefix(token, Prefix) || len(token) > 128 {
		return Capability{}, ErrInvalid
	}
	d := digest(token)
	full := hex.EncodeToString(d[:])
	c, stored, err := i.get(ctx, full[:idHexLen])
	if errors.Is(err, ErrInvalid) {
		return Capability{}, ErrInvalid
	}
	if err != nil {
		return Capability{}, err
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(full)) != 1 {
		return Capability{}, ErrInvalid
	}
	now := i.now()
	switch {
	case c.Revoked:
		return c, ErrRevoked
	case c.Org != org || !slices.Contains(c.Routes, route):
		return c, ErrScope
	case !now.Before(c.Expires):
		return c, ErrExpired
	}
	live, err := i.leased(ctx, c, now, true)
	if err != nil {
		return c, err
	}
	if !live {
		return c, ErrRunEnded
	}
	return c, nil
}

// leased reports whether c's run is still leased under c's fence at now, its lease not run out.
// When earned, the expiry is the one the runner itself earned: a restarted daemon moves the lease
// to keep the run for its runner, so its token is not revoked, but that lends the token nothing
// until the runner renews the lease (LUCY-OPS-005).
func (i *Issuer) leased(ctx context.Context, c Capability, now time.Time, earned bool) (bool, error) {
	ri, err := i.Leases.Run(ctx, c.Run)
	if errors.Is(err, runner.ErrNoRun) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	until := ri.Expires
	if earned {
		until = ri.Earned
	}
	return ri.Org == c.Org && ri.State == runner.StateLeased && ri.Fence == c.Fence && now.Before(until), nil
}

// RevokeEnded revokes every token, not yet revoked or past its expiry, whose run is no longer
// leased under it: its lease ran out or was replaced, or its run ended. It returns how many it
// revoked. The daemon calls it on its lease-expiry timer.
func (i *Issuer) RevokeEnded(ctx context.Context) (int, error) {
	now := i.now()
	rows, err := i.Store.QueryContext(ctx, `SELECT id, org, run, fence FROM capabilities WHERE revoked = 0 AND expires_ns > ? ORDER BY id`,
		now.UnixNano())
	if err != nil {
		return 0, err
	}
	var open []Capability
	for rows.Next() {
		var c Capability
		if err := rows.Scan(&c.ID, &c.Org, &c.Run, &c.Fence); err != nil {
			_ = rows.Close() // the scan error is what matters
			return 0, err
		}
		open = append(open, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	n := 0
	for _, c := range open {
		live, err := i.leased(ctx, c, now, false)
		if err != nil {
			return n, err
		}
		if live {
			continue
		}
		if _, err := i.Store.Append(ctx, Stream(c.Org), eventstore.AnyVersion, "daemon", Revoked{ID: c.ID, Reason: "run ended"}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ErrUnknown is a Revoke of an id no token has.
var ErrUnknown = errors.New("capability: no such capability")

// Revoke revokes the capability with id, for reason. Revoking one already revoked is a no-op.
func (i *Issuer) Revoke(ctx context.Context, id, by, reason string) (Capability, error) {
	if len(id) != idHexLen || strings.Trim(id, "0123456789abcdef") != "" {
		return Capability{}, fmt.Errorf("%w: %q is not a capability id", ErrUnknown, id)
	}
	c, _, err := i.get(ctx, id)
	if errors.Is(err, ErrInvalid) {
		return Capability{}, ErrUnknown
	}
	if err != nil {
		return Capability{}, err
	}
	if c.Revoked {
		return c, nil
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	if _, err := i.Store.Append(ctx, Stream(c.Org), eventstore.AnyVersion, by, Revoked{ID: id, Reason: reason}); err != nil {
		return Capability{}, err
	}
	c.Revoked, c.RevokedReason = true, reason
	return c, nil
}

// List returns every capability not yet past its expiry, newest first; with all, every one.
func (i *Issuer) List(ctx context.Context, all bool) ([]Capability, error) {
	q := `SELECT id, digest, org, run, fence, routes, issued_ns, start_ns, expires_ns, revoked, revoked_reason FROM capabilities`
	args := []any{}
	if !all {
		q += ` WHERE expires_ns > ?`
		args = append(args, i.now().UnixNano())
	}
	q += ` ORDER BY issued_ns DESC, id`
	rows, err := i.Store.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only
	out := []Capability{}
	for rows.Next() {
		c, _, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (i *Issuer) get(ctx context.Context, id string) (Capability, string, error) {
	c, d, err := scan(i.Store.QueryRowContext(ctx, `SELECT id, digest, org, run, fence, routes, issued_ns, start_ns, expires_ns, revoked, revoked_reason
		FROM capabilities WHERE id = ?`, id))
	if errors.Is(err, errNoRow) {
		return Capability{}, "", ErrInvalid
	}
	return c, d, err
}

type rowScanner interface{ Scan(dest ...any) error }

var errNoRow = errors.New("capability: no row")

func scan(r rowScanner) (Capability, string, error) {
	var c Capability
	var d, routes string
	var issued, start, expires int64
	var revoked int
	if err := r.Scan(&c.ID, &d, &c.Org, &c.Run, &c.Fence, &routes, &issued, &start, &expires, &revoked, &c.RevokedReason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Capability{}, "", errNoRow
		}
		return Capability{}, "", err
	}
	if err := json.Unmarshal([]byte(routes), &c.Routes); err != nil {
		return Capability{}, "", err
	}
	c.Issued, c.Start, c.Expires, c.Revoked = time.Unix(0, issued).UTC(), time.Unix(0, start).UTC(), time.Unix(0, expires).UTC(), revoked != 0
	return c, d, nil
}
