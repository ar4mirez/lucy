package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	version = "1.2.3-test"
	t.Cleanup(func() { version = "dev" })

	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"no args shows help", nil, 0, "Usage:", ""},
		{"long help", []string{"--help"}, 0, "Usage:", ""},
		{"short help", []string{"-h"}, 0, "Usage:", ""},
		{"long version", []string{"--version"}, 0, "lucy 1.2.3-test\n", ""},
		{"version command", []string{"version"}, 0, "lucy 1.2.3-test\n", ""},
		{"unknown flag", []string{"--nope"}, 2, "", `unknown command or flag "--nope"`},
		{"self-update help", []string{"self-update", "--help"}, 0, "lucy self-update --check", ""},
		{"self-update usage error", []string{"self-update", "--to", "nope"}, 2, "", "is not a release version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tc.args, &out, &errOut)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			if !strings.Contains(out.String(), tc.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tc.wantStdout)
			}
			if tc.wantStderr == "" && errOut.Len() > 0 {
				t.Errorf("unexpected stderr %q", errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tc.wantStderr)
			}
		})
	}
}
