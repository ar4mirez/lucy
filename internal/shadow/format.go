// Package shadow is lucy's shadow mode (LUCY-RUN-012, first half): the existing supervisor's
// actions on an org's tasks, imported from an offline file lucy never fetches, so that lucy's own
// decisions on the same tasks can be compared with them per task class before lucy takes a class
// over. lucy reads no live state for it: no forge, no harness, no container and no other tool's
// files. The file is the owner's to produce; its format is docs/tech/shadow.md.
//
// The format is versioned JSON Lines and read through an allowlist: every field is named and
// bounded, anything else is refused, and so is any value that looks like a credential. A task
// carries no transcript, no file contents and no answer text.
package shadow

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Version is the format version this lucy reads.
const Version = 1

// Limits.
const (
	MaxFileBytes = 8 << 20 // one import file
	MaxLineBytes = 4 << 10 // one line
	MaxTasks     = 2000    // tasks in one file
	MaxActions   = 40      // actions of one task
	MaxGoal      = 512     // bytes of a task's goal
	MaxText      = 512     // bytes of a question
)

// Postures a task was run under, as lucy names them.
var postures = []string{"green", "owner", "client"}

// Actions the existing supervisor took, in the order a task can take them.
const (
	ActDispatched      = "dispatched"       // it started a Pilot on the task
	ActAsked           = "asked"            // the Pilot asked a question (text)
	ActAnswered        = "answered"         // the question was answered (by: supervisor or owner)
	ActPROpened        = "pr_opened"        // the Pilot opened a pull request (checks: passing, failing, pending)
	ActReviewRequested = "review_requested" // it asked for a review
	ActMerged          = "merged"           // it merged the pull request
	ActClosed          = "closed"           // it closed the task or the pull request without merging
	ActHandedToOwner   = "handed_to_owner"  // it stopped and left the task to the owner
)

var actions = []string{ActDispatched, ActAsked, ActAnswered, ActPROpened, ActReviewRequested, ActMerged, ActClosed, ActHandedToOwner}

// terminal actions end a task: nothing may follow them.
var terminal = map[string]bool{ActMerged: true, ActClosed: true}

// Task is one task the existing supervisor ran, with its actions.
type Task struct {
	Org        string   `json:"org"`
	ID         string   `json:"task"`
	Class      string   `json:"class"`
	Goal       string   `json:"goal"`
	Repository string   `json:"repository"`
	Posture    string   `json:"posture"`
	Actions    []Action `json:"actions"`
}

// Action is one thing the existing supervisor did, or saw done, on a task.
type Action struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Text   string    `json:"text,omitempty"`   // asked: the question
	By     string    `json:"by,omitempty"`     // answered: supervisor or owner
	Checks string    `json:"checks,omitempty"` // pr_opened: passing, failing or pending
}

var (
	idRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	classRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	orgRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	repoRE  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// credential matches values that look like a credential: known token prefixes, private key
// blocks, JWTs and cloud access key ids, each starting the text or following a character that is
// not a letter or digit, so a word such as task-runner-integration-tests is not one. A file
// holding one is refused whole.
var credential = regexp.MustCompile(`(?:^|[^A-Za-z0-9])(?:(?i:gh[pousr]_[a-z0-9]{16,}|github_pat_|glpat-|xox[abprs]-|sk-[a-z0-9_-]{16,}|sk_(?:live|test)_|rk_(?:live|test)_|lcap_|eyJ[a-z0-9_-]{8,}\.eyJ[a-z0-9_-]{8,}|npm_[a-z0-9]{36})|AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY|AIza[0-9A-Za-z_-]{35})`)

// ErrRefused wraps every reason a file, a line or a task is refused.
var ErrRefused = errors.New("shadow: refused")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// line is one line of the file, as the allowlist reads it.
type line struct {
	V          *int    `json:"v"`
	Type       string  `json:"type"`
	Org        *string `json:"org"`
	Task       *string `json:"task"`
	Class      *string `json:"class"`
	Goal       *string `json:"goal"`
	Repository *string `json:"repository"`
	Posture    *string `json:"posture"`
	At         *string `json:"at"`
	Action     *string `json:"action"`
	Text       *string `json:"text"`
	By         *string `json:"by"`
	Checks     *string `json:"checks"`
}

// fields each line type may carry; any other field is refused.
var fields = map[string][]string{
	"task":   {"v", "type", "org", "task", "class", "goal", "repository", "posture"},
	"action": {"v", "type", "task", "at", "action", "text", "by", "checks"},
}

// Parse reads an import file: a task line before its actions, every line a JSON object of version
// 1. It refuses the whole file, naming the line, if any line is refused or any task is invalid.
func Parse(r io.Reader) ([]Task, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, refuse("the file is over %d bytes", MaxFileBytes)
	}
	var tasks []Task
	var taskLines []int
	var actionLines [][]int
	index := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, MaxLineBytes), MaxLineBytes)
	n := 0
	for sc.Scan() {
		n++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		l, err := readLine(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		switch l.Type {
		case "task":
			t := Task{Org: *l.Org, ID: *l.Task, Class: *l.Class, Goal: *l.Goal, Repository: *l.Repository, Posture: *l.Posture}
			if _, dup := index[t.ID]; dup {
				return nil, fmt.Errorf("line %d: %w", n, refuse("task %q appears twice", t.ID))
			}
			if len(tasks) == MaxTasks {
				return nil, fmt.Errorf("line %d: %w", n, refuse("more than %d tasks", MaxTasks))
			}
			index[t.ID] = len(tasks)
			tasks = append(tasks, t)
			taskLines = append(taskLines, n)
			actionLines = append(actionLines, nil)
		case "action":
			i, ok := index[*l.Task]
			if !ok {
				return nil, fmt.Errorf("line %d: %w", n, refuse("an action of task %q before its task line", *l.Task))
			}
			at, err := time.Parse(time.RFC3339, *l.At)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n, refuse("at is not an RFC 3339 time"))
			}
			a := Action{At: at.UTC(), Action: *l.Action}
			for dst, src := range map[*string]*string{&a.Text: l.Text, &a.By: l.By, &a.Checks: l.Checks} {
				if src != nil {
					*dst = *src
				}
			}
			tasks[i].Actions = append(tasks[i].Actions, a)
			actionLines[i] = append(actionLines[i], n)
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("line %d: %w", n+1, refuse("a line is over %d bytes", MaxLineBytes))
		}
		return nil, err
	}
	for i, t := range tasks {
		if a, err := validate(t); err != nil {
			at := taskLines[i]
			if a >= 0 {
				at = actionLines[i][a]
			}
			return nil, fmt.Errorf("line %d: task %q: %w", at, t.ID, err)
		}
	}
	return tasks, nil
}

// readLine decodes one line through the allowlist.
func readLine(raw []byte) (line, error) {
	if !utf8.Valid(raw) {
		return line{}, refuse("not UTF-8")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return line{}, refuse("not a JSON object")
	}
	var l line
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return line{}, refuse("a field is unknown or of the wrong type: %s", firstUnknown(keys))
	}
	if l.V == nil || *l.V != Version {
		return line{}, refuse("v must be %d", Version)
	}
	allowed, ok := fields[l.Type]
	if !ok {
		return line{}, refuse("type must be task or action")
	}
	for k := range keys {
		if !slices.Contains(allowed, k) {
			return line{}, refuse("a %s line has no field %q", l.Type, k)
		}
	}
	need := map[string][]*string{
		"task":   {l.Org, l.Task, l.Class, l.Goal, l.Repository, l.Posture},
		"action": {l.Task, l.At, l.Action},
	}[l.Type]
	names := map[string]string{"task": "org, task, class, goal, repository and posture", "action": "task, at and action"}[l.Type]
	for _, p := range need {
		if p == nil {
			return line{}, refuse("a %s line needs %s", l.Type, names)
		}
	}
	return l, nil
}

func firstUnknown(keys map[string]json.RawMessage) string {
	known := append(slices.Clone(fields["task"]), fields["action"]...)
	ks := make([]string, 0, len(keys))
	for k := range keys {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	for _, k := range ks {
		if !slices.Contains(known, k) {
			return fmt.Sprintf("%q", k)
		}
	}
	return "check each field's type"
}

// Validate checks a task as an import accepts it: its names, bounds and actions, and that no value
// looks like a credential. The daemon checks every task again, whatever a client checked.
func Validate(t Task) error {
	_, err := validate(t)
	return err
}

// validate is Validate, also returning the index of the refused action, or -1 when the task's own
// fields are refused.
func validate(t Task) (int, error) {
	switch {
	case !orgRE.MatchString(t.Org):
		return -1, refuse("org %q is not an org name", t.Org)
	case !idRE.MatchString(t.ID):
		return -1, refuse("task %q: lower-case letters, digits, '.', '_' and '-', at most 64", t.ID)
	case !classRE.MatchString(t.Class):
		return -1, refuse("class %q: lower-case letters, digits and '-', at most 32", t.Class)
	case !repoRE.MatchString(t.Repository):
		return -1, refuse("repository %q is not a repository name", t.Repository)
	case !slices.Contains(postures, t.Posture):
		return -1, refuse("posture must be green, owner or client")
	case t.Goal == "" || len(t.Goal) > MaxGoal || !utf8.ValidString(t.Goal):
		return -1, refuse("goal: 1 to %d bytes of UTF-8", MaxGoal)
	case len(t.Actions) == 0 || len(t.Actions) > MaxActions:
		return -1, refuse("1 to %d actions", MaxActions)
	case t.Actions[0].Action != ActDispatched:
		return 0, refuse("a task's first action is dispatched")
	}
	if credential.MatchString(t.Goal) {
		return -1, refuse("the goal holds what looks like a credential")
	}
	for i, a := range t.Actions {
		if err := validAction(a); err != nil {
			return i, fmt.Errorf("action %d: %w", i+1, err)
		}
		if i > 0 && a.At.Before(t.Actions[i-1].At) {
			return i, fmt.Errorf("action %d: %w", i+1, refuse("actions are in time order"))
		}
		if i > 0 && terminal[t.Actions[i-1].Action] {
			return i, fmt.Errorf("action %d: %w", i+1, refuse("nothing follows %s", t.Actions[i-1].Action))
		}
	}
	return -1, nil
}

func validAction(a Action) error {
	if !slices.Contains(actions, a.Action) {
		return refuse("action must be one of %s", strings.Join(actions, ", "))
	}
	if a.At.IsZero() {
		return refuse("at is needed")
	}
	want := map[string]string{ActAsked: "text", ActAnswered: "by", ActPROpened: "checks"}[a.Action]
	for name, v := range map[string]string{"text": a.Text, "by": a.By, "checks": a.Checks} {
		if name != want && v != "" {
			return refuse("a %s action has no %s", a.Action, name)
		}
	}
	switch a.Action {
	case ActAsked:
		if a.Text == "" || len(a.Text) > MaxText || !utf8.ValidString(a.Text) {
			return refuse("an asked action's text is the question: 1 to %d bytes of UTF-8", MaxText)
		}
		if credential.MatchString(a.Text) {
			return refuse("the question holds what looks like a credential")
		}
	case ActAnswered:
		if a.By != "supervisor" && a.By != "owner" {
			return refuse("an answered action's by is supervisor or owner")
		}
	case ActPROpened:
		if a.Checks != "passing" && a.Checks != "failing" && a.Checks != "pending" {
			return refuse("a pr_opened action's checks are passing, failing or pending")
		}
	}
	return nil
}
