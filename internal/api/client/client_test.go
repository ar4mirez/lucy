package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
)

// fake serves a fixed body for every request on a unix socket in a temp dir.
func fake(t *testing.T, body string) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "lucyc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != api.Path || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body)) //nolint:errcheck // test server
	})}
	go hs.Serve(ln) //nolint:errcheck // closed at cleanup
	t.Cleanup(func() { hs.Close() })
	c := New(sock, "test", "0")
	t.Cleanup(c.Close)
	return c
}

func TestCallDecodesResultsTolerantly(t *testing.T) {
	c := fake(t, `{"jsonrpc":"2.0","id":1,"result":{"status":"ok","new_field_from_a_newer_daemon":1}}`)
	h, err := c.Health(context.Background())
	if err != nil || h.Status != "ok" {
		t.Errorf("health = %+v, %v", h, err)
	}
}

func TestCallErrors(t *testing.T) {
	ctx := context.Background()
	cases := map[string]string{
		"not json":      `<html>`,
		"wrong version": `{"jsonrpc":"1.0","id":1,"result":{}}`,
		"wrong id":      `{"jsonrpc":"2.0","id":99,"result":{}}`,
		"bad result":    `{"jsonrpc":"2.0","id":1,"result":{"status":5}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := fake(t, body).Health(ctx); err == nil {
				t.Error("accepted a bad response")
			}
		})
	}
	t.Run("api error", func(t *testing.T) {
		_, err := fake(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"forbidden","data":{"kind":"forbidden"}}}`).Health(ctx)
		if !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("not running", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "lucyc-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		c := New(filepath.Join(dir, "none.sock"), "test", "0")
		if _, err := c.Health(ctx); !errors.Is(err, ErrNotRunning) {
			t.Errorf("got %v", err)
		}
	})
}
