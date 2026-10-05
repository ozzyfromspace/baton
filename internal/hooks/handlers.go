package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/plan"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/valve"
)

// NudgePrefix starts every message baton types or sends into the session, so the hooks can tell
// baton's prompts from the human's.
const NudgePrefix = "[baton]"

// Deps gives handlers access to the project's state.
type Deps struct {
	// Open returns the store for the session's .baton directory, given the hook's environment.
	Open func(env func(string) string) (*state.Store, error)
}

// Handlers returns baton's handler for each hook event.
func Handlers(d Deps) map[string]Handler {
	h := &handlers{d}
	return map[string]Handler{
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
		"PreCompact":         h.preCompact,
		"PostCompact":        h.postCompact,
		"PostCompactRewake":  h.postCompactRewake,
		"SessionStartRewake": h.sessionStartRewake,
	}
}

type handlers struct{ d Deps }

// update applies fn to the state if this session owns the project; a non-owner session is dormant.
func (h *handlers) update(c Context, fn func(st *state.State, s *state.Store) error) (*state.Store, state.State, error) {
	s, err := h.d.Open(c.Env)
	if err != nil {
		return nil, state.State{}, err
	}
	instance := c.Env("BATON_INSTANCE")
	var owner bool
	st, err := s.Update(func(st *state.State) error {
		if owner = st.IsOwner(instance, c.Now); !owner {
			return errNotOwner
		}
		st.Run.LastActivity = c.Now
		if sid := str(c.Input, "session_id"); sid != "" {
			st.Run.SessionID = sid
		}
		if pm := str(c.Input, "permission_mode"); pm != "" {
			st.Run.PermissionMode = pm
		}
		return fn(st, s)
	})
	if err == errNotOwner {
		return nil, st, errNotOwner
	}
	return s, st, err
}

var errNotOwner = fmt.Errorf("this session does not own the project")

func ok(err error) error {
	if err == errNotOwner {
		return nil
	}
	return err
}

// preToolUse does two things.
//
// It lets baton's own CLI through deterministically. A plain `baton …` command is allowed outright, so
// neither a permission prompt nor the auto-mode classifier can park an unattended run on it. One whose
// arguments the shell would rewrite (backticks or $ inside double quotes: notes often quote code) is
// refused with the fix, because it would run those as commands and mangle the notes.
//
// And it holds the model at a compaction it owes. After `baton done` or `baton checkpoint`, the turn
// must end so the host can compact; a model that carries on would start the next phase (or keep going)
// in the old context. So until then every main-agent tool call except baton's own CLI is denied, with
// the reason. Subagents are not held: they cannot end the main turn.
func (h *handlers) preToolUse(c Context) (Result, error) {
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
	var owed string
	var refuseQuestion string
	var pending []issued
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if st.Mode == state.ModeRunning && str(c.Input, "tool_name") == "AskUserQuestion" {
			// A human-started turn may ask the human anything, except a question passed off as baton's:
			// one that starts "baton:" while baton has a question waiting must be that question, word
			// for word, or the host will not recognize it and the run waits on an answer nobody gives.
			if _, own := matchIssued(st, c.Input); !own {
				pending = issuedQuestions(st)
				if st.Run.TurnBy != "human" || len(pending) > 0 && claimsBaton(c.Input) {
					refuseQuestion = questionRefusal(pending)
					return nil
				}
			}
		}
		if st.Mode != state.ModeRunning || str(c.Input, "agent_id") != "" || isBatonCommand(c.Input) {
			return nil
		}
		switch {
		case st.BoundaryOwed:
			owed = "the phase is done and baton must compact the context before " + st.Current + " begins"
		case st.CheckpointOwed:
			owed = "you asked for a checkpoint and baton must compact the context first"
		}
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	if refuseQuestion != "" {
		s.Event("question_refused", map[string]any{"questions": len(questions(c.Input)), "issued": len(pending)})
		return permission("deny", refuseQuestion), nil
	}
	if owed == "" {
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
	return permission("deny", NudgePrefix+" End your turn now: "+owed+". Do not start further work in this turn."), nil
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
func questionRefusal(pending []issued) string {
	if len(pending) > 0 {
		return NudgePrefix + " While a plan runs, the only question that may be put to the human is the one baton issued, exactly as issued. " +
			"To ask it, " + pending[0].Call() + ". For anything else, decide yourself: pick the most reasonable option, note the assumption, and carry on."
	}
	return NudgePrefix + " baton is running this plan unattended, so a question would stop the run until someone answers it. " +
		"Decide yourself: pick the most reasonable option, note the assumption (in your commit message and your `baton done` notes), and carry on. " +
		"If you truly cannot continue without the human, run `baton blocked \"<your question>\"` and end your turn: baton will notify them."
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
	var pl plan.Plan
	var havePlan bool
	s, st, err := h.update(c, func(st *state.State, s *state.Store) error {
		st.Run.Ended = nil
		if source != "compact" {
			st.Run.TurnOpen, st.Run.Subagents = false, 0
			st.Run.Dialogs.Clear()
		}
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	s.Event("session_start", map[string]any{"source": source})
	if p, err := s.LoadPlan(); err == nil && st.Mode != state.ModeIdle {
		pl, havePlan = p, true
	}
	if source == "compact" {
		return h.afterCompaction(c, s)
	}
	if !havePlan {
		return Result{Output: map[string]any{"systemMessage": "baton: hosting this session (no plan attached — /baton plan or /baton attach)"}}, nil
	}
	return Result{Output: map[string]any{
		"systemMessage": "baton: hosting · " + progressLine(pl, st),
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": Primer(pl, st),
		},
	}}, nil
}

// sessionStartRewake runs in the background when a session resumes. After an elevation it wakes the model
// with what it was about to do (BATON_PENDING, set by the relaunch), exactly once.
func (h *handlers) sessionStartRewake(c Context) (Result, error) {
	pending := strings.TrimSpace(c.Env("BATON_PENDING"))
	if pending == "" || str(c.Input, "source") != "resume" {
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

// Primer tells the model, at the start of a hosted session, how baton expects it to report progress.
func Primer(pl plan.Plan, st state.State) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This session is hosted by baton, which runs the attached plan %q phase by phase and compacts the context between phases. ", pl.Title)
	switch st.Mode {
	case state.ModeComplete:
		b.WriteString("Every phase of the plan is done.")
		return b.String()
	case state.ModePaused:
		b.WriteString("baton is paused: the human is driving; do not report phase progress unless asked. ")
	}
	if i := pl.Index(st.Current); i >= 0 {
		fmt.Fprintf(&b, "Current phase: %s — %s (see %s). ", st.Current, pl.Phases[i].Title, pl.File)
	}
	b.WriteString("Report progress only with these commands (Bash tool): " +
		"when the current phase is complete and committed, run `baton done <phase> --notes \"<what later phases need to know>\"` and end your turn — baton then compacts the context and starts the next phase with a fresh brief. " +
		"If you cannot continue without the human, run `baton blocked \"<why>\"` and end your turn. " +
		"If you must wait for something outside you (a build, a deploy, background work), run `baton waiting \"<what>\" --until <duration>` (at most 2h): background work alone is not a status. " +
		"Keep backticks and $ out of double-quoted notes (the shell would run them), or use single quotes. " +
		"The plan runs unattended, so do not ask the human questions (AskUserQuestion): decide, note your assumption, and carry on, or run `baton blocked` if you truly cannot. " +
		"In a long phase, at a safe point (work committed), `baton checkpoint --notes \"<where you are>\"` compacts mid-phase. " +
		"Never type /compact yourself, and don't stop between phases without one of these commands: baton will ask you why. " +
		"(If the human wants to talk instead of running the plan, they can run /baton pause.)")
	return b.String()
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
	if st.Mode != state.ModeRunning {
		line += " · " + st.Mode
	}
	return line
}

func (h *handlers) sessionEnd(c Context) (Result, error) {
	reason := str(c.Input, "reason")
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		st.Run.Ended = &state.Ended{Reason: reason, At: c.Now}
		st.Run.TurnOpen = false
		if st.Mode == state.ModeRunning && !deliberateEnd[reason] {
			notice(st, "session_ended", "the session ended mid-plan ("+reason+")", c.Now)
		}
		return nil
	})
	if err == nil {
		s.Event("session_end", map[string]any{"reason": reason})
	}
	return Result{}, ok(err)
}

// deliberateEnd are session-end reasons that mean the human chose to end the session.
var deliberateEnd = map[string]bool{"prompt_input_exit": true, "clear": true, "logout": true}

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
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		st.Run.TurnOpen, st.Run.TurnBy, st.Run.TurnStarted = true, by, c.Now
		st.Run.Dialogs.CloseMain() // a prompt was submitted, so no dialog of the main agent is on screen
		if by == "human" {
			// The human is engaged: whatever baton escalated, they have it now.
			st.Run.Progress()
			st.Run.Escalation = nil
		}
		return nil
	})
	if err == nil {
		s.Event("turn_started", map[string]any{"by": by})
	}
	return Result{}, ok(err)
}

// permissionRequest records a dialog joining Claude Code's queue. It fires when the dialog is requested,
// which is not when it is shown: a dialog requested earlier is shown first.
func (h *handlers) permissionRequest(c Context) (Result, error) {
	tool, agent := str(c.Input, "tool_name"), str(c.Input, "agent_id")
	kind := ""
	var open int
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if tool == "AskUserQuestion" && agent == "" {
			if iq, own := matchIssued(st, c.Input); own {
				kind = iq.kind // baton's own question, exactly as issued: the only kind the host may answer
			}
		}
		st.Run.Dialogs.Open(state.Dialog{Tool: tool, Agent: agent, Key: dialogKey(c.Input), Since: c.Now, Kind: kind})
		open = len(st.Run.Dialogs)
		return nil
	})
	if err == nil {
		fields := map[string]any{"tool": tool, "dialog": kind, "open": open}
		if agent != "" {
			fields["agent"] = agent
		}
		s.Event("dialog_open", fields)
	}
	return Result{}, ok(err)
}

// dialogKey identifies a tool call across its PermissionRequest and its result, which have no
// tool_use_id in common. A question is known by its question texts (its result adds the answers to its
// input); any other call by a hash of its input, which is the same in both (measured for Bash, Write,
// Edit and Read).
func dialogKey(in map[string]any) string {
	if str(in, "tool_name") == "AskUserQuestion" {
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
	tool := str(c.Input, "tool_name")
	var v valveAction
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		closed, _ := st.Run.Dialogs.CloseExact(str(c.Input, "agent_id"), tool, dialogKey(c.Input))
		if c.Event == "PostToolUse" && str(c.Input, "agent_id") == "" {
			if tool == "AskUserQuestion" {
				v = answered(st, c, !closed.AutoAnswered.IsZero())
			}
			if v.event == "" {
				v = contextValves(st, valve.FromEnv(c.Env))
			}
		}
		return nil
	})
	if err != nil || v.event == "" {
		return Result{}, ok(err)
	}
	s.Event(v.event, v.fields)
	return Result{Output: map[string]any{
		"systemMessage":      v.say,
		"hookSpecificOutput": map[string]any{"hookEventName": "PostToolUse", "additionalContext": v.tell},
	}}, nil
}

// answered acts on the answer to one of baton's own questions, found by its exact text, so what happens
// next depends on the answer itself, not on the model remembering to act on it. auto says baton
// answered it itself, because nobody else did.
func answered(st *state.State, c Context, auto bool) valveAction {
	resp, _ := c.Input["tool_response"].(map[string]any)
	answers, _ := resp["answers"].(map[string]any)
	for q, a := range answers {
		answer, _ := a.(string)
		iq, own := lookupIssued(st, q)
		if !own {
			continue
		}
		if iq.kind == "context_warning" {
			st.Run.WarnQuestion = "" // answered: asking it again would need a new warning
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
			tell = NudgePrefix + " The human chose Checkpoint now. Finish the step you are on and commit, then run " +
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
		}
		return valveAction{event: event, fields: map[string]any{"answer": answer, "by": by}, say: say, tell: tell}
	}
	return valveAction{}
}

// valveAction is what a context valve does: an event to log, a line for the human, words for the model.
type valveAction struct {
	event     string
	fields    map[string]any
	say, tell string
}

// contextValves runs the two mid-phase valves after a main-agent tool call. Past the checkpoint line,
// the model is asked to checkpoint at its next safe point, and asked again for every further tenth of
// the limit. Past the warning line, the model is asked to
// put a fixed question to the human: checkpoint now, or keep going and let Claude Code compact on its
// own. Each fires once, and re-arms only after the context falls a tenth of the window below its line,
// so a compaction that leaves the context high cannot start a loop. Subagents are never asked: they
// cannot end the main session's turn.
func contextValves(st *state.State, vs valve.Settings) valveAction {
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
	if st.Mode != state.ModeRunning || st.CheckpointOwed || st.BoundaryOwed || st.Run.Compaction.InFlight() {
		return valveAction{}
	}
	pct := 100 * float64(used) / float64(lim.Window)
	switch {
	case lim.Warn > 0 && used >= lim.Warn && !st.Run.ContextWarned:
		// The warning includes the checkpoint option, so the nudge has nothing left to say.
		st.Run.ContextWarned, st.Run.ContextNudged, st.Run.ContextNudgedAt = true, true, used
		q := warnQuestion(used, lim, vs.WarnTimeout)
		st.Run.WarnQuestion = q.Text
		return valveAction{
			event:  "context_warning",
			fields: map[string]any{"tokens": used, "warn": lim.Warn, "limit": lim.Window, "auto_compact": lim.AutoAt},
			say:    fmt.Sprintf("baton: context at %s of %s → asking you whether to checkpoint", valve.Tokens(used), valve.Tokens(lim.Window)),
			tell:   NudgePrefix + " Context warning. Before anything else, " + q.Call() + ". baton acts on the answer itself; then follow what it tells you.",
		}
	case lim.Checkpoint > 0 && used >= lim.Checkpoint && !st.Run.ContextWarned &&
		(!st.Run.ContextNudged || used >= st.Run.ContextNudgedAt+rearm):
		// Asked again for every further tenth of the limit: one request is easy to put off.
		st.Run.ContextNudged, st.Run.ContextNudgedAt = true, used
		return valveAction{
			event:  "context_nudge",
			fields: map[string]any{"pct": math.Round(pct), "tokens": used, "checkpoint": lim.Checkpoint, "limit": lim.Window},
			say:    fmt.Sprintf("baton: context at %s of %s → checkpoint at the next safe point", valve.Tokens(used), valve.Tokens(lim.Window)),
			tell: fmt.Sprintf("%s Checkpoint due: the context holds %s tokens, %.0f%% of its %s limit. Finish the step you are on and commit, then run "+
				"`baton checkpoint --notes \"<where you are and what is left>\"` and end your turn: baton compacts the context and you continue this phase from your notes. "+
				"(If the phase is already complete, run `baton done` instead.)", NudgePrefix, valve.Tokens(used), pct, valve.Tokens(lim.Window)),
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
// hook that would have closed it never ran (the Stop hook does not run for an interrupted turn).
func (h *handlers) notification(c Context) (Result, error) {
	kind := str(c.Input, "notification_type")
	var fixed bool
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if kind == "idle_prompt" && (st.Run.TurnOpen || st.Run.Dialogs.AnyOpen()) {
			st.Run.TurnOpen, fixed = false, true
			st.Run.Dialogs.Clear()
		}
		return nil
	})
	if err == nil {
		s.Event("notification", map[string]any{"type": kind, "corrected": fixed})
	}
	return Result{}, ok(err)
}

func (h *handlers) subagentStart(c Context) (Result, error) {
	_, _, err := h.update(c, func(st *state.State, _ *state.Store) error { st.Run.Subagents++; return nil })
	return Result{}, ok(err)
}

func (h *handlers) subagentStop(c Context) (Result, error) {
	_, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if st.Run.Subagents > 0 {
			st.Run.Subagents--
		}
		st.Run.Dialogs.CloseAgent(str(c.Input, "agent_id")) // a subagent that has stopped asks nothing
		return nil
	})
	return Result{}, ok(err)
}

// recordStop notes what every Stop tells us; the decision about the stop is made in stop.go.
func recordStop(st *state.State, c Context) {
	st.Run.TurnOpen, st.Run.LastStop = false, c.Now
	st.Run.Dialogs.CloseMain() // the main turn is over; background subagents may still be asking
	st.Run.Background = tasks(c.Input["background_tasks"])
	// Foreground subagents cannot outlive the turn, and a subagent that ends in an API error never fires
	// SubagentStop: recount from the list of what is really still running.
	st.Run.Subagents = 0
	for _, t := range st.Run.Background {
		if t.Type == "subagent" || t.Type == "workflow" {
			st.Run.Subagents++
		}
	}
	if crons, okc := c.Input["session_crons"].([]any); okc {
		st.Run.Crons = len(crons)
	} else {
		st.Run.Crons = 0
	}
}

func (h *handlers) stopFailure(c Context) (Result, error) {
	e := state.StopError{Error: str(c.Input, "error"), Details: str(c.Input, "error_details"), At: c.Now}
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		st.Run.TurnOpen, st.Run.LastError, st.Run.Subagents = false, &e, 0
		st.Run.Dialogs.CloseMain()
		return nil
	})
	if err == nil {
		s.Event("stop_failure", map[string]any{"error": e.Error})
	}
	return Result{}, ok(err)
}

// preCompact acknowledges a manual compaction: the one baton typed, or one the human typed while a
// compaction is owed (which satisfies it, so baton never compacts twice in a row).
//
// An automatic PreCompact proves nothing yet. Claude Code precomputes its summary in the background,
// from about 80% of the window, and fires PreCompact then; the compaction itself may come much later or
// never (docs/research/context-window.md). Treating it as the owed compaction would leave baton waiting
// for a compaction that is not happening. Only SessionStart(compact) proves one happened, so an
// automatic compaction is adopted there (afterCompaction).
func (h *handlers) preCompact(c Context) (Result, error) {
	trigger := str(c.Input, "trigger")
	var cp state.Compaction
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		if trigger != "manual" {
			cp = st.Run.Compaction
			return nil
		}
		comp := &st.Run.Compaction
		switch {
		case comp.Status == state.CompactQueued || comp.Status == state.CompactTyped:
			comp.ByBaton = true
		case st.BoundaryOwed || st.CheckpointOwed:
			adoptOwed(st, c.Now)
		default:
			comp.ByBaton = false
		}
		if comp.ByBaton {
			comp.Status = state.CompactActive
		}
		comp.Trigger, comp.Started = trigger, c.Now
		cp = *comp
		return nil
	})
	if err != nil {
		return Result{}, ok(err)
	}
	if trigger != "manual" {
		s.Event("precompact_auto", map[string]any{"owed": cp.Status})
		return Result{}, nil
	}
	s.Event("compact_started", map[string]any{"trigger": trigger, "by_baton": cp.ByBaton, "epoch": cp.Epoch, "reason": cp.Reason})
	return Result{}, nil
}

// adoptOwed makes a compaction baton did not type count as the one it owes: a new epoch, underway.
func adoptOwed(st *state.State, now time.Time) {
	comp := &st.Run.Compaction
	if !comp.InFlight() {
		comp.Epoch++
	}
	comp.ByBaton, comp.Reason, comp.Status, comp.Started, comp.Tries = true, reasonOwed(st), state.CompactActive, now, 0
}

func reasonOwed(st *state.State) string {
	if st.BoundaryOwed {
		return "boundary"
	}
	return "checkpoint"
}

func (h *handlers) postCompact(c Context) (Result, error) {
	var cp state.Compaction
	s, _, err := h.update(c, func(st *state.State, _ *state.Store) error {
		comp := &st.Run.Compaction
		if comp.ByBaton && (comp.Status == state.CompactActive || comp.Status == state.CompactFailed) {
			// Failed means baton gave up waiting; a compaction that finished late still counts.
			comp.Status = state.CompactDone
			if e := st.Run.Escalation; e != nil && e.Kind == "compaction_failed" {
				st.Run.Escalation = nil
			}
		}
		comp.Finished = c.Now
		cp = *comp
		return nil
	})
	if err == nil {
		s.Event("compact_finished", map[string]any{"trigger": cp.Trigger, "by_baton": cp.ByBaton, "epoch": cp.Epoch})
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
		out = append(out, state.Task{ID: str(m, "id"), Type: str(m, "type"), Status: str(m, "status"), Description: str(m, "description")})
	}
	return out
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
