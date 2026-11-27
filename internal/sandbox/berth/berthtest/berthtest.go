// Package berthtest is a fake berth API server for tests: the endpoints of berth's documented API
// v1 that lucy uses, with berth's documented rules (berth's docs/api.md and docs/json.md). lucy's
// tests and CI talk only to it, never to a real berth or a container.
//
// It serves GET /v1, GET /v1/orgs/{org} and POST /v1/orgs/{org}/{up,restart,down}, answering with
// the documents berth documents (berth.api/v1, berth.info/v1, the operation's result) and
// failing with berth.error/v1: an unknown endpoint or org is 404, an argument the endpoint does not
// have is 400, a restart-class operation without `confirm` set to the org's name, or on a
// read-only server, is 403, and a restart of a stopped org is 409.
package berthtest

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Call is one request the server received.
type Call struct {
	Method string
	Path   string
	Body   string
}

// Server is a fake berth API.
type Server struct {
	// Socket is the Unix socket it listens on.
	Socket string

	srv *httptest.Server

	mu       sync.Mutex
	orgs     map[string]bool  // org -> running
	fail     map[string]error // org -> the next operation fails with this
	readOnly bool
	version  string
	calls    []Call
}

// Start starts a fake berth on a Unix socket in a new short-lived directory, with orgs, all
// stopped. It is closed when the test ends.
func Start(t testing.TB, orgs ...string) *Server {
	t.Helper()
	// A Unix socket path is limited to about 100 bytes, so the directory is a short one under the
	// system temp dir rather than t.TempDir.
	dir, err := os.MkdirTemp("", "berth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { // a temp dir this function made
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	sock := filepath.Join(dir, "berth.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Socket: sock, orgs: map[string]bool{}, fail: map[string]error{}, version: "0.0.0-test"}
	for _, o := range orgs {
		s.orgs[o] = false
	}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	s.srv.Listener = l
	s.srv.Start()
	t.Cleanup(s.srv.Close)
	return s
}

// SetRunning sets whether org is up.
func (s *Server) SetRunning(org string, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orgs[org] = running
}

// Running reports whether org is up.
func (s *Server) Running(org string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orgs[org]
}

// SetReadOnly makes the server refuse every change, as `berth --read-only serve` does.
func (s *Server) SetReadOnly(ro bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readOnly = ro
}

// FailNext makes the next operation on org fail with a 500 `failed` error carrying err's text.
func (s *Server) FailNext(org string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[org] = err
}

// Calls returns the requests received so far.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// Changes returns the requests received so far that were not reads.
func (s *Server) Changes() []Call {
	var out []Call
	for _, c := range s.Calls() {
		if c.Method != http.MethodGet {
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Method: r.Method, Path: r.URL.EscapedPath(), Body: string(body)})
	if r.Header.Get("Authorization") != "" {
		// The socket takes no token; a caller sending one is confused about where it is.
		fail(w, http.StatusBadRequest, "usage", "the socket takes no token", "")
		return
	}
	if len(r.URL.Query()) > 0 { // none of these endpoints takes a query argument
		fail(w, http.StatusBadRequest, "usage", "unknown argument", "")
		return
	}
	path := r.URL.EscapedPath()
	switch {
	case path == "/v1" && r.Method == http.MethodGet:
		writeJSON(w, map[string]any{"schema": "berth.api/v1", "version": s.version, "read_only": s.readOnly,
			"writes": !s.readOnly, "restarts": !s.readOnly, "admin": !s.readOnly})
		return
	case strings.HasPrefix(path, "/v1/orgs/"):
		org, op, _ := strings.Cut(strings.TrimPrefix(path, "/v1/orgs/"), "/")
		running, known := s.orgs[org]
		switch {
		case op == "" && r.Method == http.MethodGet:
			if !known {
				fail(w, http.StatusNotFound, "not-found", "unknown org '"+org+"'", "run: berth org create "+org)
				return
			}
			s.info(w, org, running)
			return
		case (op == "up" || op == "restart" || op == "down") && r.Method == http.MethodPost:
			s.operate(w, org, op, known, running, body)
			return
		}
	}
	fail(w, http.StatusNotFound, "not-found", "no such endpoint", "")
}

func (s *Server) info(w http.ResponseWriter, org string, running bool) {
	state, remote := "stopped", ""
	if running {
		state, remote = "running", "https://claude.ai/code?environment=env_test"
	}
	writeJSON(w, map[string]any{
		"schema": "berth.info/v1", "org": org, "container": "claude-" + org, "state": state, "remote_url": remote,
		"address": "127.0.0.1", "local_only": true, "ssh_port": 2201, "ttyd_port": 7701, "user": "node",
		"ssh_host": "claude-" + org, "git_public_key": "ssh-ed25519 AAAAtest claude-" + org + "@test", "tunnel": nil,
	})
}

func (s *Server) operate(w http.ResponseWriter, org, op string, known, running bool, body []byte) {
	var args map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &args); err != nil {
			fail(w, http.StatusBadRequest, "usage", "the body is not a JSON object", "")
			return
		}
	}
	for k := range args {
		if k != "confirm" {
			fail(w, http.StatusBadRequest, "usage", "unknown argument "+k, "")
			return
		}
	}
	if !known {
		fail(w, http.StatusNotFound, "not-found", "unknown org '"+org+"'", "run: berth org create "+org)
		return
	}
	if s.readOnly {
		fail(w, http.StatusForbidden, "refused", "the server is read-only", "")
		return
	}
	var confirm string
	if c, ok := args["confirm"]; ok {
		if json.Unmarshal(c, &confirm) != nil {
			fail(w, http.StatusBadRequest, "usage", "confirm is not a string", "")
			return
		}
	}
	if confirm != org {
		what := map[string]string{"up": "recreates", "restart": "recreates", "down": "stops and removes"}[op]
		fail(w, http.StatusForbidden, "refused", op+" "+what+" claude-"+org+", which stops the work running in it",
			`set "confirm": "`+org+`" to agree`)
		return
	}
	if op == "restart" && !running {
		fail(w, http.StatusConflict, "not-running", org+" is not running", "run: berth up "+org)
		return
	}
	if err, ok := s.fail[org]; ok {
		delete(s.fail, org)
		fail(w, http.StatusInternalServerError, "failed", err.Error(), "")
		return
	}
	s.orgs[org] = op != "down"
	doc := map[string]any{"org": org, "output": op + " " + org + ": done\n"}
	if op != "down" {
		doc["image"] = "berth/claude-env:test"
	}
	writeJSON(w, doc)
}

// ErrInjected is an error a test may pass to FailNext.
var ErrInjected = errors.New("the container engine failed")

func fail(w http.ResponseWriter, status int, kind, msg, hint string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"schema": "berth.error/v1", "kind": kind, "code": 1, "message": msg, "hint": hint})
}

func writeJSON(w http.ResponseWriter, doc any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}
