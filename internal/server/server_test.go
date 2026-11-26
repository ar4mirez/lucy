package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/eventstore"
)

var ctx = context.Background()

func TestRoundTrip(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)

	w, err := f.client.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w.APIVersion != api.Version || w.ServerVersion != "1.2.3-test" {
		t.Errorf("hello = %+v", w)
	}
	if w.Caller.UID != os.Geteuid() || w.Caller.Transport != "unix" || w.Caller.Actor != "local/uid/"+strconv.Itoa(os.Geteuid()) {
		t.Errorf("caller = %+v", w.Caller)
	}
	if w.Caller.PID != os.Getpid() {
		t.Errorf("caller pid = %d, want %d (this process)", w.Caller.PID, os.Getpid())
	}
	if len(w.Methods) != len(api.Methods()) || len(w.Caller.Scopes) != len(api.AllScopes) {
		t.Errorf("owner should see every method and scope: %v %v", w.Methods, w.Caller.Scopes)
	}

	if h, err := f.client.Health(ctx); err != nil || h.Status != "ok" {
		t.Errorf("health = %+v, %v", h, err)
	}

	for i := range 5 {
		if _, err := f.store.Append(ctx, "notes", eventstore.AnyVersion, "test", note{Text: "n" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := f.client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.EventHead != 5 || st.APIVersion != api.Version || st.PID != os.Getpid() || st.StartedAt.IsZero() {
		t.Errorf("status = %+v", st)
	}

	page, err := f.client.Events(ctx, api.EventsParams{AfterSeq: 1, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].Seq != 2 || page.Next != 3 || !page.More || page.Head != 5 {
		t.Fatalf("page = %+v", page)
	}
	if e := page.Events[0]; e.Type != "test.note" || e.Stream != "notes" || string(e.Payload) != `{"text":"n1"}` {
		t.Errorf("event = %+v", e)
	}
	last, err := f.client.Events(ctx, api.EventsParams{AfterSeq: page.Next})
	if err != nil || len(last.Events) != 2 || last.More || last.Next != 5 {
		t.Errorf("last page = %+v, %v", last, err)
	}
	empty, err := f.client.Events(ctx, api.EventsParams{AfterSeq: 5})
	if err != nil || len(empty.Events) != 0 || empty.Next != 5 || empty.Events == nil {
		t.Errorf("empty page = %+v, %v", empty, err)
	}

	doc, err := f.client.Discover(ctx)
	var want bytes.Buffer
	if cerr := json.Compact(&want, api.Document); cerr != nil || err != nil || !bytes.Equal(doc, want.Bytes()) {
		t.Errorf("rpc.discover did not return the committed document: %v %v", err, cerr)
	}
}

func TestHTTPBoundary(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	big := append(rpc("events.list", `{"limit":1,"x":"`), bytes.Repeat([]byte("a"), api.MaxRequestBytes)...)
	cases := []struct {
		name       string
		method     string
		path       string
		ctype      string
		body       []byte
		wantStatus int
		wantCode   api.Code
	}{
		{"wrong path", "POST", "/rpc/v2", "application/json", rpc("health", ""), 404, api.CodeMethodNotFound},
		{"get", "GET", api.Path, "", nil, 405, api.CodeInvalidRequest},
		{"no content type", "POST", api.Path, "", rpc("health", ""), 415, api.CodeInvalidRequest},
		{"form post", "POST", api.Path, "application/x-www-form-urlencoded", rpc("health", ""), 415, api.CodeInvalidRequest},
		{"too large", "POST", api.Path, "application/json", big, 413, api.CodeTooLarge},
		{"parse error", "POST", api.Path, "application/json", []byte(`{`), 200, api.CodeParseError},
		{"batch", "POST", api.Path, "application/json", []byte(`[` + string(rpc("health", "")) + `]`), 200, api.CodeInvalidRequest},
		{"unknown method", "POST", api.Path, "application/json", rpc("nope.nope", ""), 200, api.CodeMethodNotFound},
		{"unknown param", "POST", api.Path, "application/json", rpc("events.list", `{"limt":1}`), 200, api.CodeInvalidParams},
		{"param out of range", "POST", api.Path, "application/json", rpc("events.list", `{"limit":100000}`), 200, api.CodeInvalidParams},
		{"params on a no-params method", "POST", api.Path, "application/json", rpc("health", `{"x":1}`), 200, api.CodeInvalidParams},
		{"charset is fine", "POST", api.Path, "application/json; charset=utf-8", rpc("health", ""), 200, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, resp, body := f.raw(t, tc.method, tc.path, tc.ctype, tc.body)
			if status != tc.wantStatus {
				t.Errorf("HTTP %d, want %d: %s", status, tc.wantStatus, body)
			}
			if tc.wantCode == 0 {
				if resp.Error != nil {
					t.Errorf("error %+v", resp.Error)
				}
				return
			}
			if resp.Error == nil {
				t.Fatalf("no error: %s", body)
			}
			wantCode(t, resp.Error, tc.wantCode)
			if resp.Result != nil {
				t.Errorf("error response carries a result: %s", body)
			}
		})
	}
}

func TestUnsupportedVersion(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	err := f.client.Call(ctx, api.MethodHello, api.HelloParams{Client: "t", APIVersions: []int{2, 3}}, nil)
	ae := wantCode(t, err, api.CodeUnsupportedVersion)
	if !slices.Equal(ae.Data.Supported, api.SupportedVersions) || ae.Data.Field != "api_versions" {
		t.Errorf("data = %+v", ae.Data)
	}
}

func TestInternalErrorsDoNotLeak(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	f.srv.handlers[api.MethodStatus] = bind(func(context.Context, *api.NoParams) (api.Status, error) {
		return api.Status{}, errors.New("open /home/someone/.local/share/lucy/lucy.db: SECRET-DETAIL")
	})
	f.srv.handlers[api.MethodHealth] = bind(func(context.Context, *api.NoParams) (api.Health, error) {
		panic("PANIC-DETAIL")
	})
	for _, m := range []string{"status.get", "health"} {
		_, resp, body := f.raw(t, "POST", api.Path, "application/json", rpc(m, ""))
		if resp.Error == nil {
			t.Fatalf("%s: no error: %s", m, body)
		}
		ae := wantCode(t, resp.Error, api.CodeInternal)
		if len(ae.Data.RequestID) != 16 {
			t.Errorf("%s: no correlation id: %s", m, body)
		}
		noLeak(t, body, "SECRET-DETAIL", "PANIC-DETAIL", "/home/someone", "lucy.db")
	}
	// The daemon still serves after a handler panicked.
	if _, err := f.client.Events(ctx, api.EventsParams{}); err != nil {
		t.Errorf("after a panic: %v", err)
	}
}

func TestCallTimeout(t *testing.T) {
	f := start(t, os.Geteuid(), 0, func(o *Options) { o.CallTimeout = 50 * time.Millisecond })
	f.srv.handlers[api.MethodStatus] = bind(func(ctx context.Context, _ *api.NoParams) (api.Status, error) {
		<-ctx.Done()
		return api.Status{}, ctx.Err()
	})
	_, err := f.client.Status(ctx)
	wantCode(t, err, api.CodeTimeout)
}

func TestMutatingCallIsRecordedFirst(t *testing.T) {
	var stopped atomic.Int32
	f := start(t, os.Geteuid(), 0, func(o *Options) { o.OnShutdown = func() { stopped.Add(1) } })
	r, err := f.client.Shutdown(ctx)
	if err != nil || !r.Stopping || stopped.Load() != 1 {
		t.Fatalf("shutdown = %+v, %v, stopped %d", r, err, stopped.Load())
	}
	evs, err := f.store.ReadStream(ctx, CommandStream, 0, 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("api stream = %v, %v", evs, err)
	}
	reg, _ := eventstore.NewRegistry(CommandEvent{})
	ce, err := eventstore.DecodeAs[CommandEvent](reg, evs[0])
	if err != nil {
		t.Fatal(err)
	}
	want := CommandEvent{Method: api.MethodShutdown, RequestID: "1", Transport: "unix", UID: os.Geteuid(),
		PID: os.Getpid(), Device: "local", ParamsSHA256: ce.ParamsSHA256}
	if ce != want || len(ce.ParamsSHA256) != 64 || evs[0].Actor != "local/uid/"+strconv.Itoa(os.Geteuid()) {
		t.Errorf("recorded %+v by %q, want %+v", ce, evs[0].Actor, want)
	}

	// Reads are not recorded.
	if _, err := f.client.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.store.StreamVersion(ctx, CommandStream); n != 1 {
		t.Errorf("api stream has %d events after a read, want 1", n)
	}

	// If the command cannot be recorded, it does not run.
	f.store.Close()
	_, err = f.client.Shutdown(ctx)
	wantCode(t, err, api.CodeInternal)
	if stopped.Load() != 1 {
		t.Error("shutdown ran although it could not be recorded")
	}
}

// denyAdmin allows reads only.
type denyAdmin struct{}

func (denyAdmin) Scopes(context.Context, api.Caller) []api.Scope { return []api.Scope{api.ScopeRead} }
func (denyAdmin) Authorize(_ context.Context, _ api.Caller, m api.Method) *api.Error {
	if m.Scope == api.ScopeRead {
		return nil
	}
	return api.NewError(api.CodeForbidden, "no")
}

func TestAuthorizerIsConsulted(t *testing.T) {
	var stopped atomic.Int32
	f := start(t, os.Geteuid(), 0, func(o *Options) {
		o.Authorizer = denyAdmin{}
		o.OnShutdown = func() { stopped.Add(1) }
	})
	_, err := f.client.Shutdown(ctx)
	wantCode(t, err, api.CodeForbidden)
	if stopped.Load() != 0 {
		t.Error("a forbidden command ran")
	}
	if n, _ := f.store.StreamVersion(ctx, CommandStream); n != 0 {
		t.Error("a forbidden command was recorded as run")
	}
	w, err := f.client.Hello(ctx)
	if err != nil || slices.Contains(w.Methods, api.MethodShutdown) || !slices.Equal(w.Caller.Scopes, []string{"read"}) {
		t.Errorf("hello for a read-only caller = %+v, %v", w, err)
	}
}

func TestOwnerOnly(t *testing.T) {
	o := OwnerOnly{UID: 1000}
	m, _ := api.Lookup(api.MethodShutdown)
	if o.Authorize(ctx, api.Caller{Transport: "unix", UID: 1000}, m) != nil {
		t.Error("owner refused")
	}
	for _, c := range []api.Caller{{Transport: "unix", UID: 1001}, {Transport: "https", UID: 1000}, {}} {
		if e := o.Authorize(ctx, c, m); e == nil || e.Code != api.CodeForbidden {
			t.Errorf("%+v allowed", c)
		}
		if len(o.Scopes(ctx, c)) != 0 {
			t.Errorf("%+v granted scopes", c)
		}
	}
}

func TestPeerCredRefusesOtherUsers(t *testing.T) {
	// The kernel reports this test's own uid; admit only another one, so the real peer check runs
	// and refuses a real connection.
	var refusedUID atomic.Int64
	f := start(t, os.Geteuid()+1, 0, nil, func(cred PeerCred, _ error) { refusedUID.Store(int64(cred.UID)) })
	_, err := f.client.Health(ctx)
	if err == nil {
		t.Fatal("a connection from another uid was served")
	}
	if _, ok := err.(*api.Error); ok {
		t.Fatalf("refused connection got an API answer: %v", err)
	}
	waitFor(t, func() bool { return f.pl.Refused() >= 1 })
	if refusedUID.Load() != int64(os.Geteuid()) {
		t.Errorf("refusal reported uid %d", refusedUID.Load())
	}
}

func TestNoCallerIsRefused(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	// Serve the same handler on a plain listener: connections carry no caller.
	sock := f.sock + "2"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	hs := f.srv.HTTPServer()
	go hs.Serve(ln) //nolint:errcheck // closed below
	defer hs.Close()
	g := &fixture{sock: sock}
	_, resp, _ := g.raw(t, "POST", api.Path, "application/json", rpc("health", ""))
	if resp.Error == nil {
		t.Fatal("served a caller-less connection")
	}
	wantCode(t, resp.Error, api.CodeForbidden)
}

func TestConnectionLimit(t *testing.T) {
	f := start(t, os.Geteuid(), 1, nil)
	hold, err := net.Dial("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(f.pl.slots) == 1 })
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err := f.client.Health(short); err == nil {
		t.Fatal("a second connection was served past the limit")
	}
	hold.Close()
	if _, err := f.client.Health(ctx); err != nil {
		t.Errorf("after the held connection closed: %v", err)
	}
}

func TestCloseWakesAcceptAtTheLimit(t *testing.T) {
	f := start(t, os.Geteuid(), 1, nil)
	if _, err := f.client.Health(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(f.pl.slots) == 1 })
	short, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.hs.Shutdown(short) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Shutdown with every slot held: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown hung while every connection slot was held")
	}
	if _, err := f.pl.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Accept after Close = %v, want net.ErrClosed", err)
	}
	if err := f.pl.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
}

func TestHTTPServerLimits(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	hs := f.srv.HTTPServer()
	if hs.ReadHeaderTimeout == 0 || hs.ReadTimeout == 0 || hs.WriteTimeout == 0 || hs.IdleTimeout == 0 || hs.MaxHeaderBytes != MaxHeaderBytes {
		t.Errorf("unbounded server: %+v", hs)
	}
	_, resp, _ := f.raw(t, http.MethodPost, api.Path, "application/json", rpc("health", ""))
	if resp.Error != nil {
		t.Error(resp.Error)
	}
}

func TestNewChecksTheTable(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("New without a store succeeded")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
