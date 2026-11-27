// Package client is the Go client of lucy's API. The CLI uses it; so will the TUI and the MCP
// server. It speaks the wire format of package api over the daemon's unix socket.
//
// Decoding is strict where the server is strict (the response envelope) and tolerant of new result
// fields, so a newer daemon that adds optional fields within API v1 does not break an older client.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
)

// ErrNotRunning is returned when no daemon listens on the socket.
var ErrNotRunning = errors.New("lucy daemon is not running")

// MaxResponseBytes bounds a response the client will read.
const MaxResponseBytes = 64 << 20

// DefaultTimeout bounds one call, connection included.
const DefaultTimeout = 15 * time.Second

// Client calls the API over one unix socket. It is safe for concurrent use.
type Client struct {
	socket string
	hc     *http.Client
	nextID atomic.Int64
	name   string
	ver    string
}

// New returns a client for the daemon at socketPath. name and version identify the calling surface
// in hello, for example "lucy-cli" and lucy's version.
func New(socketPath, name, version string) *Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: DefaultTimeout,
		DisableCompression:    true,
	}
	return &Client{socket: socketPath, hc: &http.Client{Transport: tr, Timeout: DefaultTimeout}, name: name, ver: version}
}

// Socket is the socket path the client dials.
func (c *Client) Socket() string { return c.socket }

// Close releases idle connections.
func (c *Client) Close() { c.hc.CloseIdleConnections() }

// Call sends one request and decodes its result into result (a pointer, or nil to discard it). An
// API error is returned as *api.Error; a daemon that is not listening as ErrNotRunning.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	req := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://lucy"+api.Path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(hreq)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return fmt.Errorf("%w (no daemon at %s)", ErrNotRunning, c.socket)
		}
		return fmt.Errorf("lucy api: %s: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("lucy api: %s: read response: %w", method, err)
	}
	if len(data) > MaxResponseBytes {
		return fmt.Errorf("lucy api: %s: response larger than %d bytes", method, MaxResponseBytes)
	}
	var r api.Response
	if err := json.Unmarshal(data, &r); err != nil || r.JSONRPC != "2.0" {
		return fmt.Errorf("lucy api: %s: malformed response (HTTP %d)", method, resp.StatusCode)
	}
	if r.Error != nil {
		return r.Error
	}
	if string(r.ID) != strconv.FormatInt(id, 10) {
		return fmt.Errorf("lucy api: %s: response id %s does not match request id %d", method, r.ID, id)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(r.Result, result); err != nil {
		return fmt.Errorf("lucy api: %s: decode result: %w", method, err)
	}
	return nil
}

// Hello negotiates the API version.
func (c *Client) Hello(ctx context.Context) (api.Welcome, error) {
	var w api.Welcome
	err := c.Call(ctx, api.MethodHello, api.HelloParams{Client: c.name, ClientVersion: c.ver, APIVersions: api.SupportedVersions}, &w)
	return w, err
}

// Health checks the daemon is up and its event store answers.
func (c *Client) Health(ctx context.Context) (api.Health, error) {
	var h api.Health
	err := c.Call(ctx, api.MethodHealth, nil, &h)
	return h, err
}

// Status returns the daemon's status.
func (c *Client) Status(ctx context.Context) (api.Status, error) {
	var s api.Status
	err := c.Call(ctx, api.MethodStatus, nil, &s)
	return s, err
}

// Events returns a page of the event log.
func (c *Client) Events(ctx context.Context, p api.EventsParams) (api.EventsPage, error) {
	var page api.EventsPage
	err := c.Call(ctx, api.MethodEvents, p, &page)
	return page, err
}

// Shutdown asks the daemon to stop.
func (c *Client) Shutdown(ctx context.Context) (api.ShutdownResult, error) {
	var r api.ShutdownResult
	err := c.Call(ctx, api.MethodShutdown, nil, &r)
	return r, err
}

// Discover returns the daemon's OpenRPC document.
func (c *Client) Discover(ctx context.Context) (json.RawMessage, error) {
	var doc json.RawMessage
	err := c.Call(ctx, api.MethodDiscover, nil, &doc)
	return doc, err
}

// Evaluate asks the policy engine to decide an action; the daemon records the decision first.
func (c *Client) Evaluate(ctx context.Context, p api.EvaluateParams) (api.PolicyDecision, error) {
	var d api.PolicyDecision
	err := c.Call(ctx, api.MethodEvaluate, p, &d)
	return d, err
}

// Decisions returns a page of recorded policy decisions.
func (c *Client) Decisions(ctx context.Context, p api.DecisionsParams) (api.DecisionsPage, error) {
	var page api.DecisionsPage
	err := c.Call(ctx, api.MethodDecisions, p, &page)
	return page, err
}

// Resources calls resources.list.
func (c *Client) Resources(ctx context.Context, p api.ResourcesParams) (api.ResourcesPage, error) {
	var r api.ResourcesPage
	err := c.Call(ctx, api.MethodResources, p, &r)
	return r, err
}

// VerifyAudit calls audit.verify.
func (c *Client) VerifyAudit(ctx context.Context) (api.AuditReport, error) {
	var r api.AuditReport
	err := c.Call(ctx, api.MethodAudit, nil, &r)
	return r, err
}

// RunnerQueued calls runner.queued.
func (c *Client) RunnerQueued(ctx context.Context, p api.QueuedParams) (api.QueuedResult, error) {
	var r api.QueuedResult
	err := c.Call(ctx, api.MethodQueued, p, &r)
	return r, err
}

// RunnerLease calls runner.lease.
func (c *Client) RunnerLease(ctx context.Context, p api.LeaseParams) (api.LeaseResult, error) {
	var r api.LeaseResult
	err := c.Call(ctx, api.MethodLease, p, &r)
	return r, err
}

// RunnerHeartbeat calls runner.heartbeat.
func (c *Client) RunnerHeartbeat(ctx context.Context, p api.HeartbeatParams) (api.HeartbeatResult, error) {
	var r api.HeartbeatResult
	err := c.Call(ctx, api.MethodHeartbeat, p, &r)
	return r, err
}

// Questions calls questions.list.
func (c *Client) Questions(ctx context.Context) (api.QuestionsList, error) {
	var r api.QuestionsList
	err := c.Call(ctx, api.MethodQuestions, api.NoParams{}, &r)
	return r, err
}

// Answer calls questions.answer.
func (c *Client) Answer(ctx context.Context, p api.AnswerParams) (api.AnswerResult, error) {
	var r api.AnswerResult
	err := c.Call(ctx, api.MethodAnswer, p, &r)
	return r, err
}

// SandboxHeld calls sandbox.held.
func (c *Client) SandboxHeld(ctx context.Context) (api.HeldList, error) {
	var r api.HeldList
	err := c.Call(ctx, api.MethodHeld, api.NoParams{}, &r)
	return r, err
}

// SandboxConfirm calls sandbox.confirm.
func (c *Client) SandboxConfirm(ctx context.Context, p api.ConfirmParams) (api.ConfirmResult, error) {
	var r api.ConfirmResult
	err := c.Call(ctx, api.MethodConfirm, p, &r)
	return r, err
}

// GatewayRoutes calls gateway.routes.
func (c *Client) GatewayRoutes(ctx context.Context, org string) (api.GatewayRoutes, error) {
	var r api.GatewayRoutes
	err := c.Call(ctx, api.MethodGwRoutes, api.GatewayRoutesParams{Org: org}, &r)
	return r, err
}

// GatewayUse calls gateway.use.
func (c *Client) GatewayUse(ctx context.Context, p api.GatewayUseParams) (api.GatewayDecision, error) {
	var r api.GatewayDecision
	err := c.Call(ctx, api.MethodGwUse, p, &r)
	return r, err
}

// GatewayDeny calls gateway.deny.
func (c *Client) GatewayDeny(ctx context.Context, p api.GatewayDenyParams) error {
	var r api.GatewayRecorded
	return c.Call(ctx, api.MethodGwDeny, p, &r)
}

// GatewayComplete calls gateway.complete.
func (c *Client) GatewayComplete(ctx context.Context, p api.GatewayCompleteParams) error {
	var r api.GatewayRecorded
	return c.Call(ctx, api.MethodGwDone, p, &r)
}

// GatewayVerify calls gateway.verify.
func (c *Client) GatewayVerify(ctx context.Context, p api.GatewayUseParams) (api.GatewayVerified, error) {
	var r api.GatewayVerified
	err := c.Call(ctx, api.MethodGwVerify, p, &r)
	return r, err
}

// Capabilities calls capabilities.list.
func (c *Client) Capabilities(ctx context.Context, all bool) (api.CapabilitiesList, error) {
	var r api.CapabilitiesList
	err := c.Call(ctx, api.MethodCaps, api.CapabilitiesParams{All: all}, &r)
	return r, err
}

// CapabilityRevoke calls capability.revoke.
func (c *Client) CapabilityRevoke(ctx context.Context, p api.RevokeParams) (api.CapabilityInfo, error) {
	var r api.CapabilityInfo
	err := c.Call(ctx, api.MethodRevoke, p, &r)
	return r, err
}

// VaultRecord calls vault.record.
func (c *Client) VaultRecord(ctx context.Context, p api.VaultRecordParams) (api.VaultRecorded, error) {
	var r api.VaultRecorded
	err := c.Call(ctx, api.MethodVaultRec, p, &r)
	return r, err
}

// Worktrees calls worktree.list.
func (c *Client) Worktrees(ctx context.Context, org string) (api.WorktreeList, error) {
	var r api.WorktreeList
	err := c.Call(ctx, api.MethodWtList, api.WorktreeListParams{Org: org}, &r)
	return r, err
}

// WorktreeDiscard calls worktree.discard.
func (c *Client) WorktreeDiscard(ctx context.Context, p api.WorktreeDiscardParams) (api.WorktreeSlot, error) {
	var r api.WorktreeSlot
	err := c.Call(ctx, api.MethodWtDiscard, p, &r)
	return r, err
}

// RunnerHostOrg calls runner.host_org.
func (c *Client) RunnerHostOrg(ctx context.Context, p api.HostOrgParams) (api.HostOrgResult, error) {
	var r api.HostOrgResult
	err := c.Call(ctx, api.MethodHostOrg, p, &r)
	return r, err
}

// RunnerPermit calls runner.permit.
func (c *Client) RunnerPermit(ctx context.Context, p api.PermitParams) (api.PermitResult, error) {
	var r api.PermitResult
	err := c.Call(ctx, api.MethodPermit, p, &r)
	return r, err
}

// RunnerFinish calls runner.finish.
func (c *Client) RunnerFinish(ctx context.Context, p api.FinishParams) (api.FinishResult, error) {
	var r api.FinishResult
	err := c.Call(ctx, api.MethodFinish, p, &r)
	return r, err
}

// Runs calls runs.list.
func (c *Client) Runs(ctx context.Context, p api.RunsParams) (api.RunsPage, error) {
	var r api.RunsPage
	err := c.Call(ctx, api.MethodRuns, p, &r)
	return r, err
}
