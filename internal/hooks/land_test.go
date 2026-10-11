package hooks

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx/gittest"
	"github.com/ozzyfromspace/baton/internal/state"
)

func call(in map[string]any, id string) map[string]any {
	in["tool_use_id"] = id
	return in
}

// inFlight lists the lead's tool calls on record, as id/agent.
func inFlight(f *fixture) []string {
	var out []string
	for _, c := range f.state().Run.Tools {
		out = append(out, strings.TrimSuffix(c.ID+"/"+c.Agent, "/"))
	}
	return out
}

func permit(out map[string]any) (string, string) {
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	d, _ := hso["permissionDecision"].(string)
	why, _ := hso["permissionDecisionReason"].(string)
	return d, why
}

// Every tool call of the lead's that runs is on record until it ends: by its result, the end of its turn
// or its subagent, or a new session. One baton holds back never runs, and is not on record.
func TestTheLeadsToolCallsAreRecordedUntilTheyEnd(t *testing.T) {
	f := newFixture(t, true)
	f.fire("PreToolUse", call(bash("make test"), "t1"))
	f.fire("PreToolUse", call(edit("Edit", "/x/a.go"), "t2"))
	f.fire("PreToolUse", call(byAgent(bash("ls"), "sub"), "t3"))
	f.fire("PreToolUse", call(bash("baton status"), "t4")) // baton's own CLI touches only .baton
	if got := inFlight(f); !slices.Equal(got, []string{"t1", "t2", "t3/sub"}) {
		t.Fatalf("in flight: %q", got)
	}
	f.fire("PostToolUse", call(bash("make test"), "t1"))
	f.fire("PostToolUseFailure", call(edit("Edit", "/x/a.go"), "t2"))
	if got := inFlight(f); !slices.Equal(got, []string{"t3/sub"}) {
		t.Fatalf("after two results: %q", got)
	}
	f.fire("PreToolUse", call(bash("rm x"), "t5"))
	f.fire("PermissionDenied", call(bash("rm x"), "t5"))
	f.fire("PreToolUse", call(bash("sleep 1"), "t6"))
	f.fire("Stop", map[string]any{})
	if got := inFlight(f); !slices.Equal(got, []string{"t3/sub"}) {
		t.Fatalf("after a denial and the turn's end: %q", got)
	}
	f.fire("SubagentStop", map[string]any{"agent_id": "sub"})
	f.fire("PreToolUse", call(bash("ls"), "t7"))
	f.fire("UserPromptSubmit", map[string]any{"prompt": "next"})
	if got := inFlight(f); len(got) != 0 {
		t.Fatalf("after the subagent ended and a new turn began: %q", got)
	}
	f.fire("PreToolUse", call(bash("ls"), "t8"))
	f.fire("SessionStart", map[string]any{"source": "resume"})
	if got := inFlight(f); len(got) != 0 {
		t.Fatalf("after a new session: %q", got)
	}

	f.set(func(st *state.State) { st.BoundaryOwed = true })
	if d, _ := permit(f.fire("PreToolUse", call(bash("ls"), "t9"))); d != "deny" || len(inFlight(f)) != 0 {
		t.Errorf("a call held back (%s) is on record: %q", d, inFlight(f))
	}
}

// A worker's calls are its own: they never hold up a landing in the lead's checkout.
func TestAWorkersToolCallsAreNotRecorded(t *testing.T) {
	w, lead, _ := newWorker(t)
	w.fire("SessionStart", map[string]any{"source": "startup"})
	w.fire("PreToolUse", call(bash("make test"), "t1"))
	if got := w.state().Run.Tools; len(got) != 0 {
		t.Errorf("the worker's run records %+v", got)
	}
	if got := lead.state().Run.Tools; len(got) != 0 {
		t.Errorf("the lead's run records %+v", got)
	}
}

// landing puts a worker of the lead's run in the merge queue.
func landing(f *fixture) {
	f.set(func(st *state.State) {
		st.Workers = append(st.Workers, state.Worker{Phase: "P1", Ref: "sess-1/P1", Session: "w", Status: state.WorkerLanding})
	})
}

// While a worker's work waits to land, each call of the lead's waits for a landing under way, for a
// while at most; with nothing to land, none waits on the lock.
func TestTheLeadsCallsWaitForALanding(t *testing.T) {
	f := newFixture(t, true)
	release, ok, err := state.HoldLanding(f.dir)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	defer release()
	start := time.Now()
	f.fire("PreToolUse", call(bash("ls"), "t1"))
	if took := time.Since(start); took > time.Second {
		t.Fatalf("with no worker's work to land, a call waited %s", took)
	}

	landing(f)
	go func(release func()) {
		time.Sleep(300 * time.Millisecond)
		release()
	}(release)
	start = time.Now()
	f.fire("PreToolUse", call(bash("ls"), "t2"))
	if took := time.Since(start); took < 250*time.Millisecond {
		t.Fatalf("a call ran %s into a landing", took)
	}
	if !f.logged("land_wait", nil) || !slices.Contains(inFlight(f), "t2") {
		t.Errorf("the wait was not logged, or the call is not on record\n%s", f.eventLog())
	}

	release, _, _ = state.HoldLanding(f.dir)
	defer release()
	defer func(w time.Duration) { landWait = w }(landWait)
	landWait = 200 * time.Millisecond
	if d, _ := permit(f.fire("PreToolUse", call(bash("ls"), "t3"))); d == "deny" {
		t.Error("a call was refused over a landing that hangs")
	}
	if !f.logged("land_wait", map[string]any{"gave_up": true}) {
		t.Errorf("giving up was not logged\n%s", f.eventLog())
	}
}

// What landed underneath the lead reaches it once, mid-turn as hook feedback or with its next prompt,
// after any compaction owed; never in a subagent's call.
func TestTheLeadIsToldWhatLanded(t *testing.T) {
	f := newFixture(t, true)
	l := state.Landing{Phase: "P1", Commits: []string{"aaaaaaa1111", "bbbbbbb2222"}, Files: []string{"a.go", "b.go"}, Head: "bbbbbbb2222", At: f.now}
	f.set(func(st *state.State) {
		st.RunBranch = "main"
		st.Landed = append(st.Landed, l)
		st.BoundaryOwed = true
	})
	if out := f.fire("PostToolUse", bash("ls")); feedback(out) != "" {
		t.Fatalf("told with a compaction owed: %v", out)
	}
	f.set(func(st *state.State) { st.BoundaryOwed = false })
	if out := f.fire("PostToolUse", byAgent(bash("ls"), "sub")); feedback(out) != "" {
		t.Fatalf("told in a subagent's call: %v", out)
	}
	out := f.fire("PostToolUse", bash("ls"))
	want := decide.Landed([]state.Landing{l}, "main")
	if got := feedback(out); !strings.Contains(got, want) {
		t.Fatalf("feedback %q, want %q", got, want)
	}
	for _, s := range []string{"on main", "P1's work: 2 commits aaaaaaa, bbbbbbb, changing a.go, b.go", "HEAD is now bbbbbbb", "Never amend, reset, rebase or revert commits you did not make"} {
		if !strings.Contains(want, s) {
			t.Errorf("the announcement lacks %q: %s", s, want)
		}
	}
	if !f.state().Landed[0].Told || !f.logged("landed_told", map[string]any{"via": "tool"}) {
		t.Errorf("not recorded as told\n%s", f.eventLog())
	}
	if out := f.fire("PostToolUse", bash("ls")); feedback(out) != "" {
		t.Errorf("told twice: %v", out)
	}

	f.set(func(st *state.State) {
		st.Landed = append(st.Landed, state.Landing{Phase: "P2", Commits: []string{"c"}, Files: []string{"c.go"}, Head: "c"})
	})
	ctx := additionalContext(f.fire("UserPromptSubmit", map[string]any{"prompt": "go on"}))
	if !strings.Contains(ctx, "P2's work: 1 commit c, changing c.go") || !f.logged("landed_told", map[string]any{"via": "prompt"}) {
		t.Errorf("at the prompt: %q", ctx)
	}
}

// baton's messages reach the session's model once: with its next tool call, its stop, or its prompt.
func TestMessagesReachTheModel(t *testing.T) {
	w, _, _ := newWorker(t)
	w.fire("SessionStart", map[string]any{"source": "startup"})
	tell := func(text string) {
		w.set(func(st *state.State) { state.Tell(st, state.Message{Kind: state.MessageLand, Text: text}, w.now) })
	}

	tell("rebase it yourself")
	if got := feedback(w.fire("PostToolUse", bash("ls"))); got != "rebase it yourself" {
		t.Errorf("mid-turn: %q", got)
	}
	tell("again, at the stop")
	out := w.fire("Stop", map[string]any{})
	if out["decision"] != "block" || out["reason"] != "again, at the stop" {
		t.Errorf("at the stop: %v", out)
	}
	tell("with the prompt")
	if got := additionalContext(w.fire("UserPromptSubmit", map[string]any{"prompt": "[baton] go"})); got != "with the prompt" {
		t.Errorf("with the prompt: %q", got)
	}
	if len(w.state().Inbox) != 0 {
		t.Errorf("left: %+v", w.state().Inbox)
	}
	for _, via := range []string{"tool", "stop", "prompt"} {
		if !w.logged("message_delivered", map[string]any{"message": state.MessageLand, "via": via}) {
			t.Errorf("not logged via %s\n%s", via, w.eventLog())
		}
	}
}

func TestRewritingGit(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		want string // amend, all, the base, or "" for none
	}{
		{"git commit --amend -m 'fix'", "amend"},
		{"git add a.go && git commit --amend --no-edit", "amend"},
		{"git commit -m amend", ""},
		{"git reset --hard HEAD~1", "HEAD~1"},
		{"git reset --soft abc123", "abc123"},
		{"git reset HEAD~2", "HEAD~2"},
		{"git reset", ""},
		{"git reset --hard", ""},
		{"git reset HEAD a.go", ""},
		{"git reset -- a.go", ""},
		{"git reset HEAD~1 -- a.go", ""},
		{"git rebase main", "main"},
		{"git rebase -i HEAD~3", "HEAD~3"},
		{"git rebase --onto other main", "main"},
		{"git rebase -x 'make test' main", "main"},
		{"git rebase", "@{upstream}"},
		{"git rebase --root", "all"},
		{"git rebase --continue", ""},
		{"git rebase --abort", ""},
		{"git log --amend", ""},
		{"echo git commit --amend", ""},
	} {
		got := ""
		if rw := rewritingGit(c.cmd); rw != nil {
			switch {
			case rw.amend:
				got = "amend"
			case rw.all:
				got = "all"
			default:
				got = rw.base
			}
		}
		if got != c.want {
			t.Errorf("%q: %q, want %q", c.cmd, got, c.want)
		}
	}
}

// The lead may not rewrite work a worker landed, by amending, resetting or rebasing it away; its own
// commits it may.
func TestTheLeadMayNotRewriteLandedWork(t *testing.T) {
	gittest.Isolate(t)
	f := newFixture(t, true)
	root := filepath.Dir(f.dir)
	gittest.Git(t, root, "init", "-q", "-b", "main")
	state.ExcludeFromGit(root)
	gittest.Write(t, root, "a.go", "a\n")
	gittest.Commit(t, root, "first")
	gittest.Write(t, root, "w.go", "w\n")
	gittest.Commit(t, root, "P1: the worker's")
	landed := gittest.Git(t, root, "rev-parse", "HEAD")

	allowed := func(cmd string) bool {
		d, _ := permit(f.fire("PreToolUse", bash(cmd)))
		return d != "deny"
	}
	if !allowed("git commit --amend --no-edit") {
		t.Fatal("refused with nothing landed")
	}
	f.set(func(st *state.State) {
		st.Landed = []state.Landing{{Phase: "P1", Commits: []string{landed}, Head: landed, Told: true}}
	})
	for _, cmd := range []string{"git commit --amend --no-edit", "git reset --hard HEAD~1", "git rebase -i HEAD~1", "git rebase --root"} {
		out := f.fire("PreToolUse", bash(cmd))
		d, why := permit(out)
		if d != "deny" || !strings.Contains(why, "would rewrite work baton landed on this branch from P1 (commit "+landed[:7]+")") {
			t.Errorf("%s: %s %q", cmd, d, why)
		}
		if !strings.Contains(say(out), "it would rewrite work a worker landed") {
			t.Errorf("%s: not announced: %v", cmd, out)
		}
	}
	if !f.logged("git_refused", map[string]any{"why": "landed"}) {
		t.Errorf("not logged\n%s", f.eventLog())
	}
	for _, cmd := range []string{"git commit -m mine", "git rebase --continue", "git reset --hard", "git reset HEAD w.go", "git reset nosuchcommit"} {
		if !allowed(cmd) {
			t.Errorf("%s: refused", cmd)
		}
	}
	gittest.Write(t, root, "mine.go", "mine\n")
	gittest.Commit(t, root, "the lead's own")
	for _, cmd := range []string{"git commit --amend --no-edit", "git reset --soft HEAD~1", "git rebase -i HEAD~1"} {
		if !allowed(cmd) {
			t.Errorf("%s: the lead's own commit, refused", cmd)
		}
	}
	if allowed("git reset --hard HEAD~2") {
		t.Error("a reset past the landed commit went through")
	}
}

// Every new path of the lead's hooks around landings fails open: a panic in one exits 0 and writes
// nothing. (Recording a call and ending it change the state alone, inside handlers whose panics
// TestPanicFailsOpenForEveryEvent covers.)
func TestTheLeadsLandingHooksFailOpen(t *testing.T) {
	for _, c := range []struct {
		name  string
		event string
		in    map[string]any
		set   func(st *state.State, now time.Time)
		git   bool
	}{
		{"a call waiting for a landing", "PreToolUse", call(bash("ls"), "t1"), func(st *state.State, _ time.Time) {
			st.Workers = append(st.Workers, state.Worker{Phase: "P1", Ref: "sess-1/P1", Session: "w", Status: state.WorkerLanding})
		}, false},
		{"a rewrite of landed work refused", "PreToolUse", bash("git commit --amend --no-edit"), nil, true},
		{"a landing told", "PostToolUse", bash("ls"), func(st *state.State, _ time.Time) {
			st.Landed = append(st.Landed, state.Landing{Phase: "P1", Commits: []string{"c"}, Head: "c"})
		}, false},
		{"a landing told at the prompt", "UserPromptSubmit", map[string]any{"prompt": "go"}, func(st *state.State, _ time.Time) {
			st.Landed = append(st.Landed, state.Landing{Phase: "P1", Commits: []string{"c"}, Head: "c"})
		}, false},
		{"a message at the stop", "Stop", map[string]any{}, func(st *state.State, now time.Time) {
			state.Tell(st, state.Message{Kind: state.MessageLand, Text: "x"}, now)
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			gittest.Isolate(t)
			f := newFixture(t, true)
			if c.git {
				root := filepath.Dir(f.dir)
				gittest.Git(t, root, "init", "-q")
				gittest.Write(t, root, "a.go", "a\n")
				gittest.Commit(t, root, "first")
				head := gittest.Git(t, root, "rev-parse", "HEAD")
				f.set(func(st *state.State) {
					st.Landed = []state.Landing{{Phase: "P1", Commits: []string{head}, Head: head, Told: true}}
				})
			}
			if c.set != nil {
				f.set(func(st *state.State) { c.set(st, f.now) })
			}
			// A landing that hangs: the call gives up on it, and says so.
			release, _, _ := state.HoldLanding(f.dir)
			defer release()
			defer func(w time.Duration) { landWait = w }(landWait)
			landWait = 150 * time.Millisecond
			open := func(ctx Context) (*state.Store, error) {
				s, err := OpenRun(f.clock)(ctx)
				if s != nil {
					s.Now = func() time.Time { panic("boom") }
				}
				return s, err
			}
			in := c.in
			in["hook_event_name"], in["session_id"] = c.event, f.sid
			raw, _ := json.Marshal(in)
			var out, errb bytes.Buffer
			code := Dispatch(c.event, bytes.NewReader(raw), &out, &errb, f.env(f.inst), f.clock, Handlers(Deps{Open: open}))
			if code != 0 || out.Len() != 0 || !strings.Contains(errb.String(), "failed open") {
				t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
			}
		})
	}
}
