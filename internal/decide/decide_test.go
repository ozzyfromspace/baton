package decide

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                         "plain",
		"  two\n lines\t\tand tabs ":    "two lines and tabs",
		`say "hi"`:                      "say 'hi'",
		`C:\path\to`:                    "C:/path/to",
		"bell\x07 and nul\x00":          "bell and nul",
		"em — dash, ünïcode, 🎺 trumpet": "em — dash, ünïcode, 🎺 trumpet",
	} {
		got := Sanitize(in)
		if got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
		if quoted := fmt.Sprintf("%q", got); quoted != `"`+got+`"` {
			t.Errorf("%q still needs escaping: %s", got, quoted)
		}
	}
}

// The deadline a question names is never earlier than the timeout, and baton never goes ahead before it.
func TestDeadlineRoundsUpToTheMinute(t *testing.T) {
	at := func(s string) time.Time { tm, _ := time.Parse(time.TimeOnly, s); return tm }
	for _, c := range []struct {
		now     string
		timeout time.Duration
		want    string
	}{
		{"15:28:35", 5 * time.Minute, "15:34:00"},
		{"15:28:00", 5 * time.Minute, "15:33:00"},
		{"15:28:59", 10 * time.Second, "15:30:00"},
		{"23:58:30", 2 * time.Minute, "00:01:00"},
	} {
		got := Deadline(at(c.now), c.timeout)
		if got.Format(time.TimeOnly) != c.want {
			t.Errorf("%s + %s: %s, want %s", c.now, c.timeout, got.Format(time.TimeOnly), c.want)
		}
	}
}

func TestProposalQuestions(t *testing.T) {
	deadline := time.Date(2026, 10, 5, 15, 33, 0, 0, time.Local)
	q := ProposalQuestion("gpg signing times out after the restart.", "Commit P4 unsigned", deadline)
	if want := "baton: gpg signing times out after the restart. Unless you answer by 15:33, I will commit P4 unsigned."; q.Text != want {
		t.Errorf("timed:\n%s\nwant\n%s", q.Text, want)
	}
	q = UntimedQuestion("the release needs its tag", "PR the fix, then push the tag v1.2", UntimedReason("push"))
	if want := "baton: the release needs its tag. I propose to PR the fix, then push the tag v1.2, and will not do it without you ('push' looks outward-facing or irreversible)."; q.Text != want {
		t.Errorf("untimed:\n%s\nwant\n%s", q.Text, want)
	}
	// Go ahead is option 3: the key baton types when nobody answers, and "No" in a permission prompt.
	if len(q.Options) != 3 || q.Options[0].Label != "Wait for me" || q.Options[1].Label != "Pause baton" || q.Options[2].Label != "Go ahead" {
		t.Errorf("options %+v", q.Options)
	}
	call := q.Call()
	for _, want := range []string{`exactly one question, "baton: the release needs`, `(header "baton")`, `exactly 3 options, in this order: "Wait for me" (description:`, "not multi-select"} {
		if !strings.Contains(call, want) {
			t.Errorf("call lacks %q:\n%s", want, call)
		}
	}
}

func TestOutward(t *testing.T) {
	for action, want := range map[string]string{
		"commit P4 unsigned; re-sign later with git rebase --exec 'git commit --amend --no-edit -S'": "",
		"skip the flaky e2e test and file an issue":                                                  "",
		"use the payload fixture from the last run":                                                  "",
		"write the dropdown component without the animation":                                         "",
		"push the branch":                     "push",
		"Force-push the rebased branch":       "force-push",
		"git push --force-with-lease":         "push",
		"publish 1.2.0 to npm":                "publish",
		"deploy to staging":                   "deploy",
		"tag and release v0.2.0":              "release",
		"merge the PR":                        "merge",
		"delete the stale fixtures":           "delete",
		"drop the users table":                "drop",
		"rm -rf build and rebuild":            "rm",
		"git reset --hard origin/main":        "reset --hard",
		"run git clean -fdx":                  "git clean",
		"send the report to the team":         "send",
		"email the client":                    "email",
		"pay the invoice":                     "pay",
		"terraform apply the plan":            "terraform apply",
		"kubectl rollout restart":             "kubectl",
		"run the migration on production":     "production",
		"overwrite the lockfile with --force": "--force",
		"upload the build artifact":           "upload",
	} {
		term, out := Outward(action)
		if term != want || out != (want != "") {
			t.Errorf("%q: %q %v, want %q", action, term, out, want)
		}
	}
}

func TestCapReached(t *testing.T) {
	for _, c := range []struct {
		n, max int
		want   bool
	}{{4, 5, false}, {5, 5, true}, {6, 5, true}, {100, 0, false}, {1, 1, true}} {
		if got := CapReached(c.n, c.max); got != c.want {
			t.Errorf("%d of %d: %v", c.n, c.max, got)
		}
	}
}

// Every text that depends on the project says what fits it, and without git none asks for a commit.
func TestWords(t *testing.T) {
	texts := func(w Words) map[string]string {
		return map[string]string{
			"playbook": w.Playbook(), "reporting": w.Reporting(), "no status": w.NoStatus("P4", "P4 (Ship)"),
			"question refused": w.QuestionRefused(""), "question refused, one issued": w.QuestionRefused("call the AskUserQuestion tool with …"),
			"finish step": w.FinishStep(), "closing": w.Closing("P4"),
		}
	}
	for name, text := range texts(Words{Git: false, Timeout: time.Minute}) {
		if strings.Contains(strings.ToLower(text), "commit") || strings.Contains(strings.ToLower(text), "git init") {
			t.Errorf("%s without git asks for a commit:\n%s", name, text)
		}
	}
	git := texts(Words{Git: true, Timeout: 5 * time.Minute})
	for name, wants := range map[string][]string{
		"playbook": {"commit it, degraded if need be (e.g. --no-gpg-sign)", "never discard, stash, reset or unstage work", "baton note", "baton propose",
			"a deadline of 5 minutes", "baton blocked \"<why>\" --tried", "Waiting, doing nothing or stopping is not a proposal"},
		"reporting": {"complete and committed", "baton note", "baton propose", "baton blocked \"<why>\" --tried", "a deadline of 5 minutes",
			"waiting, doing nothing or stopping is not a proposal", "One proposal covers the case in front of you", "`baton note` it each time", "(work committed)"},
		"no status":        {"(the phase is finished and committed)", "baton propose", "--tried", "baton waiting", "`baton note` it and carry on"},
		"question refused": {"baton note", "baton propose", "a deadline of 5 minutes", "--tried"},
		"finish step":      {"and commit it"},
		"closing":          {"When P4 is complete and committed", "baton note", "baton propose", "baton blocked --tried"},
	} {
		for _, want := range wants {
			if !strings.Contains(git[name], want) {
				t.Errorf("%s lacks %q:\n%s", name, want, git[name])
			}
		}
	}
	if plain := (Words{Timeout: time.Minute}).Playbook(); !strings.Contains(plain, "a deadline of 1 minute") {
		t.Errorf("playbook without git:\n%s", plain)
	}
}

// The skill teaches what baton enforces: the three ways to reach the human, and nothing about creating
// a repository. (Its instructions live in the binary, which the plugin's SKILL.md loads.)
func TestTheSkillNamesTheThreeTiers(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "skill", "skill.md"))
	if err != nil {
		t.Fatal(err)
	}
	skill := string(b)
	for _, want := range []string{
		"`baton note \"<what you did>\" --undo \"<how to reverse it>\"`",
		"`baton propose \"<what you will do>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\"`",
		"`baton blocked \"<why>\" --tried \"<what you tried>\"`",
		"waiting, doing nothing or stopping is not a proposal",
		"One proposal covers the case in front of you; when the same thing happens again, `baton note` it each time.",
		"committed (in a git repository; otherwise saved)",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("the skill lacks %q", want)
		}
	}
	for _, never := range []string{"git init", "create a repository", "initialize a repository"} {
		if strings.Contains(strings.ToLower(skill), never) {
			t.Errorf("the skill suggests %q", never)
		}
	}
}
