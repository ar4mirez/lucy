package shadow_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/shadow"
)

// The oracle is docs/tech/shadow.md and LUCY-RUN-012: a versioned JSON Lines format read through an
// allowlist that refuses unknown fields, credentials, transcripts and file contents, and refuses
// the whole file, naming the line, when any line is refused.

func TestAFileParses(t *testing.T) {
	f, err := os.Open("testdata/tasks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tasks, err := shadow.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].ID != "t-1" || tasks[0].Class != "bugfix" || len(tasks[0].Actions) != 5 || tasks[1].Posture != "owner" {
		t.Fatalf("tasks %+v", tasks)
	}
	a := tasks[0].Actions[1]
	if a.Action != shadow.ActAsked || a.Text != "which region?" || !a.At.Equal(time.Date(2026, 11, 20, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("the question %+v", a)
	}
	if tasks[0].Actions[2].By != "owner" || tasks[0].Actions[3].Checks != "passing" {
		t.Fatalf("actions %+v", tasks[0].Actions)
	}
}

const task = `{"v":1,"type":"task","org":"acme","task":"t-1","class":"bugfix","goal":"fix it","repository":"web","posture":"green"}`

func act(rest string) string {
	return `{"v":1,"type":"action","task":"t-1","at":"2026-11-20T10:00:00Z",` + rest + `}`
}

var dispatched = act(`"action":"dispatched"`)

// A secret-looking value is built at run time, so no scanner takes the test for a leak.
var fakeToken = "gh" + "p_" + strings.Repeat("A1", 18)

func TestRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		file, want string
	}{
		"an unknown field":           {strings.Replace(task, `"posture"`, `"transcript":"...","posture"`, 1) + "\n" + dispatched, "line 1"},
		"file contents":              {task + "\n" + act(`"action":"dispatched","content":"package main"`), "line 2"},
		"a field of the other type":  {strings.Replace(task, `"posture"`, `"text":"x","posture"`, 1) + "\n" + dispatched, `no field "text"`},
		"another version":            {strings.Replace(task, `"v":1`, `"v":2`, 1) + "\n" + dispatched, "v must be 1"},
		"no version":                 {strings.Replace(task, `"v":1,`, ``, 1) + "\n" + dispatched, "v must be 1"},
		"a missing field":            {strings.Replace(task, `"repository":"web",`, ``, 1) + "\n" + dispatched, "needs org, task"},
		"an unknown type":            {strings.Replace(task, `"type":"task"`, `"type":"note"`, 1), "type must be"},
		"not JSON":                   {task + "\nnot json", "line 2"},
		"an action before its task":  {dispatched + "\n" + task, "before its task line"},
		"a task twice":               {task + "\n" + dispatched + "\n" + task, "appears twice"},
		"a credential in the goal":   {strings.Replace(task, "fix it", "use "+fakeToken, 1) + "\n" + dispatched, "credential"},
		"a credential in a question": {task + "\n" + dispatched + "\n" + act(`"action":"asked","text":"is `+fakeToken+` right?"`), "credential"},
		"a private key":              {task + "\n" + dispatched + "\n" + act(`"action":"asked","text":"-----BEGIN RSA PRIVATE KEY----- ok?"`), "credential"},
		"an answer's text":           {task + "\n" + dispatched + "\n" + act(`"action":"answered","by":"owner","text":"eu-west"`), "has no text"},
		"an answer without by":       {task + "\n" + dispatched + "\n" + act(`"action":"answered"`), "supervisor or owner"},
		"a question without text":    {task + "\n" + dispatched + "\n" + act(`"action":"asked"`), "is the question"},
		"unknown checks":             {task + "\n" + dispatched + "\n" + act(`"action":"pr_opened","checks":"green"`), "passing, failing or pending"},
		"an unknown action":          {task + "\n" + dispatched + "\n" + act(`"action":"rebooted"`), "action must be one of"},
		"not first dispatched":       {task + "\n" + act(`"action":"merged"`), "first action is dispatched"},
		"after a merge":              {task + "\n" + dispatched + "\n" + act(`"action":"merged"`) + "\n" + act(`"action":"closed"`), "nothing follows merged"},
		"out of time order":          {task + "\n" + strings.Replace(dispatched, "10:00", "11:00", 1) + "\n" + act(`"action":"merged"`), "time order"},
		"not a time":                 {task + "\n" + strings.Replace(dispatched, "2026-11-20T10:00:00Z", "yesterday", 1), "RFC 3339"},
		"a bad posture":              {strings.Replace(task, `"green"`, `"yolo"`, 1) + "\n" + dispatched, "posture"},
		"a bad class":                {strings.Replace(task, `"bugfix"`, `"Bug Fix"`, 1) + "\n" + dispatched, "class"},
		"a bad org":                  {strings.Replace(task, `"acme"`, `"../acme"`, 1) + "\n" + dispatched, "org"},
		"a task with no actions":     {task, "1 to 40 actions"},
		"a line over the limit":      {task + "\n" + act(`"action":"asked","text":"`+strings.Repeat("x", shadow.MaxLineBytes)+`"`), "over 4096 bytes"},
		"a question over the limit":  {task + "\n" + dispatched + "\n" + act(`"action":"asked","text":"`+strings.Repeat("x", shadow.MaxText+1)+`"`), "is the question"},
	} {
		t.Run(name, func(t *testing.T) {
			tasks, err := shadow.Parse(strings.NewReader(c.file))
			if !errors.Is(err, shadow.ErrRefused) || !strings.Contains(err.Error(), c.want) || tasks != nil {
				t.Fatalf("%v (tasks %v), want refused with %q", err, tasks, c.want)
			}
		})
	}
}

// A credential prefix counts only at the start of the text or after a character that is not a
// letter or digit, so ordinary hyphenated words are not taken for tokens.
func TestCredentialsAreTokensNotWords(t *testing.T) {
	withGoal := func(goal string) string {
		return strings.Replace(task, "fix it", goal, 1) + "\n" + dispatched
	}
	for _, goal := range []string{
		"fix the flaky task-runner-integration-tests",
		"desk-booking-calendar-sync",
		"disk-usage-reporting-job",
		"rotate the akiaxxxxxxxxxxxxxxxx placeholder",
	} {
		if _, err := shadow.Parse(strings.NewReader(withGoal(goal))); err != nil {
			t.Errorf("goal %q: %v, want accepted", goal, err)
		}
	}
	for name, token := range map[string]string{
		"an sk- token":   "s" + "k-" + strings.Repeat("a1", 12),
		"an lcap_ token": "lc" + "ap_" + strings.Repeat("b2", 12),
		"an AKIA key id": "AK" + "IA" + strings.Repeat("Q7", 8),
		"a ghp_ token":   fakeToken,
		"a quoted ghp_":  "(" + fakeToken + ")",
	} {
		for _, goal := range []string{token, "use " + token + " here"} {
			_, err := shadow.Parse(strings.NewReader(withGoal(goal)))
			if !errors.Is(err, shadow.ErrRefused) || !strings.Contains(err.Error(), "credential") {
				t.Errorf("%s in %q: %v, want refused as a credential", name, goal, err)
			}
		}
	}
}

func TestTaskRefusalsNameTheLine(t *testing.T) {
	second := strings.ReplaceAll(task, "t-1", "t-2")
	act2 := func(rest string) string { return strings.Replace(act(rest), "t-1", "t-2", 1) }
	for name, c := range map[string]struct {
		file, want string
	}{
		"a bad posture": {task + "\n" + dispatched + "\n\n" + strings.Replace(second, `"green"`, `"yolo"`, 1) + "\n" + act2(`"action":"dispatched"`), "line 4: "},
		"an unknown action deep in the file": {task + "\n" + second + "\n" + dispatched + "\n" + act2(`"action":"dispatched"`) + "\n" +
			act(`"action":"asked","text":"which region?"`) + "\n" + act2(`"action":"rebooted"`) + "\n" + act(`"action":"merged"`), "line 6: "},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := shadow.Parse(strings.NewReader(c.file))
			if !errors.Is(err, shadow.ErrRefused) || !strings.HasPrefix(err.Error(), c.want) || !strings.Contains(err.Error(), `task "t-2"`) {
				t.Fatalf("%v, want refused at %q for task t-2", err, c.want)
			}
		})
	}
}

func TestAFileOverTheLimitIsRefused(t *testing.T) {
	big := strings.Repeat(task+"\n", shadow.MaxFileBytes/len(task)+2)
	if _, err := shadow.Parse(strings.NewReader(big)); !errors.Is(err, shadow.ErrRefused) || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("%v", err)
	}
}
