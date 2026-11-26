package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSecretFieldName(t *testing.T) {
	denied := []string{
		"password", "Password", "db_password", "passwd", "passphrase", "secret", "client_secret",
		"clientSecret", "token", "github_token", "accessToken", "refresh-token", "ID_TOKEN", "api_key",
		"apiKey", "private_key", "aws_access_key", "session_key", "signing_key", "credential",
		"credentials", "gitCredentials", "cookie", "set_cookie", "bearer", "authorization",
		"Authorization", "auth", "pat", "otp",
	}
	allowed := []string{
		"token_count", "tokens", "input_tokens", "secret_ref", "secretRef", "credential_policy",
		"author", "authority", "path", "patch", "fence", "title", "note", "keyword", "passing",
		"cookie_count", "authorization_rule",
	}
	for _, n := range denied {
		if !SecretFieldName(n) {
			t.Errorf("SecretFieldName(%q) = false, want true", n)
		}
	}
	for _, n := range allowed {
		if SecretFieldName(n) {
			t.Errorf("SecretFieldName(%q) = true, want false", n)
		}
	}
}

type leakyTag struct {
	User string `json:"user"`
	Key  string `json:"api_key"`
}

func (leakyTag) EventType() string { return "test.leaky_tag" }
func (leakyTag) EventVersion() int { return 1 }

type leakyName struct {
	Password string
}

func (leakyName) EventType() string { return "test.leaky_name" }
func (leakyName) EventVersion() int { return 1 }

type leakyNested struct {
	Repo struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	} `json:"repo"`
}

func (leakyNested) EventType() string { return "test.leaky_nested" }
func (leakyNested) EventVersion() int { return 1 }

type leakyList struct {
	Hosts []struct {
		Cookie string `json:"cookie"`
	} `json:"hosts"`
}

func (leakyList) EventType() string { return "test.leaky_list" }
func (leakyList) EventVersion() int { return 1 }

type quietSecret struct {
	Name   string `json:"name"`
	Secret string `json:"-"` // never serialized, so allowed
}

func (quietSecret) EventType() string { return "test.quiet_secret" }
func (quietSecret) EventVersion() int { return 1 }

func TestRegisterRefusesSecretFields(t *testing.T) {
	for _, p := range []Payload{leakyTag{}, leakyName{}, leakyNested{}, leakyList{}} {
		if _, err := NewRegistry(p); !errors.Is(err, ErrSecretField) {
			t.Errorf("Register(%T) = %v, want ErrSecretField", p, err)
		}
	}
	if _, err := NewRegistry(quietSecret{}); err != nil {
		t.Errorf("Register(quietSecret) = %v, want nil: json:\"-\" fields are never stored", err)
	}
}

func TestAppendRefusesSecretKeysInMaps(t *testing.T) {
	s := openTest(t, "")
	ctx := context.Background()
	_, err := s.Append(ctx, "s", AnyVersion, "test", freeform{Fields: map[string]string{"GITHUB_TOKEN": "x"}})
	if !errors.Is(err, ErrSecretField) {
		t.Fatalf("append with a token key = %v, want ErrSecretField", err)
	}
	if v, _ := s.StreamVersion(ctx, "s"); v != 0 {
		t.Fatalf("stream version after refused append = %d, want 0", v)
	}
	mustAppend(t, s, "s", NoStream, freeform{Fields: map[string]string{"branch": "main"}})
}

type badName struct{}

func (badName) EventType() string { return "Task Created" }
func (badName) EventVersion() int { return 1 }

type badVersion struct{}

func (badVersion) EventType() string { return "task.bad" }
func (badVersion) EventVersion() int { return 0 }

func TestRegisterValidates(t *testing.T) {
	if _, err := NewRegistry(badName{}); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("bad type name: %v, want ErrInvalidEvent", err)
	}
	if _, err := NewRegistry(badVersion{}); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("bad version: %v, want ErrInvalidEvent", err)
	}
	if _, err := NewRegistry(taskCreated{}, taskCreated{}); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("duplicate: %v, want ErrInvalidEvent", err)
	}
	if _, err := NewRegistry(nil); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("nil: %v, want ErrInvalidEvent", err)
	}
}

// impostor claims to be task.created v1 but is a different Go type.
type impostor struct {
	Title string `json:"title"`
}

func (impostor) EventType() string { return "task.created" }
func (impostor) EventVersion() int { return 1 }

type unregistered struct{}

func (unregistered) EventType() string { return "test.unregistered" }
func (unregistered) EventVersion() int { return 1 }

func TestAppendValidates(t *testing.T) {
	s := openTest(t, "")
	ctx := context.Background()
	cases := []struct {
		name   string
		stream string
		actor  string
		exp    int64
		ps     []Payload
		want   error
	}{
		{"unregistered type", "s", "a", AnyVersion, []Payload{unregistered{}}, ErrUnknownType},
		{"impostor type", "s", "a", AnyVersion, []Payload{impostor{}}, ErrInvalidEvent},
		{"nil payload", "s", "a", AnyVersion, []Payload{nil}, ErrInvalidEvent},
		{"no payloads", "s", "a", AnyVersion, nil, ErrInvalidEvent},
		{"empty stream", "", "a", AnyVersion, []Payload{taskCreated{}}, ErrInvalidEvent},
		{"control char in stream", "s\n", "a", AnyVersion, []Payload{taskCreated{}}, ErrInvalidEvent},
		{"invalid utf8 actor", "s", "\xff", AnyVersion, []Payload{taskCreated{}}, ErrInvalidEvent},
		{"long stream", strings.Repeat("s", MaxStreamBytes+1), "a", AnyVersion, []Payload{taskCreated{}}, ErrInvalidEvent},
		{"long actor", "s", strings.Repeat("a", MaxActorBytes+1), AnyVersion, []Payload{taskCreated{}}, ErrInvalidEvent},
		{"bad expected", "s", "a", -2, []Payload{taskCreated{}}, ErrInvalidEvent},
		{"payload too large", "s", "a", AnyVersion, []Payload{blob{Data: strings.Repeat("x", MaxPayloadBytes)}}, ErrPayloadTooLarge},
		{"too many events", "s", "a", AnyVersion, make([]Payload, MaxEventsPerAppend+1), ErrInvalidEvent},
	}
	for _, c := range cases {
		if _, err := s.Append(ctx, c.stream, c.exp, c.actor, c.ps...); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if h, _ := s.Head(ctx); h != 0 {
		t.Fatalf("head after refused appends = %d, want 0", h)
	}
	// Exactly at the cap is fine: {"data":"..."} adds 11 bytes.
	mustAppend(t, s, "s", NoStream, blob{Data: strings.Repeat("x", MaxPayloadBytes-11)})
}

func TestDecodeIsStrictAndVersioned(t *testing.T) {
	s := openTest(t, "")
	evs := mustAppend(t, s, "task-1", NoStream, taskNoted{Note: "a", Tokens: 3}, taskNotedV2{Note: "b", Author: "c"})
	reg := testRegistry(t)
	v1, err := DecodeAs[taskNoted](reg, evs[0])
	if err != nil || v1.Note != "a" || v1.Tokens != 3 {
		t.Fatalf("v1 decode = %+v, %v", v1, err)
	}
	v2, err := DecodeAs[taskNotedV2](reg, evs[1])
	if err != nil || v2.Author != "c" {
		t.Fatalf("v2 decode = %+v, %v", v2, err)
	}
	if _, err := DecodeAs[taskNoted](reg, evs[1]); err == nil {
		t.Fatal("decoding v2 as v1 succeeded")
	}
	bad := evs[0]
	bad.Payload = json.RawMessage(`{"note":"a","token_count":1,"extra":true}`)
	if _, err := reg.Decode(bad); err == nil {
		t.Fatal("decode with an unknown field succeeded")
	}
	bad.Payload = json.RawMessage(`{"note":"a"} {}`)
	if _, err := reg.Decode(bad); err == nil {
		t.Fatal("decode with trailing data succeeded")
	}
	bad.Type = "test.unregistered"
	if _, err := reg.Decode(bad); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("decode unknown type = %v, want ErrUnknownType", err)
	}
}
