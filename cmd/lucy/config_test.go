package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	dir := t.TempDir()
	write := func(name, doc string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("airspace.yaml", "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\nspec: { displayName: Acme }\n")
	code, out, errOut := lucy("config", "validate", dir)
	if code != 0 || out != "config: "+dir+"\nresources[1]{source,kind,namespace,name,valid,reason}:\n  airspace.yaml#1,Airspace,acme,acme,True,Admitted\nresult: all 1 valid\n" {
		t.Fatalf("valid config: code %d stdout %q stderr %q", code, out, errOut)
	}

	write("pilot.yaml", "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p1, namespace: acme }\nspec: { role: captain }\nstatus: {}\n")
	write("runway.yaml", "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\nspec: { colour: red }\n")
	write("bomb.yaml", "a: &a [x, x]\nb: [*a, *a]\n")
	before, _ := os.ReadFile(filepath.Join(dir, "pilot.yaml"))
	code, out, _ = lucy("config", "validate", dir)
	for _, want := range []string{
		"resources[4]{source,kind,namespace,name,valid,reason}:\n",
		"  bomb.yaml,,,,False,DecodeError\n",
		"  pilot.yaml#1,Pilot,acme,p1,False,StatusInConfig\n",
		"  runway.yaml#1,Runway,acme,web,False,SchemaViolation\n",
		"conditions[3]{source,type,status,reason,message}:\n",
		"anchors (&a) are not allowed",
		"violations[1]{source,violation}:\n  runway.yaml#1,/spec: additional properties 'colour' not allowed\n",
		"result: 3 of 4 refused\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	if code != 1 {
		t.Errorf("refused config: code %d", code)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "pilot.yaml")); string(after) != string(before) {
		t.Error("validate changed a config file")
	}

	empty := t.TempDir()
	if code, out, _ := lucy("config", "validate", empty); code != 0 || !strings.Contains(out, "resources[0]: no .yaml") {
		t.Errorf("empty dir: code %d stdout %q", code, out)
	}
	if code, _, errOut := lucy("config", "validate", filepath.Join(dir, "missing")); code != 2 || !strings.Contains(errOut, "help: pass the directory") {
		t.Errorf("missing dir: code %d stderr %q", code, errOut)
	}
	// With no argument it reads $XDG_CONFIG_HOME/lucy/resources.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if code, _, errOut := lucy("config", "validate"); code != 2 || !strings.Contains(errOut, filepath.Join("lucy", "resources")) {
		t.Errorf("default dir: code %d stderr %q", code, errOut)
	}
	for _, args := range [][]string{{"config"}, {"config", "nope"}, {"config", "validate", "a", "b"}, {"audit"}, {"audit", "nope"}, {"audit", "verify", "x"}, {"audit", "export"}, {"audit", "verify", "--file", "chain.jsonl"}} {
		if code, _, _ := lucy(args...); code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
	for _, args := range [][]string{{"config", "--help"}, {"config", "validate", "--help"}, {"audit", "--help"}, {"audit", "verify", "--help"}} {
		if code, out, _ := lucy(args...); code != 0 || !strings.HasPrefix(out, "Usage:") {
			t.Errorf("%v: code %d stdout %q", args, code, out)
		}
	}
}
