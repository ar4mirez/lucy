// Package berth is the first Sandbox provider: berth, the separate product that runs one
// container per org (D3a). lucy talks to berth only through berth's documented HTTP API, version
// v1 (berth's docs/api.md and its OpenAPI document), never by importing its code or driving its
// command line. The documents lucy reads are pinned by their schema names; a document of another
// schema is refused rather than guessed at, so a berth that changed its API is held until this
// client's contract tests pass against it.
//
// Every operation that stops the work in an org's container (up, restart, down) is one berth
// refuses without `confirm` set to the org's name. This client sends that confirmation only when
// it is asked to run the operation, and only the guarded sandbox.Sandbox asks, after lucy's policy
// engine cleared it (LUCY-SBX-003).
package berth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/sandbox"
)

// Type is the provider's type, as an AdapterBinding names it.
const Type = "berth"

// The documents lucy reads, by schema name ("schema": "berth.<kind>/v<N>"). berth bumps a
// document's version when it removes, renames or changes the meaning of a field.
const (
	SchemaAPI   = "berth.api/v1"   // GET /v1: what is serving and what this caller may do
	SchemaInfo  = "berth.info/v1"  // GET /v1/orgs/{org}: an org's connection sheet
	SchemaError = "berth.error/v1" // every failure
)

// maxBody bounds what lucy reads of any berth response.
const maxBody = 1 << 20

// Client is a client of berth's API. It implements sandbox.Provider.
type Client struct {
	base  string // "http://berth" on the socket, or an https:// address
	http  *http.Client
	token string // a bearer token, for berth's TCP listener only; never logged
}

var _ sandbox.Provider = (*Client)(nil)

// Options configure a Client.
type Options struct {
	// Socket is the absolute path of berth's Unix socket, which only its owner can open: berth
	// needs no token there. Exactly one of Socket and BaseURL is set.
	Socket string
	// BaseURL is berth's address over the network (https://host:port), with Token.
	BaseURL string
	Token   string
	// HTTP is the client to use for BaseURL (its TLS configuration pins berth's certificate);
	// http.DefaultClient when nil. Ignored for Socket.
	HTTP *http.Client
	// Timeout bounds each call; 10 minutes when zero, since up and restart may pull an image.
	Timeout time.Duration
}

// New returns a client for the berth API that opts name.
func New(opts Options) (*Client, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	switch {
	case opts.Socket != "" && opts.BaseURL != "":
		return nil, errors.New("berth: set a socket or a base URL, not both")
	case opts.Socket != "":
		if !filepath.IsAbs(opts.Socket) || filepath.Clean(opts.Socket) != opts.Socket {
			return nil, fmt.Errorf("berth: the socket must be a clean absolute path, not %q", opts.Socket)
		}
		if opts.Token != "" {
			return nil, errors.New("berth: the socket takes no token")
		}
		socket := opts.Socket
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}}
		return &Client{base: "http://berth", http: &http.Client{Transport: tr, Timeout: timeout}}, nil
	case opts.BaseURL != "":
		u, err := url.Parse(opts.BaseURL)
		if err != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil {
			return nil, fmt.Errorf("berth: %q is not a base URL like https://127.0.0.1:8443", opts.BaseURL)
		}
		if u.Scheme != "https" {
			return nil, errors.New("berth: berth serves TCP over TLS only; use an https:// base URL")
		}
		if opts.Token == "" {
			return nil, errors.New("berth: every request over TCP needs a token")
		}
		hc := opts.HTTP
		if hc == nil {
			hc = &http.Client{}
		}
		c := *hc
		if c.Timeout == 0 {
			c.Timeout = timeout
		}
		return &Client{base: "https://" + u.Host, http: &c, token: opts.Token}, nil
	}
	return nil, errors.New("berth: set the socket or a base URL")
}

// Type implements sandbox.Provider.
func (c *Client) Type() string { return Type }

// Server is what berth's GET /v1 says about the server and this caller.
type Server struct {
	Version  string `json:"version"`
	ReadOnly bool   `json:"read_only"`
	Writes   bool   `json:"writes"`
	Restarts bool   `json:"restarts"` // this caller may start, restart and stop orgs
	Admin    bool   `json:"admin"`
}

// Server reads what is serving and what this caller may do.
func (c *Client) Server(ctx context.Context) (Server, error) {
	var doc struct {
		Schema string `json:"schema"`
		Server
	}
	if err := c.call(ctx, http.MethodGet, "/v1", nil, &doc); err != nil {
		return Server{}, err
	}
	if doc.Schema != SchemaAPI {
		return Server{}, fmt.Errorf("%w: GET /v1 answered %q, not %s", ErrContract, doc.Schema, SchemaAPI)
	}
	return doc.Server, nil
}

// Status implements sandbox.Provider with berth's connection sheet, GET /v1/orgs/{org}.
func (c *Client) Status(ctx context.Context, org string) (sandbox.Status, error) {
	var doc struct {
		Schema    string `json:"schema"`
		Org       string `json:"org"`
		Container string `json:"container"`
		State     string `json:"state"`
	}
	if err := c.call(ctx, http.MethodGet, orgPath(org, ""), nil, &doc); err != nil {
		return sandbox.Status{}, err
	}
	if doc.Schema != SchemaInfo {
		return sandbox.Status{}, fmt.Errorf("%w: an org's sheet is %q, not %s", ErrContract, doc.Schema, SchemaInfo)
	}
	if doc.Org != org {
		return sandbox.Status{}, fmt.Errorf("%w: asked for org %q, answered for %q", ErrContract, org, doc.Org)
	}
	var running bool
	switch doc.State {
	case "running":
		running = true
	case "stopped":
	default:
		return sandbox.Status{}, fmt.Errorf("%w: org %s is in unknown state %q", ErrContract, org, doc.State)
	}
	return sandbox.Status{Org: org, Running: running, Container: doc.Container}, nil
}

// Do implements sandbox.Provider: POST /v1/orgs/{org}/{up,restart,down} with the org's name as
// berth's confirmation. It returns when berth has ended the operation.
func (c *Client) Do(ctx context.Context, org string, op sandbox.Op) error {
	if !op.Valid() {
		return fmt.Errorf("berth: unknown operation %q", op)
	}
	body := struct {
		Confirm string `json:"confirm"`
	}{org}
	var doc struct {
		Org string `json:"org"`
	}
	if err := c.call(ctx, http.MethodPost, orgPath(org, string(op)), body, &doc); err != nil {
		return err
	}
	if doc.Org != org {
		return fmt.Errorf("%w: %s of org %q answered for %q", ErrContract, op, org, doc.Org)
	}
	return nil
}

// ErrContract is a berth answer that is not the documented one.
var ErrContract = errors.New("berth: the answer does not match berth's documented API")

// Error is berth's berth.error/v1 document.
type Error struct {
	Status  int    `json:"-"`
	Kind    string `json:"kind"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("berth: %s (%d): %s", e.Kind, e.Status, e.Message)
	if e.Hint != "" {
		msg += " (" + e.Hint + ")"
	}
	return msg
}

// Unwrap maps berth's documented error kinds onto the sandbox's errors.
func (e *Error) Unwrap() error {
	switch {
	case e.Status == http.StatusNotFound:
		return sandbox.ErrNotFound
	case e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden:
		return sandbox.ErrRefused
	case e.Status == http.StatusConflict:
		return sandbox.ErrState
	}
	return nil
}

// orgPath is /v1/orgs/{org}[/{op}], with the org checked and escaped.
func orgPath(org, op string) string {
	p := "/v1/orgs/" + url.PathEscape(org)
	if op != "" {
		p += "/" + op
	}
	return p
}

// call sends one request and decodes berth's answer into out, or its error document.
func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	if strings.HasPrefix(path, "/v1/orgs/") {
		org, _, _ := strings.Cut(strings.TrimPrefix(path, "/v1/orgs/"), "/")
		if u, err := url.PathUnescape(org); err != nil || !sandbox.ValidOrg(u) {
			return fmt.Errorf("berth: %q is not an org name", org)
		}
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %w", sandbox.ErrUnavailable, method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read fully below
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("%w: reading %s %s: %w", sandbox.ErrUnavailable, method, path, err)
	}
	if len(data) > maxBody {
		return fmt.Errorf("%w: %s %s answered more than %d bytes", ErrContract, method, path, maxBody)
	}
	if resp.StatusCode != http.StatusOK {
		e := &Error{Status: resp.StatusCode}
		var doc struct {
			Schema string `json:"schema"`
			*Error
		}
		doc.Error = e
		if json.Unmarshal(data, &doc) != nil || doc.Schema != SchemaError {
			return fmt.Errorf("%w: %s %s answered %d without a %s document", ErrContract, method, path, resp.StatusCode, SchemaError)
		}
		return e
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: %s %s: %w", ErrContract, method, path, err)
	}
	return nil
}

// ContractMajor is the major version of the berth contract this client implements: berth's API v1
// and the documents named above. A Sandbox binding of type berth names a 1.x version.
const ContractMajor = 1

// Open is the berth provider's sandbox.Factory. Its binding's config takes `socket`, the absolute
// path of berth's Unix socket, berth's default way in, which needs no token. A TCP listener needs
// a token, and tokens come from the credential gateway (m1 slice 4), never from a binding.
func Open(b sandbox.Binding) (sandbox.Provider, error) {
	if b.Type != Type {
		return nil, fmt.Errorf("berth: binding %s/%s is of type %q", b.Namespace, b.Name, b.Type)
	}
	if err := sandbox.CheckVersion(b, ContractMajor); err != nil {
		return nil, err
	}
	if err := sandbox.CheckConfig(b, "socket"); err != nil {
		return nil, err
	}
	socket, _ := b.Config["socket"].(string)
	if socket == "" {
		return nil, fmt.Errorf("berth: binding %s/%s names no socket", b.Namespace, b.Name)
	}
	return New(Options{Socket: socket})
}
