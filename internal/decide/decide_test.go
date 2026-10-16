package decide

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
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
// A plan that would not attach is turned back with each problem on a line of its own, at most ten.
func TestPlanUnbuiltListsItsProblems(t *testing.T) {
	got := PlanUnbuilt([]string{"phase 2 (\"P0\"): duplicate id"})
	if !strings.Contains(got, "as written, so the human's approval would not start it:\n\n- phase 2 (\"P0\"): duplicate id\n\nFix the plan") {
		t.Errorf("one problem: %q", got)
	}
	var many []string
	for range 13 {
		many = append(many, "a problem")
	}
	if got := PlanUnbuilt(many); strings.Count(got, "\n- a problem") != 10 || !strings.Contains(got, "\n- and 3 more\n") {
		t.Errorf("thirteen problems: %q", got)
	}
	if got := PlanUnbuiltSaid("x"); got != "baton: the plan would not attach as written (x) → Claude fixes it before you see it" {
		t.Errorf("said %q", got)
	}
}

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

// Every question of baton's that has a default keeps it third, where the host's 3 lands in serial mode,
// with Wait for me second in the questions about the run and about a plan approved mid-run.
func TestEachDefaultIsThird(t *testing.T) {
	for _, c := range []struct {
		name string
		opts []Option
		dflt string
	}{
		{"escalation", EscalationOptions, "Continue"},
		{"revised plan", ReviseOptions, PlanQuestionDefault(true)},
		{"another plan", ReplaceOptions, PlanQuestionDefault(false)},
		{"context", WarnOptions, "Keep going"},
		{"proposal", ProposalOptions, "Go ahead"},
	} {
		if len(c.opts) != 3 || c.opts[2].Label != c.dflt {
			t.Errorf("%s: options %+v, default %q", c.name, c.opts, c.dflt)
		}
	}
	if EscalationOptions[1].Label != "Wait for me" {
		t.Errorf("options %+v: Wait for me is not second", EscalationOptions)
	}
	if PlanQuestionDefault(true) != "Continue with the revised plan" || PlanQuestionDefault(false) != "Keep the current run" {
		t.Errorf("plan question defaults %q, %q", PlanQuestionDefault(true), PlanQuestionDefault(false))
	}
}

// The question about a plan approved mid-run puts Wait for me first, where a stray Enter lands, and Start
// the new plan from P0, which sets the run's progress aside, second: no single key discards a run.
func TestPlanQuestionOrder(t *testing.T) {
	for _, c := range []struct {
		name string
		same bool
		want []string
	}{
		{"revised plan", true, []string{"Wait for me", "Start the new plan from P0", "Continue with the revised plan"}},
		{"another plan", false, []string{"Wait for me", "Start the new plan from P0", "Keep the current run"}},
	} {
		q := PlanQuestion("Demo", "P1", "running", c.same)
		var got []string
		for _, o := range q.Options {
			got = append(got, o.Label)
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: options %q, want %q", c.name, got, c.want)
		}
		if got[2] != PlanQuestionDefault(c.same) {
			t.Errorf("%s: the default %q is not third", c.name, PlanQuestionDefault(c.same))
		}
		if !strings.Contains(q.Call(), `exactly 3 options, in this order: "Wait for me"`) {
			t.Errorf("%s: the call does not put Wait for me first: %s", c.name, q.Call())
		}
	}
}

func TestProposalQuestions(t *testing.T) {
	deadline := time.Date(2026, 10, 5, 15, 33, 0, 0, time.Local)
	q := ProposalQuestion("gpg signing times out after the restart.", "Commit P4 unsigned", deadline)
	if want := "baton: gpg signing times out after the restart. Unless you answer by 15:33, I will commit P4 unsigned."; q.Text != want {
		t.Errorf("timed:\n%s\nwant\n%s", q.Text, want)
	}
	// Go ahead is option 3: the key baton types when nobody answers, and "No" in a permission prompt.
	if len(q.Options) != 3 || q.Options[0].Label != "Wait for me" || q.Options[1].Label != "Pause baton" || q.Options[2].Label != "Go ahead" {
		t.Errorf("options %+v", q.Options)
	}
	// An outward one says what baton answers at its deadline, and Not now is option 3 in its stead: Go
	// ahead, only the human's to give, comes last.
	q = OutwardQuestion("the release needs its tag", "PR the fix, then push the tag v1.2", OutwardReason("push"), deadline)
	if want := "baton: the release needs its tag. I propose to PR the fix, then push the tag v1.2, but 'push' looks outward-facing or irreversible: " +
		"unless you answer by 15:33, I will do it only if it is safely reversible and stays in the project, and leave it for you otherwise."; q.Text != want {
		t.Errorf("outward:\n%s\nwant\n%s", q.Text, want)
	}
	if len(q.Options) != 4 || q.Options[0].Label != "Wait for me" || q.Options[1].Label != "Pause baton" || q.Options[2].Label != state.NotNow || q.Options[3].Label != "Go ahead" {
		t.Errorf("options %+v", q.Options)
	}
	call := q.Call()
	for _, want := range []string{`exactly one question, "baton: the release needs`, `(header "baton")`, `exactly 4 options, in this order: "Wait for me" (description:`, "not multi-select"} {
		if !strings.Contains(call, want) {
			t.Errorf("call lacks %q:\n%s", want, call)
		}
	}
	p := state.Decision{ID: "d3", Question: q.Text, Outward: OutwardReason("push"), Deadline: deadline}
	if got := AskFirst(p); !strings.Contains(got, "put your proposal d3 to the human first") || !strings.Contains(got, `"Not now" (description:`) {
		t.Errorf("ask first: %s", got)
	}
	if got := ProposalDismissed(p); !strings.Contains(got, "baton answers Not now for you (Claude does it only if it is safely reversible and stays in the project) at 15:33") {
		t.Errorf("dismissed: %s", got)
	}
	p.Outward = ""
	if got := ProposalDismissed(p); !strings.Contains(got, "baton goes ahead with it at 15:33") {
		t.Errorf("dismissed: %s", got)
	}
}

// Not now, baton's at the deadline or the human's own, tells the model the safe kind only, and to note it.
func TestNotNow(t *testing.T) {
	deadline := time.Date(2026, 10, 5, 15, 33, 0, 0, time.Local)
	for _, c := range []struct {
		name      string
		say, tell string
		says      []string
		tells     []string
	}{
		{"the lead's, at its deadline", "", "", []string{"nobody answered by 15:33, so baton answered Not now for you on d3", "Claude does it only if it is safely reversible", "push the tag"},
			[]string{"Nobody answered your proposal d3 (push the tag) by 15:33, so baton answered Not now for the human", "baton note", "do not: leave it undone", "carry on with P2"}},
		{"the lead's, the human's", "", "", []string{"you chose Not now on d3"}, []string{"The human chose Not now on your proposal d3 (push the tag)", "baton note", "carry on with P2"}},
		{"an agent's, at its deadline", "", "", []string{"on the proposal P3.d1 of the agent on P3", "the agent does it only if"},
			[]string{"Nobody answered your proposal P3.d1 (push the tag) by 15:33", "carry on with P3."}},
	} {
		switch c.name {
		case "the lead's, at its deadline":
			c.say, c.tell = AnsweredNotNow("d3", "push the tag", "P2", deadline)
		case "the lead's, the human's":
			c.say, c.tell = AnsweredNotNow("d3", "push the tag", "P2", time.Time{})
		default:
			c.say, c.tell = RelayNotNow("P3.d1", "push the tag", "P3", deadline)
		}
		for _, w := range c.says {
			if !strings.Contains(c.say, w) {
				t.Errorf("%s: say %q lacks %q", c.name, c.say, w)
			}
		}
		for _, w := range c.tells {
			if !strings.Contains(c.tell, w) {
				t.Errorf("%s: tell %q lacks %q", c.name, c.tell, w)
			}
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
			"finish step": w.FinishStep(), "closing": w.Closing("P4"), "background stop": w.BackgroundStop("npm test"),
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
			"a deadline of 5 minutes", "baton blocked \"<why>\" --tried", "Waiting, doing nothing or stopping is not a proposal",
			"for an outward or irreversible act, that you do it only if it is safely reversible", "if nobody answers in 30 minutes, baton lifts the block, and you look again"},
		"reporting": {"complete and committed", "baton note", "baton propose", "baton blocked \"<why>\" --tried", "a deadline of 5 minutes",
			"waiting, doing nothing or stopping is not a proposal", "One proposal covers the case in front of you", "`baton note` it each time", "(work committed)",
			"`baton verify \"<command>\" --notes", "run `baton done` even while its gate still runs", "moves to the background by itself and wakes you when it ends",
			"do not ask the human questions (AskUserQuestion) of your own", "A question baton gives you to put to the human is the exception: ask it as given.",
			"for an outward or irreversible act, that you do it only if it is safely reversible", "if nobody answers in 30 minutes, baton lifts the block, and you look again"},
		"no status":        {"(the phase is finished and committed, even while a job you handed to baton still runs)", "baton propose", "--tried", "`baton await \"<a command that ends when it is ready>\"", "`baton note` it and carry on"},
		"question refused": {"baton note", "baton propose", "a deadline of 5 minutes", "--tried"},
		"finish step":      {"and commit it"},
		"background stop": {"You stopped while your background work runs (npm test). Keep working on what does not depend on it",
			"invalidated by your own edits", "`baton verify \"<command>\" --notes", "If the phase is complete and committed, report it with `baton done`", "end your turn again"},
		"closing": {"When P4 is complete and committed", "baton note", "baton propose", "baton blocked --tried", "`baton verify \"<command>\"", "Quick checks run in place"},
	} {
		for _, want := range wants {
			if !strings.Contains(git[name], want) {
				t.Errorf("%s lacks %q:\n%s", name, want, git[name])
			}
		}
	}
	// A phase agent gives a job's command as its words after --: Claude Code refuses a command of an agent
	// isolated in a worktree that hands a program text starting with a shell or git.
	worker := texts(Words{Git: true, Worker: true, Timeout: 5 * time.Minute})
	verify := "`baton verify --notes \"<where the phase stands>\" -- <command>`"
	for name, wants := range map[string][]string{
		"reporting":       {verify, "Give a job's command as its words after `--`", "such as `baton verify \"sh check.sh\"`"},
		"closing":         {verify, "Give a job's command as its words after `--`"},
		"background stop": {verify},
		"no status":       {"`baton await --notes \"<where the phase stands>\" -- <a command that ends when it is ready>`"},
	} {
		for _, want := range wants {
			if !strings.Contains(worker[name], want) {
				t.Errorf("a phase agent's %s lacks %q:\n%s", name, want, worker[name])
			}
		}
		if strings.Contains(worker[name], `verify "<command>"`) || strings.Contains(worker[name], `await "<`) {
			t.Errorf("a phase agent's %s gives a job's command quoted:\n%s", name, worker[name])
		}
	}
	if plain := (Words{Timeout: time.Minute}).Closing("P4"); !strings.Contains(plain, "`baton await \"<command>\"") || strings.Contains(plain, "verify") {
		t.Errorf("closing without git:\n%s", plain)
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
		"`baton propose \"<what you will do>\" --because \"<what went wrong>\" --undo \"<how to reverse it>\" --wait 5m`",
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

// As a run starts, the human hears which workers an earlier run left behind baton ended, and what of
// theirs it kept.
func TestWorkersRetired(t *testing.T) {
	for _, c := range []struct {
		name string
		left []Left
		want string
	}{
		{"one, with nothing kept", []Left{{Phase: "P3"}}, "baton: ended 1 worker left by an earlier run (P3)"},
		{"two, one branch kept", []Left{{Phase: "P3", Branch: "baton/r1/P3", Unlanded: 1}, {Phase: "P4"}},
			"baton: ended 2 workers left by an earlier run (P3, P4); its branch baton/r1/P3 keeps 1 commit that never landed"},
		{"branches and a worktree kept", []Left{
			{Phase: "P1", Branch: "baton/r1/P1", Unlanded: 2, Worktree: "/h/worktrees/k/r1/P1", Kept: "its uncommitted work could not be saved: disk full"},
			{Phase: "P2", Branch: "baton/r1/P2", Unlanded: 1}},
			"baton: ended 2 workers left by an earlier run (P1, P2); its branch baton/r1/P1 keeps 2 commits that never landed; " +
				"its branch baton/r1/P2 keeps 1 commit that never landed; " +
				"the worktree of its worker on P1 stays at /h/worktrees/k/r1/P1, as its uncommitted work could not be saved: disk full"},
	} {
		if got := WorkersRetired(c.left); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// As a plan is attached, the lead hears how many workers will work beside it, and whether a permission
// prompt would stop them.
func TestWorkersAttached(t *testing.T) {
	const warn = "Workers work in this session's permission mode"
	for _, c := range []struct {
		name         string
		n            int
		auto         bool
		width        int
		mode         string
		want, absent []string
	}{
		{"workers off", 0, false, 4, "", nil, nil},
		{"a machine too small for one", 0, true, 4, "", []string{"no worker runs beside this session: with workers: auto, a machine needs 4 CPUs and 8 GiB of memory for one, " +
			"and this one has less. To run them anyway, set workers to a number in baton's config.json."}, []string{warn}},
		{"a chain", 2, false, 1, "default", []string{"no worker runs beside this session: each phase of this plan needs the one before it."}, []string{warn}},
		{"as many as the phases allow", 3, false, 3, "auto", []string{"up to 2 workers work beside this session", "(workers: 3)."}, []string{warn}},
		{"as many as configured", 1, false, 5, "bypassPermissions", []string{"up to 1 worker works beside this session"}, []string{warn}},
		{"auto: as many as the phases allow", 6, true, 4, "auto", []string{"up to 3 workers work beside this session on the phases that can go ahead, each in a worktree of its own, " +
			"and baton lands their work on this branch (the plan's widest step is 4 phases; this machine allows 6)."}, []string{warn}},
		{"auto: as many as the machine allows", 1, true, 5, "auto", []string{"up to 1 worker works beside this session", "(the plan's widest step is 5 phases; this machine allows 1)."}, nil},
		{"the default mode", 1, false, 2, "", []string{warn + " (default as it started), so a permission prompt stops a worker until you answer it, here in this terminal", "--permission-mode auto"},
			[]string{"baton view", "settings.local.json"}},
		{"acceptEdits", 1, false, 2, "acceptEdits", []string{warn + " (acceptEdits as it started)"}, nil},
		{"plan mode", 1, false, 2, "plan", []string{warn + " (plan as it started)"}, nil},
		{"no prompts", 1, false, 2, "dontAsk", []string{"up to 1 worker works beside this session"}, []string{warn}},
	} {
		got := WorkersAttached(c.n, c.auto, c.width, c.mode)
		if c.want == nil && got != "" {
			t.Errorf("%s: %q", c.name, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: no %q in %q", c.name, w, got)
			}
		}
		for _, a := range c.absent {
			if strings.Contains(got, a) {
				t.Errorf("%s: %q in %q", c.name, a, got)
			}
		}
	}
}

// The model's --wait is taken from 5 to 20 minutes; anything else falls back to escalation_timeout, with
// the reason, and nothing is refused.
func TestProposalWait(t *testing.T) {
	for _, c := range []struct {
		given string
		want  time.Duration
		why   string
	}{
		{"", 10 * time.Minute, ""},
		{"  ", 10 * time.Minute, ""},
		{"5m", 5 * time.Minute, ""},
		{"7m30s", 7*time.Minute + 30*time.Second, ""},
		{"20m", 20 * time.Minute, ""},
		{"299s", 10 * time.Minute, "--wait 299s is outside 5 minutes to 20 minutes"},
		{"1h", 10 * time.Minute, "--wait 1h is outside 5 minutes to 20 minutes"},
		{"10", 10 * time.Minute, `--wait "10" is not a duration such as 5m`},
	} {
		got, why := ProposalWait(c.given, 10*time.Minute)
		if got != c.want || why != c.why {
			t.Errorf("ProposalWait(%q) = %s, %q; want %s, %q", c.given, got, why, c.want, c.why)
		}
	}
}
