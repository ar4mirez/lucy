// Package gateway is lucy's per-org credential gateway (LUCY-CRED-001, LUCY-CRED-004, D3c): the
// one place a credential is used, outside the agents' container, so a worker never holds one.
//
// A worker calls the gateway instead of the service: http://<gateway>/<route>/<path>. The gateway
// checks the request against the route's allowlist (method and path prefix), resolves the
// route's upstream and refuses it if any address is private, loopback, link-local (the metadata
// address included), shared, reserved or multicast, then decides the credential's use through the
// policy engine and records the decision before anything is sent. Only then does it forward the
// request over TLS to that checked address, with the worker's own credential headers removed and
// the route's credential added. It never follows a redirect, so a credential is never sent to a
// host the route does not name; a redirect goes back to the worker as it came. Every copy of the
// credential the upstream echoes back is scrubbed from the answer. Any failure (an unknown route, a
// missing credential, an address it cannot check, a record it cannot write) is a refusal: the
// gateway fails closed, and every request, forwarded or denied, is in the audit chain.
package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
)

// Route is one service the gateway reaches for an org's workers.
type Route struct {
	Name       string // the first path element a worker calls it by: [a-z0-9-]{1,32}
	Upstream   string // https://host[:port][/base]; never another scheme
	Credential string // the credential's name in the org's store
	Scope      string // what the credential grants, as the policy engine records it
	Header     string // the header the credential goes in; Authorization when empty
	Prefix     string // put before the credential in the header, for example "Bearer "
	Allow      []Rule // what a worker may ask for; nothing else is forwarded
}

// Rule allows some methods on the paths under a prefix. A prefix ends at a path segment: "/repos/acme"
// allows "/repos/acme" and "/repos/acme/web", never "/repos/acme-other".
type Rule struct {
	Methods []string // GET, POST, ...; at least one
	Path    string   // a clean absolute path prefix, "/" for everything
}

// Credentials gives the gateway its org's credentials: lucy's per-org vault (m1 slice 4b).
type Credentials interface {
	Credential(ctx context.Context, org, name string) ([]byte, error)
}

// Config configures a Gateway. Org, Recorder, Routes and Credentials are required.
type Config struct {
	Org         string
	Recorder    Recorder // the daemon's log, in process (StoreRecorder) or over the API (Remote)
	Routes      []Route
	Credentials Credentials
	// Resolver looks upstream hosts up; net.DefaultResolver when nil.
	Resolver Resolver
	// Addresses is the address policy; PublicOnly when nil. Only tests set another.
	Addresses AddressPolicy
	// TLS is the TLS configuration for upstreams (its RootCAs); the system's when nil. Only tests
	// set it.
	TLS *tls.Config
	// Timeout bounds one upstream call, headers and body; 5 minutes when zero.
	Timeout time.Duration
}

// Gateway is one org's credential gateway. It is an http.Handler, safe for concurrent use.
type Gateway struct {
	cfg       Config
	routes    map[string]route
	transport *http.Transport
}

type route struct {
	Route
	upstream *url.URL
	header   string
}

// New checks cfg and returns its gateway.
func New(cfg Config) (*Gateway, error) {
	if cfg.Org == "" || cfg.Recorder == nil || cfg.Credentials == nil {
		return nil, errors.New("gateway: Config needs an org, a recorder and the org's credentials")
	}
	if e := orgLabel(cfg.Org); e != "" {
		return nil, fmt.Errorf("gateway: org %q %s", cfg.Org, e)
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	if cfg.Addresses == nil {
		cfg.Addresses = PublicOnly
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	g := &Gateway{cfg: cfg, routes: map[string]route{}}
	for _, r := range cfg.Routes {
		rt, err := checkRoute(r)
		if err != nil {
			return nil, err
		}
		if _, dup := g.routes[r.Name]; dup {
			return nil, fmt.Errorf("gateway: route %q is defined twice", r.Name)
		}
		g.routes[r.Name] = rt
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.TLS != nil {
		tlsCfg = cfg.TLS.Clone()
		if tlsCfg.MinVersion < tls.VersionTLS12 {
			tlsCfg.MinVersion = tls.VersionTLS12
		}
	}
	g.transport = &http.Transport{
		Proxy:                 nil, // never an environment proxy: the gateway is the only way out
		DialContext:           dialPinned(cfg.Addresses),
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true, // the scrubber must see the bytes the upstream sent
	}
	return g, nil
}

// Close closes the gateway's idle upstream connections.
func (g *Gateway) Close() { g.transport.CloseIdleConnections() }

func checkRoute(r Route) (route, error) {
	if e := nameLabel(r.Name); e != "" {
		return route{}, fmt.Errorf("gateway: route name %q %s", r.Name, e)
	}
	u, err := url.Parse(r.Upstream)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return route{}, fmt.Errorf("gateway: route %s: upstream %q is not an https://host[/base] URL", r.Name, r.Upstream)
	}
	if u.Path != "" && (u.Path != path.Clean(u.Path) || !strings.HasPrefix(u.Path, "/")) {
		return route{}, fmt.Errorf("gateway: route %s: upstream base path %q is not clean", r.Name, u.Path)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if r.Credential == "" {
		return route{}, fmt.Errorf("gateway: route %s names no credential", r.Name)
	}
	header := r.Header
	if header == "" {
		header = "Authorization"
	}
	if http.CanonicalHeaderKey(header) == "Host" || strings.ContainsAny(header, " :\r\n") {
		return route{}, fmt.Errorf("gateway: route %s: %q is not a credential header", r.Name, header)
	}
	if len(r.Allow) == 0 {
		return route{}, fmt.Errorf("gateway: route %s allows nothing", r.Name)
	}
	for _, rule := range r.Allow {
		if len(rule.Methods) == 0 || rule.Path == "" || rule.Path != path.Clean(rule.Path) || !strings.HasPrefix(rule.Path, "/") {
			return route{}, fmt.Errorf("gateway: route %s: rule %+v needs methods and a clean absolute path", r.Name, rule)
		}
	}
	return route{Route: r, upstream: u, header: http.CanonicalHeaderKey(header)}, nil
}

// allowed reports whether the route lets method reach p, a checked path.
func (r route) allowed(method, p string) bool {
	for _, rule := range r.Allow {
		if !slices.Contains(rule.Methods, method) {
			continue
		}
		if rule.Path == "/" || p == rule.Path || strings.HasPrefix(p, rule.Path+"/") {
			return true
		}
	}
	return false
}

// workerPath checks the path a worker asked for: the route's name, then a clean absolute path.
// Encoded slashes and backslashes, dot segments, empty segments and control characters are refused
// rather than normalised, so what the allowlist matched is exactly what the upstream gets.
func workerPath(r *http.Request) (name, rest string, err error) {
	raw := r.URL.EscapedPath()
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%2e") ||
		strings.Contains(raw, "\\") || strings.Contains(raw, "//") {
		return "", "", errors.New("the path has an encoded separator or an empty segment")
	}
	p := r.URL.Path
	for _, c := range p {
		if c < 0x20 || c == 0x7f {
			return "", "", errors.New("the path has a control character")
		}
	}
	if !strings.HasPrefix(p, "/") {
		return "", "", errors.New("the path is not absolute")
	}
	name, rest, _ = strings.Cut(p[1:], "/")
	rest = "/" + rest
	if path.Clean(rest) != rest && rest != "/" {
		return "", "", errors.New("the path is not clean")
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == "." || seg == ".." {
			return "", "", errors.New("the path has a dot segment")
		}
	}
	return name, rest, nil
}

// hopHeaders are never forwarded either way (RFC 9110 section 7.6.1), nor are the worker's own
// credentials, forwarding hints or anything naming the gateway.
var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade"}

var workerCredentialHeaders = []string{"Authorization", "Cookie", "X-Api-Key", "Api-Key", "Private-Token",
	"X-Auth-Token", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"}

// workerShapeHeaders would let a worker ask for an answer the scrubber cannot read whole: encoded,
// or cut into byte ranges that each hold part of an echoed credential.
var workerShapeHeaders = []string{"Accept-Encoding", "Range", "If-Range"}

// ServeHTTP implements http.Handler.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw := r.URL.EscapedPath()
	deny := func(status int, routeName, reason string) {
		// Recorded first; refused whether or not the record could be written.
		_ = g.cfg.Recorder.Deny(ctx, g.cfg.Org, Denied{Route: routeName, Method: r.Method, Path: raw, Reason: reason})
		http.Error(w, "lucy gateway: "+reason, status)
	}
	name, rest, err := workerPath(r)
	if err != nil {
		deny(http.StatusBadRequest, "", err.Error())
		return
	}
	rt, ok := g.routes[name]
	if !ok {
		deny(http.StatusNotFound, "", "no such route")
		return
	}
	if !rt.allowed(r.Method, rest) {
		deny(http.StatusForbidden, name, "the route does not allow "+r.Method+" on this path")
		return
	}
	host := rt.upstream.Hostname()
	port := rt.upstream.Port()
	if port == "" {
		port = "443"
	}
	addrs, err := resolve(ctx, g.cfg.Resolver, g.cfg.Addresses, host)
	if err != nil {
		deny(http.StatusForbidden, name, "the upstream's address is refused")
		return
	}
	secret, err := g.cfg.Credentials.Credential(ctx, g.cfg.Org, rt.Credential)
	if err != nil || len(secret) == 0 {
		deny(http.StatusBadGateway, name, "the route's credential is unavailable")
		return
	}
	defer clear(secret)

	// The credential's use is decided and recorded before anything is sent (LUCY-POL-002).
	d, err := g.cfg.Recorder.Decide(ctx, g.cfg.Org, Use{Route: name, Method: r.Method, Path: raw, Upstream: rt.upstream.Host})
	if err != nil {
		http.Error(w, "lucy gateway: the request could not be decided and recorded", http.StatusServiceUnavailable)
		return
	}
	if !d.Cleared {
		http.Error(w, "lucy gateway: the credential's use was not cleared", http.StatusForbidden)
		return
	}
	seq := d.Seq

	out := g.outgoing(ctx, r, rt, rest, addrs, port, secret)
	resp, err := g.transport.RoundTrip(out) // RoundTrip, not a client: a redirect is never followed
	if err != nil {
		g.complete(ctx, Completed{DecisionSeq: seq, Error: "the upstream call failed"})
		http.Error(w, "lucy gateway: the upstream call failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close() //nolint:errcheck // read fully below
	if encoded(resp.Header) {
		g.complete(ctx, Completed{DecisionSeq: seq, Status: resp.StatusCode, Error: "the upstream answered with an encoded body"})
		http.Error(w, "lucy gateway: the upstream answered with an encoded body", http.StatusBadGateway)
		return
	}
	secrets := [][]byte{secret}
	scrubbed := 0
	for k, vs := range resp.Header {
		if slices.Contains(hopHeaders, http.CanonicalHeaderKey(k)) {
			continue
		}
		for _, v := range vs {
			v, n := scrubString(v, secrets)
			scrubbed += n
			w.Header().Add(k, v)
		}
	}
	w.Header().Del("Content-Length") // scrubbing may change the body's length
	w.WriteHeader(resp.StatusCode)
	body := newScrubber(resp.Body, secrets)
	_, copyErr := io.Copy(w, body)
	c := Completed{DecisionSeq: seq, Status: resp.StatusCode, Scrubbed: scrubbed + body.found}
	if copyErr != nil {
		c.Error = "the answer was cut off"
	}
	g.complete(ctx, c)
}

// outgoing is the request the upstream gets: the worker's method, path, query and body, the
// worker's headers less hop-by-hop, credential, forwarding and answer-shaping ones, an
// identity-only Accept-Encoding, and the route's credential.
func (g *Gateway) outgoing(ctx context.Context, r *http.Request, rt route, rest string, addrs []netip.Addr, port string, secret []byte) *http.Request {
	u := *rt.upstream
	u.Path = rt.upstream.Path + rest
	u.RawPath = ""
	u.RawQuery = r.URL.RawQuery
	ctx = context.WithValue(ctx, pinnedKey{}, pinned{addrs: addrs, port: port})
	ctx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	context.AfterFunc(ctx, cancel)
	out := r.Clone(ctx)
	out.URL, out.Host, out.RequestURI = &u, u.Host, ""
	for _, h := range slices.Concat(hopHeaders, workerCredentialHeaders, workerShapeHeaders) {
		out.Header.Del(h)
	}
	out.Header.Set("Accept-Encoding", "identity")
	out.Header.Del(rt.header)
	out.Header.Set(rt.header, rt.Prefix+string(secret))
	return out
}

// encoded reports whether an answer's body is in any content coding but identity.
func encoded(h http.Header) bool {
	for _, v := range h.Values("Content-Encoding") {
		for _, c := range strings.Split(v, ",") {
			if c = strings.TrimSpace(c); c != "" && !strings.EqualFold(c, "identity") {
				return true
			}
		}
	}
	return false
}

// complete records how a forwarded request ended. The answer has already gone to the worker, so a
// failure to record it is only logged in the gateway's own error, never retried with the token.
func (g *Gateway) complete(ctx context.Context, c Completed) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = g.cfg.Recorder.Complete(rctx, g.cfg.Org, c) // the decision before it was recorded; this is only the outcome
}

func nameLabel(s string) string { return label(s, 32) }

// orgLabel is an org name as the rest of lucy takes it: 1-63 bytes of a-z, 0-9 and '-'.
func orgLabel(s string) string { return label(s, 63) }

func label(s string, max int) string {
	if s == "" || len(s) > max {
		return fmt.Sprintf("must be 1-%d bytes", max)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' && i > 0) {
			return "may hold only a-z, 0-9 and '-', starting with a letter or digit"
		}
	}
	return ""
}
