package workflowcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const workflowsDir = "../../.github/workflows"

var pinnedUses = regexp.MustCompile(`^[\w.-]+/[\w.-]+(/[\w./-]+)?@[0-9a-f]{40}$`)

// writeAllowed lists the only jobs that may hold write scopes, keyed by workflow and job, with the
// scopes each may hold: the release job publishes, signs and attests; the Scorecard job uploads
// its results to code scanning.
var writeAllowed = map[[2]string]map[string]bool{
	{"release.yml", "release"}:    {"contents": true, "id-token": true, "attestations": true},
	{"scorecard.yml", "analysis"}: {"security-events": true},
}

// writeAllowedText describes writeAllowed for error messages.
const writeAllowedText = "release.yml's release job (contents, id-token, attestations) and scorecard.yml's analysis job (security-events)"

type workflow struct {
	On          yaml.Node      `yaml:"on"`
	Permissions yaml.Node      `yaml:"permissions"`
	Jobs        map[string]job `yaml:"jobs"`
}

type job struct {
	Permissions yaml.Node `yaml:"permissions"`
	Uses        string    `yaml:"uses"`
	Steps       []struct {
		Uses string `yaml:"uses"`
	} `yaml:"steps"`
}

// permissions reads a permissions block, written as a map or as read-all/write-all ("*" is every
// scope). set is false when the block is absent.
func permissions(n yaml.Node) (scopes map[string]string, set bool, err error) {
	switch n.Kind {
	case 0:
		return nil, false, nil
	case yaml.ScalarNode:
		switch n.Value {
		case "read-all":
			return map[string]string{"*": "read"}, true, nil
		case "write-all":
			return map[string]string{"*": "write"}, true, nil
		}
		return nil, true, fmt.Errorf("unknown permissions value %q", n.Value)
	case yaml.MappingNode:
		scopes = map[string]string{}
		if err := n.Decode(&scopes); err != nil {
			return nil, true, err
		}
		return scopes, true, nil
	}
	return nil, true, fmt.Errorf("permissions at line %d is neither a map nor a string", n.Line)
}

// check returns every rule the workflow named name breaks.
func check(name string, src []byte) []string {
	var wf workflow
	if err := yaml.Unmarshal(src, &wf); err != nil {
		return []string{fmt.Sprintf("%s: not valid YAML: %v", name, err)}
	}
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(name+": "+format, args...))
	}

	top, set, err := permissions(wf.Permissions)
	switch {
	case err != nil:
		add("top-level %v", err)
	case !set || len(top) != 1 || top["contents"] != "read":
		add("top-level permissions must be exactly {contents: read}, got %v", top)
	}

	ids := make([]string, 0, len(wf.Jobs))
	for id := range wf.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		j := wf.Jobs[id]
		uses := []string{}
		if j.Uses != "" {
			uses = append(uses, j.Uses)
		}
		for _, s := range j.Steps {
			if s.Uses != "" {
				uses = append(uses, s.Uses)
			}
		}
		for _, u := range uses {
			if !pinnedUses.MatchString(u) {
				add("job %s: %s is not owner/repo[/path]@<full commit SHA>", id, u)
			}
		}

		scopes, _, err := permissions(j.Permissions)
		if err != nil {
			add("job %s: %v", id, err)
			continue
		}
		for scope, level := range scopes {
			if level != "write" {
				continue
			}
			if !writeAllowed[[2]string{name, id}][scope] {
				add("job %s: %s: write is only allowed on %s", id, scope, writeAllowedText)
			}
		}
	}

	if name == "release.yml" {
		if err := releaseTrigger(wf.On); err != nil {
			add("%v", err)
		}
	}
	return problems
}

// releaseTrigger accepts only `on: push: tags: ["v*"]`, with no other events or filters.
func releaseTrigger(on yaml.Node) error {
	var events map[string]map[string]yaml.Node
	if on.Kind != yaml.MappingNode || on.Decode(&events) != nil {
		return fmt.Errorf("`on` must be a map holding only push")
	}
	push, ok := events["push"]
	if len(events) != 1 || !ok {
		return fmt.Errorf("the only trigger must be push, got %v", keys(events))
	}
	tagsNode, ok := push["tags"]
	if len(push) != 1 || !ok {
		return fmt.Errorf("push must filter on tags only, got %v", keys(push))
	}
	var tags []string
	if err := tagsNode.Decode(&tags); err != nil || len(tags) != 1 || tags[0] != "v*" {
		return fmt.Errorf(`push tags must be exactly ["v*"], got %v`, tags)
	}
	return nil
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRepositoryWorkflows(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(workflowsDir, "*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows found in %s: %v", workflowsDir, err)
	}
	sawRelease := false
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(p)
		sawRelease = sawRelease || name == "release.yml"
		for _, problem := range check(name, src) {
			t.Error(problem)
		}
	}
	if !sawRelease {
		t.Error("release.yml is missing")
	}
}

const sha = "3d3c42e5aac5ba805825da76410c181273ba90b1"

func TestCheckIsFormatIndependent(t *testing.T) {
	cases := []struct {
		name, file, src string
		want            string // empty: no problems
	}{
		{
			name: "flow style and comments pass",
			file: "release.yml",
			src: `on: {push: {tags: ["v*"]}}
permissions: {contents: read} # read-only
jobs:
    release:
        permissions:
            # needed to publish
            contents: write
            id-token: write
        steps:
            - uses: actions/checkout@` + sha + ` # v7
            - uses: github/codeql-action/init@` + sha + `
            - run: echo hi
`,
		},
		{
			name: "block style tags pass",
			file: "release.yml",
			src:  "on:\n  push:\n    tags:\n      - 'v*'\npermissions:\n  contents: read\njobs: {}\n",
		},
		{
			name: "unpinned action fails",
			file: "ci.yml",
			src:  "on: pull_request\npermissions: {contents: read}\njobs:\n  check:\n    steps:\n      - uses: actions/checkout@v7\n",
			want: "actions/checkout@v7 is not owner/repo",
		},
		{
			name: "pull_request trigger on release fails",
			file: "release.yml",
			src:  "on:\n  push: {tags: [\"v*\"]}\n  pull_request:\npermissions: {contents: read}\njobs: {}\n",
			want: "the only trigger must be push",
		},
		{
			name: "branches filter on release fails",
			file: "release.yml",
			src:  "on:\n  push: {tags: [\"v*\"], branches: [main]}\npermissions: {contents: read}\njobs: {}\n",
			want: "push must filter on tags only",
		},
		{
			name: "id-token write on another job fails",
			file: "release.yml",
			src:  "on: {push: {tags: [\"v*\"]}}\npermissions: {contents: read}\njobs:\n  build:\n    permissions: {id-token: write}\n",
			want: "job build: id-token: write is only allowed",
		},
		{
			name: "security-events write on the scorecard job passes",
			file: "scorecard.yml",
			src:  "on: {push: {branches: [main]}}\npermissions: {contents: read}\njobs:\n  analysis:\n    permissions: {contents: read, security-events: write}\n",
		},
		{
			name: "security-events write on another job fails",
			file: "security.yml",
			src:  "on: pull_request\npermissions: {contents: read}\njobs:\n  analysis:\n    permissions: {security-events: write}\n",
			want: "job analysis: security-events: write is only allowed",
		},
		{
			name: "contents write on the scorecard job fails",
			file: "scorecard.yml",
			src:  "on: {push: {branches: [main]}}\npermissions: {contents: read}\njobs:\n  analysis:\n    permissions: {contents: write}\n",
			want: "job analysis: contents: write is only allowed",
		},
		{
			name: "write-all fails",
			file: "release.yml",
			src:  "on: {push: {tags: [\"v*\"]}}\npermissions: {contents: read}\njobs:\n  release:\n    permissions: write-all\n",
			want: "job release: *: write is only allowed",
		},
		{
			name: "read-all at top level fails",
			file: "ci.yml",
			src:  "on: pull_request\npermissions: read-all\njobs: {}\n",
			want: "top-level permissions must be exactly {contents: read}",
		},
		{
			name: "missing top-level permissions fails",
			file: "ci.yml",
			src:  "on: pull_request\njobs: {}\n",
			want: "top-level permissions must be exactly {contents: read}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(check(tc.file, []byte(tc.src)), "\n")
			if tc.want == "" && got != "" {
				t.Errorf("want no problems, got:\n%s", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, got)
			}
		})
	}
}
