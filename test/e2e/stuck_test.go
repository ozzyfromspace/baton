//go:build !windows

package e2e

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/host"
)

// v0.8.4: no edge case leaves a run stuck. Each scenario leaves one of baton's questions unanswered, or
// takes something away from the run (an agent the human stops, a host that dies), and the run goes on.

// proposePush has the model propose an outward act, a push, then follow baton.
const proposePush = `Before anything else, run exactly this one command with the Bash tool: ` +
	`baton propose "push the main branch to the shared remote with git push" --because "the plan does not say whether to publish the work" --undo "delete the pushed branch from the remote" ` +
	`— then do exactly what baton tells you, and go on with the plan.`

// Nobody answers an outward proposal: at its deadline baton answers Not now for the human, which leaves
// the push for them (it is neither reversible nor inside the project), and the run goes on to its end.
// Where baton's mod runs, the mod settles it; where it does not, the host types 3, Not now's key.
func TestAnOutwardProposalIsLeftAtItsDeadline(t *testing.T) {
	for _, c := range []struct {
		name string
		mod  bool
	}{{"the mod settles it", true}, {"with no mod, the host types its answer", false}} {
		t.Run(c.name, func(t *testing.T) {
			dir := NewProject(t)
			Attach(t, dir, twoPhasePlan)
			// The push is refused without a prompt, should the model try it: a permission prompt would hold
			// the run for the human.
			// The prompt comes before the rules: a flag of several values would take it for one of them.
			args := []string{"--model", "haiku", proposePush, "--permission-mode", "acceptEdits", "--allowedTools", "Bash(echo:*)", "--disallowedTools", "Bash(git push:*)"}
			steps := []func(map[string]any) bool{
				func(e map[string]any) bool { return e["kind"] == "proposed" && e["id"] == "d1" && e["outward"] != nil },
				is("proposal_asked", "id", "d1"),
			}
			if !c.mod {
				args = append(args, "--plugin-dir", noModPlugin(t))
				steps = append(steps, is("auto_answered", "dialog", "proposal", "key", "3"))
			}
			steps = append(steps,
				is("proposal_resolved", "id", "d1", "by", "timeout", "answer", "Not now"),
				is("phase_done", "phase", "P0"),
				kind("plan_complete"),
			)
			s := StartEnv(t, dir, []string{"BATON_ESCALATION_TIMEOUT=20s"}, args...)
			s.Trust()
			// Haiku may look the repository over once it is told Not now; a human approves those commands.
			s.ApprovePermissions()
			waitSequence(t, s, 10*time.Minute, steps...)
			ev := s.Events()
			if first(ev, is("notified", "notice", "not_now")) < 0 {
				t.Errorf("the Not now was not pushed:\n%s", eventLog(s))
			}
			// A Not now is no decision made without the human: the plan completes with none.
			if i := first(ev, kind("plan_complete")); ev[i]["decisions"] != nil && ev[i]["decisions"] != 0.0 {
				t.Errorf("the Not now counted as a decision: %v", ev[i])
			}
			none(t, s, "review_due", "controller_panic")
		})
	}
}

// greetingPlan's first phase needs a greeting the plan does not give.
const greetingPlan = `# Greeting plan

## P0 — Write the greeting
Write the greeting into a new file named greeting.txt with the Write tool. Then report P0 done: baton done P0 --notes 'greeted'

## P1 — Say goodbye
Run echo goodbye.
`

// The model blocks, and nobody answers. Once nobody has written for BlockLook (a minute here, 30 for
// real), baton lifts the block, Look again, and the model looks for a safe way around what blocked it,
// which it proposes before it takes it; nobody answers that either, so it goes ahead at its deadline.
func TestABlockNobodyAnswersIsLookedAtAgain(t *testing.T) {
	dir := NewPlainProject(t) // nothing to commit, so no git command waits on a permission prompt
	Attach(t, dir, greetingPlan)
	s := StartEnv(t, dir, []string{"BATON_BLOCK_LOOK=1m", "BATON_ESCALATION_TIMEOUT=20s"},
		"--model", "haiku",
		`This phase needs a decision only the human can make. Run exactly: baton blocked "need the human to pick the greeting for greeting.txt" --tried "the plan does not say which greeting to write" — then end your turn and follow baton's instructions.`,
		"--permission-mode", "acceptEdits", "--allowedTools", "Bash(echo:*)")
	s.Trust()
	// The model may give its proposal a wait of its own, 5 minutes at least.
	waitSequence(t, s, 15*time.Minute,
		is("escalated", "type", "blocked"),
		is("block_lapsed", "by", "timeout"),
		kind("proposed"),
		is("proposal_resolved", "by", "timeout", "answer", "Go ahead"),
		is("phase_done", "phase", "P0"),
	)
	ev := s.Events()
	blocked, lapsed := first(ev, is("escalated", "type", "blocked")), first(ev, is("block_lapsed"))
	if d := eventTime(ev[lapsed]).Sub(eventTime(ev[blocked])); d < time.Minute {
		t.Errorf("the block was lifted %s after it began, sooner than BlockLook", d)
	}
	if _, err := os.Stat(filepath.Join(dir, "greeting.txt")); err != nil {
		t.Errorf("the model did not take the way it proposed: %v", err)
	}
	none(t, s, "controller_panic")
}

// askGreeting has the model ask the human a question of its own.
const askGreeting = `Before anything else, ask the human exactly one question with the AskUserQuestion tool: ` +
	`the question "Which greeting should greeting.txt hold?", the header "Greeting", and two options: ` +
	`"hello" (description "the usual greeting") and "hi (Recommended)" (description "a short one"). ` +
	`Then write the greeting chosen, without the word Recommended, into greeting.txt with the Write tool, and go on with the plan.`

// The model asks the human a question of its own while the plan runs, and nobody answers. Where baton's
// mod runs, the mod answers it at escalation_timeout (never sooner than a minute after it was shown) with
// its recommended option, the model does what that says, and the run goes on.
func TestTheModelsOwnQuestionIsAnsweredAtItsDeadline(t *testing.T) {
	dir := NewPlainProject(t) // nothing to commit, so no git command waits on a permission prompt
	Attach(t, dir, twoPhasePlan)
	s := StartEnv(t, dir, []string{"BATON_ESCALATION_TIMEOUT=20s"},
		"--model", "haiku", askGreeting, "--permission-mode", "acceptEdits", "--allowedTools", "Bash(echo:*)")
	s.Trust()
	waitSequence(t, s, 8*time.Minute,
		is("question_answered", "own", "true", "by", "timeout", "answer", "hi (Recommended)"),
		is("phase_done", "phase", "P0"),
	)
	ev := s.Events()
	answered := ev[first(ev, is("question_answered", "own", "true"))]
	if after, err := time.ParseDuration(fmt.Sprint(answered["after"])); err != nil || after < time.Minute {
		t.Errorf("answered %v after it was shown, sooner than a minute", answered["after"])
	}
	if got := strings.TrimSpace(readFile(dir, "greeting.txt")); got != "hi" {
		t.Errorf("greeting.txt: %q, want the recommended greeting", got)
	}
	if first(ev, is("notified", "notice", "answered")) < 0 {
		t.Errorf("what baton answered was not pushed:\n%s", eventLog(s))
	}
	none(t, s, "question_refused", "controller_panic")
}

// stopPlan: the lead's P0 is quick; the phase agent's P1 runs slow.sh in the foreground, during which the
// human stops it.
const stopPlan = `# Stop plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Take a while
After: none

1. Run exactly this command with the Bash tool: sh slow.sh — it takes about half a minute, and says when it is done.
2. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'. Then report P1 done: baton done P1 --notes 'slow.sh ran'

## P2 — Say two
After: P0, P1

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// StopAgents stops every background agent of the session as a person does: ctrl+x ctrl+k at the prompt,
// which asks to be pressed again, then again (spike 36).
func (s *Session) StopAgents() {
	for i := 0; i < 2; i++ {
		s.pty.Write([]byte("\x18"))
		time.Sleep(150 * time.Millisecond)
		s.pty.Write([]byte("\x0b"))
		time.Sleep(time.Second)
	}
}

// The human stops the phase agent on P1 while it works, then walks away. baton asks them whether to
// restart it, and nobody answers: at escalation_timeout baton decides from the record. The agent's work
// is not done, so a fresh agent takes P1 up in its worktree, and the human is told (a second question,
// which nobody answers either). P1's work lands, and the plan completes.
func TestAStoppedAgentIsRestartedAtTheDeadline(t *testing.T) {
	dir := projectWith(t, map[string]string{"slow.sh": slowScript})
	Attach(t, dir, stopPlan)
	s := StartAgents(t, dir, []string{"BATON_ESCALATION_TIMEOUT=20s"},
		"--allowedTools", "Bash(git add:*)", "Bash(git commit:*)", "Bash(echo:*)", "Bash(sh slow.sh)", "Write")
	if !s.Until("slow.sh to start", 5*time.Minute, func() bool { return readFile(filepath.Join(dir, ".git"), "slow-runs") != "" }) {
		t.Fatalf("slow.sh never ran:\n%s", eventLog(s))
	}
	if s.WaitEvent("worker_started", time.Minute) == nil {
		t.Fatalf("no phase agent at work:\n%s", eventLog(s))
	}
	time.Sleep(3 * time.Second)
	s.StopAgents()
	waitSequence(t, s, 8*time.Minute,
		is("agent_stopped", "phase", "P1", "stops", "1"),
		is("relay_issued", "phase", "P1", "type", "stopped"),
		is("relay_answered", "phase", "P1", "type", "stopped", "by", "timeout"),
		is("agent_restarted", "phase", "P1", "by", "timeout"),
		is("worker_started", "phase", "P1"),
		is("landed", "phase", "P1"),
		kind("plan_complete"),
	)
	ev := s.Events()
	if n := count(ev, is("worker_started", "phase", "P1")); n != 2 {
		t.Errorf("%d agents at work on P1, want the first and the one baton restarted", n)
	}
	asked, answered := first(ev, is("agent_restart_asked", "phase", "P1")), first(ev, is("relay_answered", "type", "stopped"))
	if d := eventTime(ev[answered]).Sub(eventTime(ev[asked])); d < 20*time.Second {
		t.Errorf("decided %s after it was asked, before escalation_timeout", d)
	}
	if first(ev, is("relay_issued", "phase", "P1", "type", "restarted")) < 0 {
		t.Errorf("the human was not told baton restarted the agent:\n%s", eventLog(s))
	}
	if got := strings.TrimSpace(readFile(dir, "one.txt")); got != "one" {
		t.Errorf("one.txt in the checkout: %q", got)
	}
	none(t, s, "agent_taken_down", "controller_panic")
}

// newSessionID is a fresh session id for claude's --session-id.
func newSessionID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// killHost kills a baton host and its claude at once, as a crash would: nothing of the host's runs after.
func killHost(t *testing.T, s *Session) {
	t.Helper()
	claude, _ := claudeOf(t, s.cmd.Process.Pid)
	s.cmd.Process.Kill()
	<-s.exited
	if claude != 0 {
		syscall.Kill(claude, syscall.SIGKILL)
	}
}

// A claude that only inherited baton's environment, with no baton host above it, as when Claude Code
// moves a conversation to its background service: BATON_HOST=1, and the BATON_INSTANCE and BATON_DIR of
// a host that died (killed, so its run is still linked to it). baton leaves it alone: its hooks bind no
// run, claim none and move no link, its mod is not answered, and the human is told once that baton does
// not host it, which the dead host's run records.
func TestAnInheritedSessionIsDormant(t *testing.T) {
	dir := NewProject(t)
	Attach(t, dir, twoPhasePlan)
	lead := Start(t, dir, "--model", "haiku", "Run the plan's phase P0 now, exactly as it says.", "--allowedTools", "Bash(echo:*)")
	lead.Trust()
	started := lead.WaitEvent("host_started", time.Minute)
	if started == nil || lead.WaitEvent("turn_started", 2*time.Minute) == nil {
		t.Fatalf("the host never started its run:\n%s", eventLog(lead))
	}
	inst := fmt.Sprint(started["instance"])
	killHost(t, lead)
	time.Sleep(2 * time.Second) // a hook its claude started just before may still be writing
	run := ""
	if logs, _ := filepath.Glob(filepath.Join(dir, ".baton", "runs", "*", "events.jsonl")); len(logs) == 1 {
		run = filepath.Base(filepath.Dir(logs[0]))
	}
	link := func(sub, name string) string {
		return strings.TrimSpace(readFile(filepath.Join(dir, ".baton", sub), name))
	}
	if run == "" || link("hosts", inst) != run {
		t.Fatalf("the dead host's run %q is not linked to it (%q)", run, link("hosts", inst))
	}
	killed := time.Now()

	// Its claude carries what the host started claude with (baton's hooks, in --settings) and the
	// environment, but it is no child of a baton host.
	sid := newSessionID(t)
	bg := StartProgram(t, dir, []string{"BATON_HOST=1", "BATON_INSTANCE=" + inst, "BATON_DIR=" + filepath.Join(dir, ".baton"), "BATON_BIN=" + batonBin, "BATON_WORKERS=auto"},
		"claude", "--settings", host.SettingsJSON(batonBin), "--plugin-dir", pluginDir(t), "--session-id", sid, "--model", "haiku", "--permission-mode", "acceptEdits",
		"Write the single word ok into a new file named ok.txt with the Write tool, then end your turn.")
	bg.Trust()
	if !bg.Until("ok.txt", 3*time.Minute, func() bool { return strings.TrimSpace(readFile(dir, "ok.txt")) == "ok" }) {
		t.Fatal("the inherited session did not do what it was asked")
	}
	bg.WaitQuiet(5*time.Second, time.Minute)

	ev := lead.Events()
	if n := count(ev, is("inherited_notice", "session", sid)); n != 1 {
		t.Errorf("%d notices that baton does not host the session, want 1:\n%s", n, eventLog(lead))
	}
	for _, e := range ev {
		if eventTime(e).After(killed) && e["kind"] != "inherited_notice" {
			t.Errorf("the run changed after its host died: %v", e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".baton", "inherited", sid)); err != nil {
		t.Errorf("no mark that the session was told: %v", err)
	}
	for _, p := range []string{filepath.Join("runs", sid), filepath.Join("sessions", sid)} {
		if _, err := os.Stat(filepath.Join(dir, ".baton", p)); err == nil {
			t.Errorf("the inherited session was given %s", p)
		}
	}
	if link("hosts", inst) != run {
		t.Errorf("the dead host's link moved to %q", link("hosts", inst))
	}
}

// carryPlan: the phase agent's P1 commits, then waits on hold.sh, which the test releases; the lead's P0
// is quick.
const carryPlan = `# Carry plan

## P0 — Say zero
After: none

Run echo zero, then report P0 done: baton done P0 --notes 'said zero'

## P1 — Write one, then wait
After: none

1. Write the single word one into a new file named one.txt. Commit it: git add one.txt, then git commit -m 'feat: one'.
2. Hand baton this wait, exactly: baton await --notes 'one.txt committed; report P1 done once the wait ends' -- sh hold.sh
3. End your turn. baton gives you the result when the wait ends: do not run hold.sh yourself, and do not wait for it or check on it.
4. When baton gives you the result, report P1 done: baton done P1 --notes 'wrote one'

## P2 — Say two
After: P0, P1

Run echo two, then report P2 done: baton done P2 --notes 'said two'
`

// A host dies with its run under way and a phase agent waiting on a job. Another session, with no plan of
// its own, runs the same plan: it carries the dead host's run on where it stood (run_carried) rather than
// starting it again, and since the agent died with the old claude, a fresh one takes P1 up in its
// worktree. The job's result reaches it, P1's work lands, and the plan completes.
func TestACarriedRunTakesItsAgentsUp(t *testing.T) {
	dir := listPlanProject(t)
	Attach(t, dir, carryPlan)
	first1 := StartAgents(t, dir, nil, workerTools...)
	first1.AutoApprove()
	if !first1.Until("the phase agent's wait to start", 5*time.Minute, func() bool { return first(first1.WorkerEvents("P1"), is("job_started")) >= 0 }) {
		t.Fatalf("the phase agent never handed baton its wait:\n%s", eventLog(first1))
	}
	if !first1.Until("P0 to be done", 3*time.Minute, func() bool { return first(first1.Events(), is("phase_done", "phase", "P0")) >= 0 }) {
		t.Fatalf("the lead never finished P0:\n%s", eventLog(first1))
	}
	killHost(t, first1)
	time.Sleep(ownerTTL) // its claim on the run lapses

	next := StartEnv(t, dir, []string{"BATON_WORKERS=1"}, append([]string{"--model", "haiku"}, workerTools...)...)
	next.Trust()
	next.AutoApprove()
	next.WaitQuiet(3*time.Second, 40*time.Second)
	next.Type("/baton run plan.md")
	waitSequence(t, next, 5*time.Minute,
		kind("run_carried"),
		is("worker_started", "phase", "P1"),
	)
	ev := next.Events()
	carried := first(ev, kind("run_carried"))
	if fresh := first(ev[carried:], is("worker_started", "phase", "P1")); fresh < 0 {
		t.Fatalf("no fresh agent on P1 after the carry:\n%s", eventLog(next))
	}
	release(t, dir)
	if !next.Until("the plan to complete", 6*time.Minute, func() bool { return first(next.Events(), is("plan_complete")) >= 0 }) {
		t.Fatalf("the carried run did not complete:\n%s", eventLog(next))
	}
	ev = next.Events()
	if first(ev, is("landed", "phase", "P1")) < 0 {
		t.Errorf("P1 did not land:\n%s", eventLog(next))
	}
	if n := count(ev, kind("attached")); n != 1 {
		t.Errorf("the plan was attached %d times, want once (the carry attaches nothing)", n)
	}
	if got := strings.TrimSpace(readFile(dir, "one.txt")); got != "one" {
		t.Errorf("one.txt in the checkout: %q", got)
	}
	none(t, next, "controller_panic")
}
