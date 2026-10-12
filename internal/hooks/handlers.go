package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/gitx"
	"github.com/ozzyfromspace/baton/internal/machine"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/transcript"
	"github.com/ozzyfromspace/baton/internal/valve"
)

// NudgePrefix starts every message baton types or sends into the session, so the hooks can tell
// baton's prompts from the human's.
const NudgePrefix = "[baton]"

// Deps gives handlers access to the project's state.
type Deps struct {
	// Open returns the run of the session the hook is for (OpenRun, in production).
	Open func(c Context) (*state.Store, error)
	// Machine reads the machine (nil: machine.Read), which with workers: auto decides how many workers may
	// run beside the session (Context.Machine).
	Machine func() machine.Reading
}

// OpenRun returns the run a hook belongs to, in the project the host named in BATON_DIR. Every hook knows
// its loop first (docs/research/visible-workers.md): one from a phase agent (its agent_id linked,
// state.Project.LinkAgent) is its phase's sub-run's, written as the sub-run's worker instance; one from an
// agent baton did not start, working in a phase agent's worktree, is a subagent of that agent's model, its
// phase's as the lead's model's subagents are the lead's, or, with no agent type, Claude Code's own fork of
// that agent (errFork: it changes nothing, for the lead or the phase); any other is the session's, its
// session bound to its host first (state.Project.Bind). A SessionStart after /clear, or after a plan
// approved with "clear context", carries the host's run into the new session id.
func OpenRun(now func() time.Time) func(c Context) (*state.Store, error) {
	return func(c Context) (*state.Store, error) {
		dir := c.Env("BATON_DIR")
		if dir == "" {
			return nil, fmt.Errorf("BATON_DIR is not set")
		}
		p, err := state.OpenProject(dir, now)
		if err != nil {
			return nil, err
		}
		session, instance := str(c.Input, "session_id"), c.Env("BATON_INSTANCE")
		if agent := str(c.Input, "agent_id"); agent != "" {
			if ref := p.AgentRun(agent); ref != "" {
				_, phase, _ := strings.Cut(ref, "/")
				s, err := p.Run(ref, state.WorkerInstance(instance, phase))
				if err == nil {
					s.Agent = agent
				}
				return s, err
			}
			if s, ok := p.PhaseAt(session, instance, str(c.Input, "cwd")); ok {
				if str(c.Input, "agent_type") == "" {
					return nil, errFork
				}
				return s, nil // a subagent the phase agent's model started, which is the phase's
			}
		}
		return p.Bind(state.Binding{
			Session:  session,
			Instance: instance,
			Carry:    c.Event == "SessionStart" && str(c.Input, "source") == "clear",
		})
	}
}

// errFork is a hook of Claude Code's own fork of a phase agent (about every 30 s while one runs, with an
// id of its own, no agent type and the agent's worktree for its working directory: spike 32), which is
// nobody's to act on.
var errFork = fmt.Errorf("a hook of a fork Claude Code made of a phase agent")

// Handlers returns baton's handler for each hook event.
func Handlers(d Deps) map[string]Handler {
	h := &handlers{d}
	m := map[string]Handler{
		"SessionStart":       h.sessionStart,
		"SessionEnd":         h.sessionEnd,
		"UserPromptSubmit":   h.userPromptSubmit,
		"PreToolUse":         h.preToolUse,
		"PostToolUse":        h.toolDone,
		"PostToolUseFailure": h.toolDone,
		"PermissionRequest":  h.permissionRequest,
		"PermissionDenied":   h.toolDone,
		"Notification":       h.notification,
		"SubagentStart":      h.subagentStart,
		"SubagentStop":       h.subagentStop,
		"Stop":               h.stop,
		"StopFailure":        h.stopFailure,
		"PreCompact":         h.compactSeen,
		"PostCompact":        h.compactSeen,
		"PostCompactRewake":  h.postCompactRewake,
		"SessionStartRewake": h.sessionStartRewake,
		"WorktreeCreate":     h.worktreeCreate,
	}
	read := d.Machine
	if read == nil {
		read = machine.Read
	}
	for event, fn := range m {
		m[event] = func(c Context) (Result, error) {
			c.Machine = read
			if agent := str(c.Input, "agent_id"); agent != "" && d.Open != nil {
				// A subagent's hook: its loop comes first. A phase agent's hooks are its sub-run's own, a fork's
				// are nobody's, and any other subagent is one its loop's model started.
				s, err := d.Open(c)
				if err == errFork {
					return Result{}, nil
				}
				if c.Agent = agent; err == nil && s.Agent == agent {
					c.Agent = ""
				}
			}
			return fn(c)
		}
	}
	return m
}

type handlers struct{ d Deps }

// update applies fn to the state of the hook's run (its loop's: OpenRun) if this host drives it;
// otherwise (the same conversation resumed in another terminal while the first still runs it) the hook is
// dormant. Only a session's main agent says what the session is (its id, its permission mode) and where its
// transcript stands (state.Remark): a subagent's hook, a phase agent's among them, carries the lead's
// session and transcript, and a compaction hook may be any loop's.
func (h *handlers) update(c Context, fn func(st *state.State, s *state.Store) error) (*state.Store, state.State, error) {
	s, err := h.d.Open(c)
	if err != nil {
		return nil, state.State{}, err
	}
	main := str(c.Input, "agent_id") == ""
	var owner bool
	var rewind []event
	st, err := s.Update(func(st *state.State) error {
		if owner = st.IsOwner(s.Instance, c.Now); !owner {
			return errNotOwner
		}
		st.Run.LastActivity = c.Now
		if main {
			if sid := str(c.Input, "session_id"); sid != "" {
				st.Run.SessionID = sid
			}
			if pm := str(c.Input, "permission_mode"); pm != "" {
				st.Run.PermissionMode = pm
			}
			if !compactionHook(c) {
				state.Remark(st, str(c.Input, "transcript_path"), c.Now)
			}
		}
		rewind = noticeRewind(c, st, s)
		return fn(st, s)
	})
	if err == errNotOwner {
		return nil, st, errNotOwner
	}
	if err == nil {
		for _, e := range rewind {
			s.Event(e.kind, e.fields)
		}
	}
	return s, st, err
}

var errNotOwner = fmt.Errorf("this session does not own the project")

func ok(err error) error {
	if err == errNotOwner || err == errFork {
		return nil
	}
	return err
}

// onScreen applies fn to the state of the run whose session's screen shows what the phase agent of s (a
// sub-run OpenRun opened for one of its hooks) asks the human: the lead's. Its permission prompts are drawn
// there and hold baton's typing there like any dialog (docs/research/visible-workers.md), so they are
// recorded there, under the agent's id. It changes nothing for any other store.
func (h *handlers) onScreen(c Context, s *state.Store, fn func(st *state.State)) (*state.Store, error) {
	if s == nil || s.Agent == "" {
		return nil, nil
	}
	up, err := s.ParentRun()
	if err != nil {
		return nil, err
	}
	up.Instance = c.Env("BATON_INSTANCE")
	_, err = up.Update(func(st *state.State) error {
		if !st.IsOwner(up.Instance, c.Now) {
			return errNotOwner
		}
		fn(st)
		return nil
	})
	return up, err
}

// preToolUse does two things.
//
// It lets baton's own CLI through deterministically. A plain `baton …` command is allowed outright, so
// neither a permission prompt nor the auto-mode classifier can park an unattended run on it. One whose
// arguments the shell would rewrite (backticks or $ inside double quotes: notes often quote code) is
// refused with the fix, because it would run those as commands and mangle the notes.
//
// And it holds the model where it must not carry on. After `baton done` or `baton checkpoint`, the turn
// must end so the host can compact; a model that carries on would start the next phase (or keep going)
// in the old context. A run resumed after the conversation grew while it was paused must record where
// the phase stands first (`baton checkpoint`), so it goes on from a compacted context with those notes.
// Once a phase's decisions without the human reach the cap, the turn must end for
// the human to review them. And a proposal must be put to the human before anything else happens. So
// until then every main-agent tool call except baton's own CLI (and the questions baton issued) is
// denied, with the reason. Subagents are not held: they cannot end the main turn. Nor are the calls baton's
// mod makes in the lead's session to spawn, message and stop phase agents: they are how the host acts.
func (h *handlers) preToolUse(c Context) (Result, error) {
	if modCall(c.Input) {
		h.spawning(c)
		return Result{}, nil
	}
	if s, phase := h.steersAgent(c); phase != "" {
		tool := str(c.Input, "tool_name")
		s.Event("refused", map[string]any{"tool": tool, "why": "a phase agent", "phase": phase})
		return permission("deny", decide.AgentIsBatons(tool, phase)), nil
	}
	if str(c.Input, "tool_name") == "Bash" {
		ti, _ := c.Input["tool_input"].(map[string]any)
		if baton, verdict := batonShell(str(ti, "command")); baton {
			switch verdict {
			case shellSimple:
				return permission("allow", "baton's own CLI"), nil
			case shellSubstitution:
				return permission("deny", NudgePrefix+" The shell would rewrite part of this baton command: backticks, $( ) and $NAME inside double quotes run as commands or expand. "+
					"Put the text in single quotes instead (write an apostrophe as '\\''), or leave those characters out, and run it again."), nil
			}
			return Result{}, nil // operators or redirections: Claude Code's usual permission checks apply
		}
	}
	var hold string
	var refuseQuestion string
	var pending []issued
	var sweep string
	var others []state.RunInfo
	var refuseWorker, refuseHere, edited string
	var gate state.Job
	var rw *rewrite
	if str(c.Input, "tool_name") == "Bash" {
		ti, _ := c.Input["tool_input"].(map[string]any)
		sweep = sweepingGit(str(ti, "command"))
		rw = rewritingGit(str(ti, "command"))
	}
	if rw != nil {
		if res, refused, err := h.guardLanded(c, rw); refused || err != nil {
			return res, ok(err)
		}
	}
	var landing bool
	s, _, err := h.update(c, func(st *state.State, s *state.Store) error {
		holds := func() {
			if phase := s.Worker(); phase != "" {
				if refuseWorker, edited = workerRefusal(c.Input, s, phase); refuseWorker != "" {
					return
				}
			}
			if j := st.HereGate(); j != nil {
				// A gate runs in this working tree (baton verify --here), and reads its files as it runs.
				if edited = editedPath(c.Input, s.Root); edited != "" {
					refuseHere, gate = decide.HereEdit(*j), *j
					return
				}
			}
			if sweep != "" {
				if others = sharedCheckout(s); len(others) > 0 {
					return
				}
			}
			own := false
			if st.Mode == state.ModeRunning && str(c.Input, "tool_name") == "AskUserQuestion" {
				// A human-started turn may ask the human anything, except a question passed off as baton's:
				// one that starts "baton:" while baton has a question waiting must be that question, word
				// for word, or the host will not recognize it and the run waits on an answer nobody gives.
				if _, own = matchIssued(st, c.Input); !own {
					pending = issuedQuestions(st)
					if st.Run.TurnBy != "human" || len(pending) > 0 && claimsBaton(c.Input) {
						refuseQuestion = questionRefusal(pending, words(c, s))
						return
					}
				}
			}
			if st.Mode != state.ModeRunning || c.Agent != "" || isBatonCommand(c.Input) {
				return
			}
			hold = holdCall(st, own)
		}
		if holds(); refuseWorker == "" && refuseHere == "" && len(others) == 0 && refuseQuestion == "" && hold == "" && s.Worker() == "" {
			// The call runs, in the lead's checkout: no worker's work lands there until it has ended, and
			// it waits for any landing under way.
			st.Run.ToolStarted(state.ToolCall{ID: str(c.Input, "tool_use_id"), Agent: c.Agent, Tool: str(c.Input, "tool_name"), Since: c.Now})
			landing = st.Landing()
		}
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	if landing {
		waitLanding(s)
	}
	if refuseWorker != "" {
		fields := map[string]any{"tool": str(c.Input, "tool_name"), "why": "plan mode"}
		say := "baton: kept this worker out of plan mode — its phase is planned already"
		if edited != "" {
			fields["why"], fields["path"] = "the lead's checkout", edited
			say = "baton: refused an edit to " + edited + " in the lead's checkout — a worker edits only its own worktree"
		}
		s.Event("worker_refused", fields)
		res := permission("deny", refuseWorker)
		res.Output["systemMessage"] = say
		return res, nil
	}
	if refuseHere != "" {
		s.Event("edit_refused", map[string]any{"tool": str(c.Input, "tool_name"), "path": edited, "job": gate.ID, "why": "a gate runs here"})
		res := permission("deny", refuseHere)
		res.Output["systemMessage"] = "baton: refused an edit to " + edited + " — job " + gate.ID + "'s gate runs in this working tree until it ends"
		return res, nil
	}
	if len(others) > 0 {
		var ids []string
		for _, r := range others {
			ids = append(ids, r.ID)
		}
		s.Event("git_refused", map[string]any{"command": sweep, "others": ids})
		res := permission("deny", gitRefusal(sweep, others))
		res.Output["systemMessage"] = "baton: refused `" + sweep + "` — another baton session is working in this checkout"
		return res, nil
	}
	if refuseQuestion != "" {
		s.Event("question_refused", map[string]any{"questions": len(questions(c.Input)), "issued": len(pending)})
		return permission("deny", refuseQuestion), nil
	}
	if hold == "" {
		return Result{}, nil
	}
	// While something has gone wrong, let the model LOOK. The block exists to stop work before a
	// compaction, and reading is not work — but it refused every tool, so in the one incident where a
	// human needed help, the agent could not read `.baton/events.jsonl` to diagnose it.
	if readOnlyTool(str(c.Input, "tool_name")) {
		if live, lerr := s.Load(); lerr == nil && live.Run.Escalation != nil {
			return Result{}, nil
		}
	}
	return permission("deny", hold), nil
}

// holdCall is why the main agent's call may not run while a plan runs, because something else must
// happen first (the turn must end, the human must be asked), or "" if it may. own says the call asks the
// human baton's own question.
func holdCall(st *state.State, own bool) string {
	p := st.OwnProposal()
	switch {
	case st.BoundaryOwed && !started(st):
		return endTurn("baton attached the plan, and compacts the conversation so far before " + st.Current + " begins")
	case st.BoundaryOwed:
		return endTurn("the phase is done and baton must compact the context before " + st.Current + " begins")
	case st.CheckpointOwed:
		return endTurn("you asked for a checkpoint and baton must compact the context first")
	case st.CheckpointDue:
		return NudgePrefix + " Not yet. " + decide.CheckpointOnResume(st.Current)
	case own:
		// baton's own question gets through the holds below: it is how they end.
	case st.ReviewDue != "" && !st.Run.HumanAt.After(st.ReviewAt) && !goingAhead(st):
		return endTurn(st.ReviewDue + " has made as many decisions without the human as baton allows before they review them, and baton stops for that review")
	case p != nil && !p.Asked:
		return NudgePrefix + " Not yet: " + decide.AskFirst(p.ID, p.Question) + ". Until the human has it, nothing else runs."
	case st.Replan != nil:
		return replanHold(st.Replan)
	case st.RelayUnasked() != nil:
		r := st.RelayUnasked()
		return decide.RelayHold(r.Phase, relayQuestion(*r).Call())
	}
	return ""
}

// landWait bounds how long a tool call waits for a landing under way: one takes a fraction of a second,
// and a call never waits on a landing that hangs.
var landWait = 10 * time.Second

// waitLanding holds a tool call of the lead's while a worker's work lands in its checkout, for landWait
// at most. A wait that gives up lets the call run.
func waitLanding(s *state.Store) {
	waited, err := state.WaitLanding(s.Project, landWait)
	switch {
	case err != nil:
		s.Event("land_wait", map[string]any{"ms": waited.Milliseconds(), "gave_up": true, "error": err.Error()})
	case waited > 100*time.Millisecond:
		s.Event("land_wait", map[string]any{"ms": waited.Milliseconds()})
	}
}

// guardLanded refuses the lead's git command rw if it would rewrite work the run's workers landed. It
// reports whether it refused.
func (h *handlers) guardLanded(c Context, rw *rewrite) (Result, bool, error) {
	s, err := h.d.Open(c)
	if err != nil || s.Worker() != "" {
		return Result{}, false, err
	}
	st, err := s.Load()
	if err != nil || len(st.Landed) == 0 || st.Owner != nil && !st.IsOwner(c.Env("BATON_INSTANCE"), c.Now) {
		return Result{}, false, err
	}
	why, commits := landedRewrite(rw, s.Root, st.Landed)
	if why == "" {
		return Result{}, false, nil
	}
	s.Event("git_refused", map[string]any{"command": rw.shown, "why": "landed", "commits": commits})
	res := permission("deny", why)
	res.Output["systemMessage"] = "baton: refused `" + rw.shown + "` — it would rewrite work a worker landed"
	return res, true, nil
}

// workerRefusal is why a worker on phase may not make a tool call, or "" if it may: it never changes the
// project's checkout, which is the lead's (edited is the path, relative to the checkout), and it does not
// plan, since its phase is planned already and only the lead attaches plans. Its subagents are held to
// the same.
func workerRefusal(in map[string]any, s *state.Store, phase string) (why, edited string) {
	if str(in, "tool_name") == "EnterPlanMode" {
		return decide.WorkerPlanRefused(phase), ""
	}
	if rel := editedPath(in, s.Checkout); rel != "" && editedPath(in, s.Root) == "" {
		return decide.WorkerEditRefused(phase, filepath.Join(s.Root, filepath.FromSlash(rel))), rel
	}
	return "", ""
}

// endTurn is a hold's reason when the turn must end.
func endTurn(why string) string {
	return NudgePrefix + " End your turn now: " + why + ". Do not start further work in this turn."
}

// readOnlyTool names the tools that only read. Bash is deliberately absent: a command line can do
// anything, and baton must not be the thing that decides which ones are safe.
func readOnlyTool(name string) bool {
	switch name {
	case "Read", "Grep", "Glob", "NotebookRead":
		return true
	}
	return false
}

// claimsBaton reports a question call that presents itself as baton's.
func claimsBaton(in map[string]any) bool {
	for _, q := range questions(in) {
		if strings.HasPrefix(strings.TrimSpace(q), "baton:") {
			return true
		}
	}
	return false
}

// questionRefusal is why a question that is not one baton issued is refused while a plan runs, and what
// to do instead. When baton has a question of its own waiting to be asked, it quotes the exact call.
func questionRefusal(pending []issued, w decide.Words) string {
	if len(pending) > 0 {
		return w.QuestionRefused(pending[0].Call())
	}
	return w.QuestionRefused("")
}

func permission(decision, reason string) Result {
	return Result{Output: map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision,
			"permissionDecisionReason": reason,
		},
	}}
}

// steersAgent is the phase of the phase agent a model's call would message or stop ("" for none), and the
// run it belongs to: baton's agents are the host's to steer (through its mod), never a model's, the
// lead's or an agent's.
func (h *handlers) steersAgent(c Context) (*state.Store, string) {
	ti, _ := c.Input["tool_input"].(map[string]any)
	var target string
	switch str(c.Input, "tool_name") {
	case "SendMessage":
		target = str(ti, "to")
	case "TaskStop":
		target = str(ti, "task_id")
	}
	if target == "" {
		return nil, ""
	}
	s, err := h.d.Open(c)
	if err != nil {
		return nil, ""
	}
	if s.Worker() != "" {
		if s, err = s.ParentRun(); err != nil {
			return nil, ""
		}
	}
	st, err := s.Load()
	if err != nil {
		return nil, ""
	}
	for _, w := range st.LiveWorkers() {
		if w.AgentID != "" && (w.AgentID == target || w.Phase == target) {
			return s, w.Phase
		}
	}
	return nil, ""
}

// modCall reports a call baton's mod made in the lead's session (its tool_use_id says a plugin made it) to
// spawn, message or stop a phase agent (spike 32).
func modCall(in map[string]any) bool {
	switch str(in, "tool_name") {
	case "Agent", "SendMessage", "TaskStop":
		return str(in, "agent_id") == "" && strings.HasPrefix(str(in, "tool_use_id"), "toolu_plugin_")
	}
	return false
}

// isBatonCommand reports a Bash call that runs baton's own CLI (always allowed).
func isBatonCommand(in map[string]any) bool {
	if str(in, "tool_name") != "Bash" {
		return false
	}
	ti, _ := in["tool_input"].(map[string]any)
	cmd := strings.TrimSpace(str(ti, "command"))
	return cmd == "baton" || strings.HasPrefix(cmd, "baton ")
}

func (h *handlers) activity(c Context) (Result, error) {
	_, _, err := h.update(c, func(*state.State, *state.Store) error { return nil })
	return Result{}, ok(err)
}

func (h *handlers) sessionStart(c Context) (Result, error) {
	source := str(c.Input, "source")
	if source == "compact" {
		return h.compactSeen(c)
	}
	var pl plan.Plan
	var havePlan bool
	var events []event
	var restarted *state.Restart
	var fresh *state.Recovery
	var ann, cleared string
	s, st, err := h.update(c, func(st *state.State, s *state.Store) error {
		st.Run.Ended = nil
		ann = announced(st)
		if source == "startup" || source == "resume" {
			st.Run.ExitAt = time.Time{}     // a session started anew is not on its way out of baton
			st.Run.ModReports = time.Time{} // and its claude has yet to show it runs baton's mod
			if e := st.Run.Escalation; e != nil && e.Kind == "crashed" {
				// claude runs again: the human resumed the session baton stopped bringing back.
				st.Run.Escalation, cleared = nil, e.Kind
			}
		}
		if source == "startup" && c.Env("BATON_RECOVER") != "" && s.Worker() == "" {
			// The fresh session its host started, as the conversation could not be resumed: it is briefed, once.
			if r := st.Run.Recovering(true); r != nil {
				r.Told = true
				rec := *r
				fresh = &rec
			}
		}
		// A new conversation (or one /clear started over) has nothing before its first turn; a resumed one
		// has.
		st.Run.History = source != "startup" && source != "clear"
		if source == "clear" {
			// The conversation that grew while baton was paused is gone: there is nothing to checkpoint.
			st.CheckpointDue = false
		}
		if source == "resume" && st.Run.Restart != nil {
			if c.Now.Sub(st.Run.Restart.At) < restartWindow {
				restarted = st.Run.Restart
				// A restart at a phase boundary takes the place of a compaction, which summarizes any
				// earlier /baton instructions away; an idle one leaves them in the conversation.
				if restarted.To == c.Env("BATON_VERSION") && st.Run.Compaction.Status != state.CompactQueued {
					st.Run.StaleSkill = restarted.From
				}
			}
			st.Run.Restart = nil
		}
		st.Run.TurnOpen, st.Run.Subagents, st.Run.Tools, st.Run.Mark = false, 0, nil, nil
		st.Run.Dialogs.Clear()
		events, _ = vanished(st, c.Now, false)
		return nil
	})
	if err == errNotOwner {
		return Result{Output: map[string]any{"systemMessage": "baton: another baton terminal is running this conversation's plan; " +
			"this one leaves it alone until that one exits"}}, nil
	}
	if err != nil {
		return Result{}, ok(err)
	}
	s.Event("session_start", map[string]any{"source": source})
	for _, e := range events {
		s.Event(e.kind, e.fields)
	}
	if cleared != "" {
		s.Event("escalation_cleared", map[string]any{"type": cleared})
	}
	if fresh != nil {
		s.Event("lead_fresh", map[string]any{"why": fresh.Why, "phase": st.Current, "session": str(c.Input, "session_id")})
	}
	note := ""
	if restarted != nil {
		running := c.Env("BATON_VERSION")
		s.Event("restarted", map[string]any{"from": restarted.From, "to": restarted.To, "version": running})
		if running == restarted.To {
			note = fmt.Sprintf("baton: restarted this session on baton %s (was %s) · ", running, restarted.From)
		} else {
			note = fmt.Sprintf("baton: could not restart this session on baton %s; it stays on %s · ", restarted.To, running)
		}
	}
	if p, err := s.LoadPlan(); err == nil && st.Mode != state.ModeIdle {
		pl, havePlan = p, true
	}
	if !havePlan {
		return Result{Output: map[string]any{"systemMessage": lines(ann, note+"baton: hosting this session (no plan attached — /baton plan or /baton run)")}}, nil
	}
	if fresh != nil && st.InHand() {
		return h.freshStart(c, s, pl, st, lines(ann, note+"baton: hosting · "+progressLine(pl, st))), nil
	}
	if source == "clear" && st.InHand() {
		return h.freshStart(c, s, pl, st, lines(ann, "baton: conversation cleared — "+phaseTitle(pl, st.Current)+" goes on, with its brief")), nil
	}
	return Result{Output: map[string]any{
		"systemMessage": lines(ann, note+"baton: hosting · "+progressLine(pl, st)),
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": Primer(pl, st, words(c, s), s.Decisions(st)),
		},
	}}, nil
}

// restartWindow is how long a restart on a newer baton may take to reach the restarted session's
// SessionStart; a record older than that belongs to a restart that never happened.
const restartWindow = 10 * time.Minute

// sessionStartRewake runs in the background when a session resumes. In a conversation its host resumed
// after claude exited by itself mid-plan (BATON_RECOVER), it wakes the model to go on (recovered). After an
// elevation it wakes the model with what it was about to do (BATON_PENDING, set by the relaunch). Either,
// exactly once.
func (h *handlers) sessionStartRewake(c Context) (Result, error) {
	if str(c.Input, "source") != "resume" {
		return Result{}, nil
	}
	if c.Env("BATON_RECOVER") != "" {
		if r, done, err := h.recovered(c); done || err != nil {
			return r, err
		}
	}
	pending := strings.TrimSpace(c.Env("BATON_PENDING"))
	if pending == "" {
		return Result{}, nil
	}
	deliver := false
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if st.Run.PendingDone != pending {
			st.Run.PendingDone, deliver = pending, true
		}
		return nil
	})
	if err != nil || !deliver {
		return Result{}, ok(err)
	}
	s.Event("pending_delivered", map[string]any{"pending": pending})
	return Result{Rewake: NudgePrefix + " This session is now hosted by baton (same conversation). Continue with: " + pending}, nil
}

// recovered wakes the model of a conversation its host resumed after claude exited by itself mid-plan,
// once: the turn under way was lost, and the phase in hand goes on (decide.Words.Recovered). With no
// phase in hand nothing is to go on, and the host drives the run as before (a boundary's compaction, …).
// done says the resume was handled.
func (h *handlers) recovered(c Context) (res Result, done bool, err error) {
	var r state.Recovery
	phase := ""
	s, _, err := h.update(c, func(st *state.State, s *state.Store) error {
		if rec := st.Run.Recovering(false); rec != nil && s.Worker() == "" {
			rec.Told, r = true, *rec
			if st.InHand() {
				phase = st.Current
			}
		}
		return nil
	})
	if err != nil || r.At.IsZero() {
		return Result{}, false, ok(err)
	}
	s.Event("lead_resumed", map[string]any{"why": r.Why, "phase": phase, "woken": phase != ""})
	if phase == "" {
		return Result{}, true, nil
	}
	return Result{Rewake: words(c, s).Recovered(phase, r.Why, false)}, true, nil
}

// Primer tells the model, at the start of a hosted session, how baton expects it to report progress, and
// what the run (all of the run's decisions: its workers' too) has decided without the human so far: after
// a /clear or a restart the model has none of it in context, and the human may ask about it.
func Primer(pl plan.Plan, st state.State, w decide.Words, all []state.Decision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This session is hosted by baton, which runs the attached plan %q phase by phase and compacts the context between phases. ", pl.Title)
	switch st.Mode {
	case state.ModeComplete:
		b.WriteString("Every phase of the plan is done.")
	case state.ModePaused:
		b.WriteString("baton is paused: the human is driving; do not report phase progress unless asked. ")
	}
	if st.Mode != state.ModeComplete {
		if i := pl.Index(st.Current); i >= 0 {
			fmt.Fprintf(&b, "Current phase: %s — %s (see %s). ", st.Current, pl.Phases[i].Title, pl.File)
		}
		switch {
		case st.Mode != state.ModeRunning:
		case st.BoundaryOwed:
			fmt.Fprintf(&b, "%s has not started: baton compacts the context first, so do not start it, and end your turn when you are given one. ", st.Current)
		case st.CheckpointDue:
			b.WriteString(decide.CheckpointOnResume(st.Current) + " ")
		}
		if aside := phasesAside(pl, st); aside != "" {
			b.WriteString(aside + " ")
		}
		b.WriteString(w.Reporting())
	}
	if ds := decide.WithoutHuman(all); len(ds) > 0 {
		b.WriteString("\n\n" + strings.TrimSpace(decide.RecordSection(ds)))
	}
	return b.String()
}

// phasesAside names, for the Primer, the phases other than the current one that are verifying or set
// aside, and why, and those baton's workers have. baton takes each up again, or lands it, by itself, so
// the model leaves them alone. "" for none.
func phasesAside(pl plan.Plan, st state.State) string {
	var verifying, parked, working []string
	for _, ph := range pl.Phases {
		ps := st.Phases[ph.ID]
		if ps == nil || ph.ID == st.Current {
			continue
		}
		name := ph.ID + " — " + ph.Title
		switch ps.Status {
		case state.PhaseVerifying:
			if j := st.Latest(ph.ID); j != nil {
				name += " (job " + j.ID + ")"
			}
			verifying = append(verifying, name)
		case state.PhaseParked:
			parked = append(parked, name+" ("+parkedWhy(st, ps.Park)+")")
		case state.PhaseWorking:
			working = append(working, name)
		}
	}
	var out []string
	if len(verifying) > 0 {
		out = append(out, "Verifying, each done once its job passes: "+strings.Join(verifying, "; ")+".")
	}
	if len(parked) > 0 {
		out = append(out, "Set aside unfinished: "+strings.Join(parked, "; ")+".")
	}
	if len(out) > 0 {
		out = append(out, "baton takes these up again by itself: work only on the current phase.")
	}
	if len(working) > 0 {
		out = append(out, decide.WorkingPhases(working))
	}
	return strings.Join(out, " ")
}

// parkedWhy is why a phase was set aside, for the model.
func parkedWhy(st state.State, pk *state.Park) string {
	if pk == nil {
		return "set aside"
	}
	switch pk.Reason {
	case state.ParkRed:
		return "its verification failed"
	case state.ParkUnverified:
		return "its verification had no verdict"
	case state.ParkCheckpoint:
		return "set aside at a checkpoint"
	}
	if j := st.Job(pk.Job); j != nil && j.Finished() {
		return "its job " + j.ID + " has ended"
	}
	return "until its job " + pk.Job + " ends"
}

// words is what the model-facing text depends on in this project: whether git is usable there, and how
// long a proposal waits for the human.
func words(c Context, s *state.Store) decide.Words {
	return decide.Words{Git: gitx.Usable(s.Root), Timeout: config.EscalationFromEnv(c.Env).Timeout, Worker: s.Worker() != ""}
}

func progressLine(pl plan.Plan, st state.State) string {
	done := 0
	for _, ph := range pl.Phases {
		if ps := st.Phases[ph.ID]; ps != nil && ps.Status == state.PhaseDone {
			done++
		}
	}
	line := fmt.Sprintf("%s · %d/%d phases done", pl.Title, done, len(pl.Phases))
	if st.Current != "" {
		line += " · current " + st.Current
	}
	for _, status := range []string{state.PhaseVerifying, state.PhaseParked, state.PhaseWorking} {
		var ids []string
		for _, ph := range pl.Phases {
			if ps := st.Phases[ph.ID]; ps != nil && ps.Status == status && ph.ID != st.Current {
				ids = append(ids, ph.ID)
			}
		}
		label := status
		if status == state.PhaseWorking {
			label = "workers on"
		}
		if len(ids) > 0 {
			line += " · " + label + " " + strings.Join(ids, ", ")
		}
	}
	if st.Mode != state.ModeRunning {
		line += " · " + st.Mode
	}
	return line
}

func (h *handlers) sessionEnd(c Context) (Result, error) {
	reason := str(c.Input, "reason")
	s, _, err := h.update(c, func(st *state.State, s *state.Store) error {
		st.Run.Ended = &state.Ended{Reason: reason, At: c.Now}
		st.Run.TurnOpen = false
		if reason == "clear" && st.Run.PlanFile != "" && planOnScreen(st) {
			// Approved with "clear context", most likely: the next session's first tool call confirms it.
			st.Run.ClearedPlan = &state.Approval{File: st.Run.PlanFile, At: c.Now}
		}
		// A restart resumes the session, and the host ends and resumes its workers itself.
		if st.Mode == state.ModeRunning && !st.Run.Ended.Deliberate() && st.Run.Restart == nil && s.Worker() == "" {
			notice(st, "session_ended", "the session ended mid-plan ("+reason+")", c.Now)
		}
		return nil
	})
	if err == nil {
		s.Event("session_end", map[string]any{"reason": reason})
	}
	return Result{}, ok(err)
}

// planOnScreen reports a plan approval of the main agent's still on record.
func planOnScreen(st *state.State) bool {
	for _, d := range st.Run.Dialogs {
		if d.Tool == "ExitPlanMode" && d.Agent == "" {
			return true
		}
	}
	return false
}

// TurnSource classifies a submitted prompt: a rewake or nudge from baton, another task notification,
// or the human.
func TurnSource(prompt string) string {
	p := strings.TrimSpace(prompt)
	switch {
	case strings.HasPrefix(p, NudgePrefix):
		return "baton"
	case strings.HasPrefix(p, "<task-notification>"):
		if strings.Contains(p, NudgePrefix) {
			return "baton"
		}
		return "system"
	default:
		return "human"
	}
}

func (h *handlers) userPromptSubmit(c Context) (Result, error) {
	by := TurnSource(str(c.Input, "prompt"))
	mark := markOf(str(c.Input, "transcript_path")) // read before the lock: the turn's next hook looks at the prompt (rewind.go)
	var events []event
	var tell, staleSkill, ann string
	var told []string
	var dl delivery
	var news news
	var ask, asker string
	var owed *state.Briefing
	s, st, err := h.update(c, func(st *state.State, s *state.Store) error {
		st.Run.TurnOpen, st.Run.TurnBy, st.Run.TurnStarted = true, by, c.Now
		owed = takeBriefing(c, st) // a compaction's brief not given yet comes first
		st.Run.ToolsEnded("")      // a prompt starts a turn: no call of the last one runs
		st.Run.Mark = nil
		if st.Mode == state.ModeRunning {
			st.Run.Mark = mark
		}
		ann = announced(st)
		staleSkill, st.Run.StaleSkill = st.Run.StaleSkill, ""
		st.Run.Dialogs.CloseMain() // a prompt was submitted, so no dialog of the main agent is on screen
		st.Run.ClearedPlan = nil   // a "clear context" approval starts working with no prompt
		events, tell = vanished(st, c.Now, by == "human")
		events = append(events, relayVanished(st, c.Now)...)
		if by == "human" {
			// The human is engaged: whatever baton escalated, they have it now, and the model may act on
			// what they say (state.Held). A proposal held for them is answered by what they wrote, and so
			// is a worker's block or review they write to it about (in its own session).
			if e, t, ok := released(st); ok {
				events, tell = append(events, e), t
			} else if phase := s.Worker(); phase != "" {
				if e, t, ok := answeredHere(st, phase); ok {
					events, tell = append(events, e), t
				}
			}
			var record string
			if told, record = tellDecisions(st, s.WorkerDecisions(*st)); record != "" {
				tell = strings.TrimSpace(tell + "\n\n" + record)
			}
			if r := st.Replan; r != nil {
				// The question about the plan they approved went unanswered, and they wrote instead.
				st.Replan = nil
				events = append(events, event{"replan_resolved", map[string]any{"plan": r.File, "answer": "", "by": "prompt"}})
				tell = strings.TrimSpace(tell + "\n\n" + NudgePrefix + " baton's question about the plan the human approved (" + r.File +
					") went unanswered, so baton changed nothing. Their message is what to do; if they want that plan run, `/baton run` attaches it.")
			}
			st.Run.Progress()
			st.Run.Escalation = nil
			st.Run.HumanAt = c.Now
		}
		// Results due come with the prompt, whoever wrote it: the host types one to hand them over to an
		// idle session. So do baton's messages, and what landed underneath the lead.
		dl = take(st, c.Now)
		news = takeNews(st, s)
		// A worker's question comes first with a line the host typed (a human's prompt is theirs: the
		// question comes with the next tool call, or at the stop).
		if by == "baton" {
			if r, q, fresh := relayToGive(st, c.Now, words(c, s).Timeout); r != nil {
				ask, asker = decide.RelayAsk(r.Phase, q.Call()), r.Phase
				if fresh {
					events = append(events, relayIssued(*r, "prompt"))
				}
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	s.Event("turn_started", map[string]any{"by": by})
	if owed != nil {
		tell = strings.TrimSpace(h.briefed(c, s, st, *owed, "prompt") + "\n\n" + tell)
	}
	s.ToldWorkers(told)
	var says []string
	if ann != "" {
		says = append(says, ann)
	}
	if dl.any() {
		tell = strings.TrimSpace(dl.text(s) + "\n\n" + tell)
		says = append(says, dl.says())
		dl.event(s, "prompt")
	}
	if news.any() {
		tell = strings.TrimSpace(news.text + "\n\n" + tell)
		news.event(s, "prompt")
	}
	if ask != "" {
		tell = strings.TrimSpace(ask + "\n\n" + tell)
		says = append(says, relayComing(asker))
	}
	for _, e := range events {
		s.Event(e.kind, e.fields)
	}
	if refresh := skillRefresh(c, staleSkill); refresh != "" {
		tell = strings.TrimSpace(tell + "\n\n" + refresh)
		s.Event("skill_refreshed", map[string]any{"from": staleSkill, "to": c.Env("BATON_VERSION")})
		says = append(says, fmt.Sprintf("baton: gave Claude the current /baton instructions (the copy earlier in this conversation is from baton %s)", staleSkill))
	}
	if tell == "" {
		if len(says) > 0 {
			return Result{Output: map[string]any{"systemMessage": strings.Join(says, "\n")}}, nil
		}
		return Result{}, nil
	}
	out := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "UserPromptSubmit", "additionalContext": tell}}
	if len(told) > 0 {
		s.Event("decisions_told", map[string]any{"ids": told})
		says = append(says, "baton: decisions made without you since you last wrote: "+strings.Join(told, ", ")+
			" — Claude has them, each with its undo (/baton status lists them)")
	}
	if len(says) > 0 {
		out["systemMessage"] = strings.Join(says, "\n")
	}
	return Result{Output: out}, nil
}

// permissionRequest records a dialog joining Claude Code's queue. It fires when the dialog is requested,
// which is not when it is shown: a dialog requested earlier is shown first. A phase agent's is drawn on
// the lead's screen, and recorded there.
func (h *handlers) permissionRequest(c Context) (Result, error) {
	tool, agent := str(c.Input, "tool_name"), c.Agent
	if s, err := h.d.Open(c); err != nil || s.Agent != "" {
		if err != nil {
			return Result{}, ok(err)
		}
		var open int
		up, err := h.onScreen(c, s, func(st *state.State) {
			st.Run.Dialogs.Open(state.Dialog{Tool: tool, Agent: s.Agent, Key: dialogKey(c.Input), Since: c.Now})
			open = len(st.Run.Dialogs)
		})
		if err == nil {
			up.Event("dialog_open", map[string]any{"tool": tool, "dialog": "", "open": open, "agent": s.Agent, "phase": s.Worker()})
		}
		return Result{}, ok(err)
	}
	kind := ""
	var open int
	var proposal event
	var isProposal bool
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if tool == "AskUserQuestion" && agent == "" {
			if iq, own := matchIssued(st, c.Input); own {
				kind = iq.kind // baton's own question, exactly as issued: the only kind the host may answer
			}
		}
		if tool == "ExitPlanMode" && agent == "" {
			// A plan the human sent back fires no hook at all, so its dialog is still on record when the
			// model asks again. Only one plan is ever on screen: this one replaces it.
			st.Run.Dialogs.CloseExact("", tool, dialogKey(c.Input))
			ti, _ := c.Input["tool_input"].(map[string]any)
			st.Run.PlanFile = str(ti, "planFilePath")
		}
		st.Run.Dialogs.Open(state.Dialog{Tool: tool, Agent: agent, Key: dialogKey(c.Input), Since: c.Now, Kind: kind})
		open = len(st.Run.Dialogs)
		if kind == "context_warning" {
			st.Run.WarnOwed = false
		}
		switch kind {
		case "proposal":
			proposal, isProposal = asked(st, c.Now)
		case "relay":
			proposal, isProposal = relayAsked(st, c.Now)
		}
		return nil
	})
	if err == nil {
		fields := map[string]any{"tool": tool, "dialog": kind, "open": open}
		if agent != "" {
			fields["agent"] = agent
		}
		s.Event("dialog_open", fields)
		if isProposal {
			s.Event(proposal.kind, proposal.fields)
		}
	}
	return Result{}, ok(err)
}

// dialogKey identifies a tool call across its PermissionRequest and its result, which have no
// tool_use_id in common. A question is known by its question texts (its result adds the answers to its
// input); a plan approval by its tool alone, since its result's input is empty and only one is ever on
// screen (docs/research/sessions.md); any other call by a hash of its input, which is the same in both
// (measured for Bash, Write, Edit and Read).
func dialogKey(in map[string]any) string {
	switch str(in, "tool_name") {
	case "ExitPlanMode":
		return "plan"
	case "AskUserQuestion":
		qs := questions(in)
		for i, q := range qs {
			qs[i] = decide.Normalize(q)
		}
		return strings.Join(qs, "\n")
	}
	b, _ := json.Marshal(in["tool_input"]) // map keys are sorted, so the encoding is canonical
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// questions lists the question texts of an AskUserQuestion call.
func questions(in map[string]any) []string {
	ti, _ := in["tool_input"].(map[string]any)
	list, _ := ti["questions"].([]any)
	var out []string
	for _, q := range list {
		m, _ := q.(map[string]any)
		out = append(out, str(m, "question"))
	}
	return out
}

func (h *handlers) toolDone(c Context) (Result, error) {
	if modCall(c.Input) {
		// A call baton's mod made: it is not the model's, and nothing given with its result reaches the model.
		return h.spawned(c)
	}
	tool := str(c.Input, "tool_name")
	var v valveAction
	var dropped []event
	var notes, ann string
	var dl delivery
	var news news
	var owed *state.Briefing
	s, st, err := h.update(c, func(st *state.State, s *state.Store) error {
		st.Run.ToolEnded(str(c.Input, "tool_use_id"), c.Agent, tool)
		closed, _ := st.Run.Dialogs.CloseExact(c.Agent, tool, dialogKey(c.Input))
		if c.Event == "PostToolUse" {
			if path := editedPath(c.Input, s.Root); path != "" {
				st.Touched(path)
			}
			if tool == "Monitor" {
				resp, _ := c.Input["tool_response"].(map[string]any)
				st.Run.Watch(str(resp, "taskId"))
			}
		}
		if c.Agent != "" {
			return nil
		}
		if c.Event == "PostToolUse" {
			owed = takeBriefing(c, st) // a compaction's brief the conversation went on without
			w := words(c, s)
			switch {
			case tool == "ExitPlanMode" && s.Worker() != "":
				v = workerPlan(s.Worker(), approvedFile(c.Input))
			case tool == "ExitPlanMode":
				v = approved(st, s, c, approvedFile(c.Input), false, w)
			case tool == "AskUserQuestion":
				auto := !closed.AutoAnswered.IsZero()
				if v = answeredReplan(st, s, c, w); v.event == "" {
					if v = answeredProposal(st, c, auto); v.event == "" {
						if v = answeredRelay(st, c, auto); v.event == "" {
							if v = answeredReview(st, c); v.event == "" {
								v = answered(st, c, auto, w)
							}
						}
					}
				}
			}
			if file, ok := clearedApproval(st, c); ok && v.event == "" && s.Worker() == "" {
				v = approved(st, s, c, file, true, w)
			}
			// No other question while a proposal, a review or an approved plan waits on the human: one at a
			// time. A job's result waits too: the model must put the question first. (A worker's proposal
			// is the lead's to put to the human, and holds up nothing in the worker.) A worker's question
			// the lead is to put to the human comes before everything else of baton's, and only then.
			if v.event == "" && st.OwnProposal() == nil && st.ReviewDue == "" && st.Replan == nil {
				// Given once mid-turn: until it is asked, the model's calls are held with it (holdCall).
				if r, q, fresh := relayToGive(st, c.Now, w.Timeout); fresh {
					v = relayGiven(*r, q)
				} else if r == nil {
					dl = take(st, c.Now)
					news = takeNews(st, s)
					v = contextValves(st, valve.FromEnv(c.Env), w)
				}
			}
			notes, ann = announceNotes(st), announced(st)
		}
		dropped, _ = vanished(st, c.Now, false) // a proposal's question that failed or was denied went unanswered
		dropped = append(dropped, relayVanished(st, c.Now)...)
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	h.onScreen(c, s, func(st *state.State) { st.Run.Dialogs.CloseExact(s.Agent, tool, dialogKey(c.Input)) })
	for _, e := range dropped {
		s.Event(e.kind, e.fields)
	}
	if v.event == "" && notes == "" && ann == "" && !dl.any() && !news.any() && owed == nil {
		return Result{}, nil
	}
	tell := v.tell
	if dl.any() {
		tell = strings.TrimSpace(dl.text(s) + "\n\n" + tell)
		dl.event(s, "tool")
	}
	if news.any() {
		tell = strings.TrimSpace(news.text + "\n\n" + tell)
		news.event(s, "tool")
	}
	if owed != nil {
		tell = strings.TrimSpace(h.briefed(c, s, st, *owed, "tool") + "\n\n" + tell)
	}
	out := map[string]any{}
	if msg := lines(ann, notes, dl.says(), v.say); msg != "" {
		out["systemMessage"] = msg
	}
	if v.event != "" {
		s.Event(v.event, v.fields)
		for _, e := range v.also {
			s.Event(e.kind, e.fields)
		}
	}
	if tell != "" {
		// Hook feedback, not context: a model weighs text beside a tool result as possible injection and
		// may ignore it, but follows a blocking hook's reason (docs/research/workers.md). The tool has
		// already run, so nothing is redone.
		out["decision"], out["reason"] = "block", tell
	}
	return Result{Output: out}, nil
}

// answered acts on the answer to one of baton's own questions, found by its exact text, so what happens
// next depends on the answer itself, not on the model remembering to act on it. auto says baton
// answered it itself, because nobody else did.
func answered(st *state.State, c Context, auto bool, w decide.Words) valveAction {
	resp, _ := c.Input["tool_response"].(map[string]any)
	answers, _ := resp["answers"].(map[string]any)
	for q, a := range answers {
		answer, _ := a.(string)
		iq, own := lookupIssued(st, q)
		if !own {
			continue
		}
		if iq.kind == "context_warning" {
			st.Run.WarnQuestion, st.Run.WarnOwed = "", false // answered: asking it again would need a new warning
		}
		// Events are named as if the CLI had done it (resumed, paused), so the record reads the same.
		var event, say, tell string
		switch strings.TrimSpace(answer) {
		case "Continue":
			if iq.kind != "escalation" || state.Resume(st) != nil {
				continue
			}
			event, say, tell = "resumed", "baton: resumed", NudgePrefix+" The human chose Continue: baton has resumed the plan. Carry on with "+st.Current+"."
		case "Pause baton":
			if state.Pause(st) != nil {
				continue
			}
			event, say, tell = "paused", "baton: paused — the human is driving", NudgePrefix+" The human chose Pause baton: baton is paused. Wait for the human's instructions."
		case "Checkpoint now":
			if iq.kind != "context_warning" || st.Mode != state.ModeRunning {
				continue
			}
			st.CheckpointAsked = true
			event, say = "checkpoint_asked", "baton: checkpoint at the end of this step"
			tell = NudgePrefix + " The human chose Checkpoint now. " + w.FinishStep() + ", then run " +
				"`baton checkpoint --notes \"<where you are and what is left>\"` and end your turn. (The next stop is a checkpoint either way.)"
		case "Keep going":
			if iq.kind != "context_warning" {
				continue
			}
			event, say = "answered", "baton: keep going — Claude Code compacts on its own when the context is full"
			if auto {
				say = "baton: nobody answered, so baton chose Keep going — Claude Code compacts on its own when the context is full"
				tell = NudgePrefix + " Nobody answered the context question, so baton chose Keep going for the human. Carry on with " + st.Current + "."
			}
		default:
			continue
		}
		by := "human"
		if auto {
			by = "timeout"
		} else {
			st.Run.HumanAt = c.Now
		}
		return valveAction{event: event, fields: map[string]any{"answer": answer, "by": by}, say: say, tell: tell}
	}
	return valveAction{}
}

// valveAction is what a context valve or an answer does: an event to log (and any that follow it), a
// line for the human, words for the model.
type valveAction struct {
	event     string
	fields    map[string]any
	also      []event
	say, tell string
}

// contextValves runs the two mid-phase valves after a main-agent tool call. Past the checkpoint line,
// the model is asked to checkpoint at its next safe point, and asked again for every further tenth of
// the limit. Past the warning line (never in a worker), the model is asked to
// put a fixed question to the human: checkpoint now, or keep going and let Claude Code compact on its
// own. Each fires once, and re-arms only after the context falls a tenth of the window below its line,
// so a compaction that leaves the context high cannot start a loop. Subagents are never asked: they
// cannot end the main session's turn.
func contextValves(st *state.State, vs valve.Settings, w decide.Words) valveAction {
	cu := st.Run.Context
	if cu == nil {
		return valveAction{}
	}
	lim, used := vs.Limits(cu.WindowSize), cu.Used()
	if lim.Window == 0 {
		return valveAction{}
	}
	rearm := lim.Window / 10
	if st.Run.ContextNudged && used < lim.Checkpoint-rearm {
		st.Run.ContextNudged, st.Run.ContextNudgedAt = false, 0
	}
	if st.Run.ContextWarned && used < lim.Warn-rearm {
		st.Run.ContextWarned = false
	}
	if st.Mode != state.ModeRunning || st.CheckpointOwed || st.CheckpointDue || st.BoundaryOwed || st.Run.Compaction.InFlight() {
		return valveAction{}
	}
	pct := 100 * float64(used) / float64(lim.Window)
	switch {
	case lim.Warn > 0 && used >= lim.Warn && !st.Run.ContextWarned && !w.Worker:
		// Nobody watches a worker's session to answer the question: it is nudged to checkpoint instead.
		// The warning includes the checkpoint option, so the nudge has nothing left to say.
		st.Run.ContextWarned, st.Run.ContextNudged, st.Run.ContextNudgedAt = true, true, used
		q := warnQuestion(used, lim, vs.WarnTimeout)
		st.Run.WarnQuestion, st.Run.WarnOwed = q.Text, true
		return valveAction{
			event:  "context_warning",
			fields: map[string]any{"tokens": used, "warn": lim.Warn, "limit": lim.Window, "auto_compact": lim.AutoAt},
			say:    fmt.Sprintf("baton: context at %s of %s → asking you whether to checkpoint", valve.Tokens(used), valve.Tokens(lim.Window)),
			tell:   decide.ContextWarning(q.Call(), false),
		}
	case lim.Checkpoint > 0 && used >= lim.Checkpoint && !st.Run.ContextWarned &&
		(!st.Run.ContextNudged || used >= st.Run.ContextNudgedAt+rearm):
		// Asked again for every further tenth of the limit: one request is easy to put off.
		st.Run.ContextNudged, st.Run.ContextNudgedAt = true, used
		return valveAction{
			event:  "context_nudge",
			fields: map[string]any{"pct": math.Round(pct), "tokens": used, "checkpoint": lim.Checkpoint, "limit": lim.Window},
			say:    fmt.Sprintf("baton: context at %s of %s → checkpoint at the next safe point", valve.Tokens(used), valve.Tokens(lim.Window)),
			tell: fmt.Sprintf("%s Checkpoint due: the context holds %s tokens, %.0f%% of its %s limit. %s, then run "+
				"`baton checkpoint --notes \"<where you are and what is left>\"` and end your turn: baton compacts the context and you continue this phase from your notes. "+
				"(If the phase is already complete, run `baton done` instead.)", NudgePrefix, valve.Tokens(used), pct, valve.Tokens(lim.Window), w.FinishStep()),
		}
	}
	return valveAction{}
}

// warnQuestion is the context question, which the model is told to put to the human word for word.
// AskUserQuestion reaches every device the human uses (and the watchdog pushes a notification if it
// goes unanswered).
func warnQuestion(used int, lim valve.Limits, timeout time.Duration) issued {
	if timeout <= 0 {
		timeout = valve.DefaultWarnTimeout
	}
	q := fmt.Sprintf("baton: the context holds %s tokens, past the %s warning line. Claude Code compacts it on its own at about %s. Checkpoint now? "+
		"If nobody answers within %s, baton picks Keep going.",
		valve.Tokens(used), valve.Tokens(lim.Warn), valve.Tokens(lim.AutoAt), decide.Spell(timeout))
	return issued{"context_warning", decide.Question{Text: q, Options: decide.WarnOptions}}
}

// notification records Claude Code's notifications. One of them is authoritative about the session:
// idle_prompt ("Claude is waiting for your input") fires about a minute after a turn ends, and never
// while a dialog is on screen. It corrects a turn or dialog that baton still thinks is open because the
// hook that would have closed it never ran (the Stop hook does not run for an interrupted turn). It is the
// session's: one an agent's hook brings says nothing about the session's turn.
func (h *handlers) notification(c Context) (Result, error) {
	kind := str(c.Input, "notification_type")
	var fixed bool
	var events []event
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if kind == "idle_prompt" && str(c.Input, "agent_id") == "" && (st.Run.TurnOpen || st.Run.Dialogs.AnyOpen()) {
			st.Run.TurnOpen, fixed = false, true
			st.Run.Dialogs.Clear()
			events, _ = vanished(st, c.Now, false)
		}
		return nil
	})
	if err == nil {
		s.Event("notification", map[string]any{"type": kind, "corrected": fixed})
		for _, e := range events {
			s.Event(e.kind, e.fields)
		}
	}
	return Result{}, ok(err)
}

// subagentStart counts a subagent the loop's model started. A phase agent's own start is its sub-run's
// (agentStart), and none of the lead's (nor is one baton has not linked yet: its type says what it is).
func (h *handlers) subagentStart(c Context) (Result, error) {
	if s, err := h.phaseAgent(c); s != nil || err != nil {
		if err != nil {
			return Result{}, ok(err)
		}
		return h.agentStart(c, s)
	}
	_, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if c.Agent != "" && str(c.Input, "agent_type") != state.PhaseAgent {
			st.Run.Subagents++
		}
		return nil
	})
	return Result{}, ok(err)
}

// subagentStop counts a subagent of the loop's model ended, and lets go of a plain worktree baton made for
// it (plainStopped). A phase agent's stop is its sub-run's own (agentStop).
func (h *handlers) subagentStop(c Context) (Result, error) {
	if s, err := h.phaseAgent(c); s != nil || err != nil {
		if err != nil {
			return Result{}, ok(err)
		}
		return h.agentStop(c, s)
	}
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if c.Agent == "" {
			return nil
		}
		if st.Run.Subagents > 0 {
			st.Run.Subagents--
		}
		st.Run.Dialogs.CloseAgent(c.Agent) // a subagent that has stopped asks nothing, and runs nothing
		st.Run.ToolsEnded(c.Agent)
		return nil
	})
	if err == nil && c.Agent != "" {
		h.plainStopped(c, s)
	}
	return Result{}, ok(err)
}

// phaseAgent is the sub-run of the phase agent a hook comes from (OpenRun linked it), or nil for any other
// hook.
func (h *handlers) phaseAgent(c Context) (*state.Store, error) {
	if c.Agent != "" || str(c.Input, "agent_id") == "" {
		return nil, nil
	}
	s, err := h.d.Open(c)
	if err != nil || s.Agent == "" {
		return nil, err
	}
	return s, nil
}

// recordStop notes what every Stop tells us; the decision about the stop is made in stop.go.
func recordStop(st *state.State, c Context) {
	st.Run.TurnOpen, st.Run.LastStop, st.Run.History = false, c.Now, true
	st.Run.Dialogs.CloseMain() // the main turn is over; background subagents may still be asking
	st.Run.ToolsEnded("")
	st.Run.SetBackground(tasks(c.Input["background_tasks"]), c.Now)
	// Foreground subagents cannot outlive the turn, and a subagent that ends in an API error never fires
	// SubagentStop: recount from the list of what is really still running.
	st.Run.Subagents = 0
	for _, t := range st.Run.Background {
		if (t.Type == "subagent" || t.Type == "workflow") && t.AgentType != state.PhaseAgent {
			st.Run.Subagents++
		}
	}
	if crons, okc := c.Input["session_crons"].([]any); okc {
		st.Run.Crons = len(crons)
	} else {
		st.Run.Crons = 0
	}
}

// readRefusal reads a transcript for the refusal that ended its last turn (a variable, so a test can make it
// fail).
var readRefusal = transcript.Refused

// refusalIn is the refusal that ended the last turn of the transcript at path, or nil. Reading is best
// effort: whatever goes wrong, even a panic, leaves the failure as Claude Code reported it.
func refusalIn(path string) (r *transcript.Refusal) {
	defer func() {
		if recover() != nil {
			r = nil
		}
	}()
	r, _ = readRefusal(path)
	return r
}

// stopFailure records a turn that ended in an API error: the loop's own (the lead's, or a phase agent's in
// its sub-run), or a subagent's of its model, which ends that subagent (no SubagentStop follows) and not
// the turn.
func (h *handlers) stopFailure(c Context) (Result, error) {
	e := state.StopError{Error: str(c.Input, "error"), Details: str(c.Input, "error_details"), At: c.Now}
	if e.Error == "invalid_request" && str(c.Input, "agent_id") == "" {
		// Claude Code reports a turn the model's safeguards declined as a request it could not send; only
		// the transcript says which it was (docs/research/recovery.md).
		if r := refusalIn(str(c.Input, "transcript_path")); r != nil {
			e.Error, e.Category, e.Details = state.ErrRefusal, r.Category, r.Explanation
		}
	}
	var events []event
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if c.Agent != "" {
			if st.Run.Subagents > 0 {
				st.Run.Subagents--
			}
			st.Run.Dialogs.CloseAgent(c.Agent)
			st.Run.ToolsEnded(c.Agent)
			return nil
		}
		st.Run.TurnOpen, st.Run.LastError, st.Run.Subagents, st.Run.History = false, &e, 0, true
		st.Run.Dialogs.CloseMain()
		st.Run.ToolsEnded("")
		events, _ = vanished(st, c.Now, false)
		return nil
	})
	if err == nil {
		h.onScreen(c, s, func(st *state.State) { st.Run.Dialogs.CloseAgent(s.Agent) })
		if c.Agent != "" {
			h.plainStopped(c, s) // a subagent that ends in an API error has no SubagentStop
		}
		fields := map[string]any{"error": e.Error}
		if e.Error == state.ErrRefusal {
			fields["category"] = e.Category
		}
		if c.Agent != "" {
			fields["agent"] = c.Agent
		}
		s.Event("stop_failure", fields)
		for _, e := range events {
			s.Event(e.kind, e.fields)
		}
	}
	return Result{}, ok(err)
}

func tasks(v any) []state.Task {
	list, _ := v.([]any)
	var out []state.Task
	for _, x := range list {
		m, _ := x.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, state.Task{ID: str(m, "id"), Type: str(m, "type"), Status: str(m, "status"), Description: str(m, "description"), AgentType: str(m, "agent_type")})
	}
	return out
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
