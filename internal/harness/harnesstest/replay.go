// Package harnesstest replays recorded harness protocol transcripts, for tests: a transcript plays
// the harness's side of a session, so an adapter and a runner are tested against the protocol
// without a real harness, a model, a login or the network.
//
// A transcript is JSON lines. The first is a header, {"version": "<v>", "answer": "<text>"}: the
// version the harness reports and, for a session that asks, the answer the test gives. Each later
// line is one of:
//
//	{"out": {...}}   a message the harness writes to its standard output
//	{"in": {...}}    a message the harness must read next on its input; every member given must
//	                 match, members not given are not checked
//	{"stderr": "x"}  a line the harness writes to its standard error
//
// After the last line the harness waits for its input to close and exits 0.
package harnesstest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
)

// Header is a transcript's first line.
type Header struct {
	Version string `json:"version"`
	Answer  string `json:"answer,omitempty"`
}

// Line is one later line of a transcript.
type Line struct {
	Out    json.RawMessage `json:"out,omitempty"`
	In     json.RawMessage `json:"in,omitempty"`
	Stderr string          `json:"stderr,omitempty"`
}

// Transcript is a parsed transcript.
type Transcript struct {
	Header Header
	Lines  []Line
}

// Parse reads a transcript.
func Parse(r io.Reader) (Transcript, error) {
	var t Transcript
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	n := 0
	for sc.Scan() {
		n++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var err error
		if n == 1 {
			err = dec.Decode(&t.Header)
		} else {
			var l Line
			if err = dec.Decode(&l); err == nil {
				t.Lines = append(t.Lines, l)
			}
		}
		if err != nil {
			return Transcript{}, fmt.Errorf("harnesstest: line %d: %w", n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return Transcript{}, err
	}
	if t.Header.Version == "" {
		return Transcript{}, fmt.Errorf("harnesstest: the first line must be a header with a version")
	}
	return t, nil
}

// Matches reports whether got has every member of want, recursively: objects match on want's
// members, arrays element by element, everything else by equality.
func Matches(want, got any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok || !Matches(wv, gv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !Matches(w[i], g[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(want, got)
}

// MatchJSON reports whether the JSON got matches the JSON want, as Matches does.
func MatchJSON(want, got []byte) bool {
	var w, g any
	if json.Unmarshal(want, &w) != nil || json.Unmarshal(got, &g) != nil {
		return false
	}
	return Matches(w, g)
}

// Replay plays t as the harness, started with args: it prints the version for --version, and
// otherwise writes every out line and checks every in line against what it reads. It returns the
// process's exit status: 0 when the input matched and was closed, 3 when it did not match.
func Replay(t Transcript, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if slices.Contains(args, "--version") {
		fmt.Fprintf(stdout, "%s (Claude Code)\n", t.Header.Version)
		return 0
	}
	in := bufio.NewScanner(stdin)
	in.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for i, l := range t.Lines {
		switch {
		case l.Out != nil:
			if _, err := fmt.Fprintf(stdout, "%s\n", l.Out); err != nil {
				return 1
			}
		case l.In != nil:
			if !in.Scan() {
				fmt.Fprintf(stderr, "fake harness: line %d: input ended, want %s\n", i+2, l.In)
				return 3
			}
			if !MatchJSON(l.In, in.Bytes()) {
				fmt.Fprintf(stderr, "fake harness: line %d: read %s, want %s\n", i+2, in.Bytes(), l.In)
				return 3
			}
		case l.Stderr != "":
			fmt.Fprintln(stderr, l.Stderr)
		}
	}
	for in.Scan() {
		// the session is over; anything more is ignored until the input closes
	}
	return 0
}
