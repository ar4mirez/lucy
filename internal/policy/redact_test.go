package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// secretShapes are commands and URLs carrying a secret, each with the secret they must not record.
var secretShapes = []struct{ Text, Secret string }{
	{`curl -H 'Authorization: Bearer abc.def.ghi' https://api.example.com`, "abc.def.ghi"},
	{`curl -H "Cookie: session=s3ssionvalue" https://example.com`, "s3ssionvalue"},
	{`python3 -c 'import requests; requests.get(u, headers={"Authorization": "Bearer pydict.bearer.v"})'`, "pydict.bearer.v"},
	{`curl -d '{"Authorization":"Bearer json.bearer.v"}' https://example.com`, "json.bearer.v"},
	{`git clone https://alice:hunter2pw@example.com/r.git`, "hunter2pw"},
	{`https://example.com/x?access_token=tok123value&page=2`, "tok123value"},
	{`https://example.com/x?sig=signature9&q=1`, "signature9"},
	{`https://example.com/x?api_key=k3yvalue`, "k3yvalue"},
	{`https://example.com/x?password=pw4value`, "pw4value"},
	{`export GITHUB_TOKEN=plainvalue1 && gh pr list`, "plainvalue1"},
	{`DB_PASSWORD='quoted value' ./migrate`, "quoted value"},
	{`MY_SECRET="dq1value" make`, "dq1value"},
	{`api_key=lowercasevalue go run .`, "lowercasevalue"},
	{`echo AUTH_HEADER=authvalue1`, "authvalue1"},
	{`echo ghp_0123456789abcdefABCDEF0123`, "ghp_0123456789abcdefABCDEF0123"},
	{`echo gho_0123456789abcdefABCDEF0123`, "gho_0123456789abcdefABCDEF0123"},
	{`echo github_pat_11ABCDEFG0123456789_abcdef`, "github_pat_11ABCDEFG0123456789_abcdef"},
	{`echo sk-proj0123456789abcdefXYZ`, "sk-proj0123456789abcdefXYZ"},
	{`echo xoxb-1234-5678-abcdefgh`, "xoxb-1234-5678-abcdefgh"},
	{`echo xoxp-1234-5678-abcdefgh`, "xoxp-1234-5678-abcdefgh"},
	{`echo AKIAIOSFODNN7EXAMPLE`, "AKIAIOSFODNN7EXAMPLE"},
	{`echo glpat-0123456789abcdefXYZ`, "glpat-0123456789abcdefXYZ"},
	{`echo 9fK2mQ7xL4pR8sT1vW3yZ6bN0cH5jD2gA7eU`, "9fK2mQ7xL4pR8sT1vW3yZ6bN0cH5jD2gA7eU"},
}

func TestRecordedResourceRedacts(t *testing.T) {
	for _, k := range []Kind{KindExec, KindFetch} {
		for _, c := range secretShapes {
			got := RecordedResource(k, c.Text)
			if strings.Contains(got, c.Secret) || !strings.Contains(got, Redacted) {
				t.Errorf("%s %q recorded as %q", k, c.Text, got)
			}
			sum := sha256.Sum256([]byte(c.Text))
			if !strings.HasSuffix(got, " sha256:"+hex.EncodeToString(sum[:])) {
				t.Errorf("%q: no digest of the original in %q", c.Text, got)
			}
		}
	}
}

func TestRecordedResourceKeepsPlainText(t *testing.T) {
	for _, s := range []string{"go test ./internal/policy -run TestRecordedResourceKeepsPlainText", "https://pkg.go.dev/net/http?tab=doc", "ls -la"} {
		if got := RecordedResource(KindExec, s); !strings.HasPrefix(got, s+" sha256:") {
			t.Errorf("%q recorded as %q", s, got)
		}
	}
}

func TestRecordedResourceDigestAndCap(t *testing.T) {
	a, b := RecordedResource(KindExec, "echo TOKEN=one"), RecordedResource(KindExec, "echo TOKEN=two")
	if a != RecordedResource(KindExec, "echo TOKEN=one") {
		t.Error("the same text records differently")
	}
	if a == b || a[:strings.Index(a, " sha256:")] != b[:strings.Index(b, " sha256:")] {
		t.Errorf("texts that redact alike must differ only by digest: %q, %q", a, b)
	}
	long := strings.Repeat("ä", 3000)
	got := RecordedResource(KindFetch, long)
	text, _, _ := strings.Cut(got, " sha256:")
	if len(text) > MaxRecordedText+len("…") || !strings.HasSuffix(text, "…") || !strings.HasPrefix(long, strings.TrimSuffix(text, "…")) {
		t.Errorf("capped text is %d bytes: %q", len(text), text)
	}
	for _, k := range []Kind{KindRead, KindWrite, KindDelete, KindMerge, KindCredentialUse} {
		if got := RecordedResource(k, "/w/TOKEN=x"); got != "/w/TOKEN=x" {
			t.Errorf("%s resource changed: %q", k, got)
		}
	}
	if got := RecordedResource(KindExec, ""); got != "" {
		t.Errorf("empty resource recorded as %q", got)
	}
}

// TestRecordDecidesOnTheOriginal: the decision, and the action digest an approval binds to, come
// from the original text; only the record's resource is redacted.
func TestRecordDecidesOnTheOriginal(t *testing.T) {
	cmd := secretShapes[0].Text
	req := request(worker, KindExec, "app", cmd)
	d := Evaluate(req, Facts{})
	rec := NewRecord(req, Facts{}, d)
	if d.ActionSHA256 != ActionDigest(req) || rec.ActionSHA256 != d.ActionSHA256 || rec.Outcome != d.Outcome || rec.Rule != d.Rule {
		t.Errorf("decision %+v, record %+v", d, rec)
	}
	if rec.Target.Resource != RecordedResource(KindExec, cmd) || req.Target.Resource != cmd {
		t.Errorf("record resource %q, request resource %q", rec.Target.Resource, req.Target.Resource)
	}
	redacted := req
	redacted.Target.Resource = rec.Target.Resource
	if Evaluate(redacted, Facts{}).ActionSHA256 == d.ActionSHA256 {
		t.Error("the action digest does not depend on the original text")
	}
}
