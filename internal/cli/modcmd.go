package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	goio "io"
	"time"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/config"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/hooks"
	"github.com/ozzyfromspace/baton/internal/state"
	"github.com/ozzyfromspace/baton/internal/valve"
	"github.com/ozzyfromspace/baton/internal/version"
)

// baton's mod (plugin/hooks/baton.js) reports what it sees and does by running `baton mod report` with
// one report as JSON on stdin (internal/bridge), and reads the reply from stdout. It is not a Claude Code
// hook, but like one it never fails loudly: whatever goes wrong, it replies, and exits 0.

func init() {
	registerHidden("mod", cmdMod)
}

// maxReport bounds what is read of a report.
const maxReport = 1 << 20

// modReply is what the mod reads back. OK: the report is recorded, or never will be (Ignored says why),
// and the mod lets it go. Otherwise Retry: it keeps the report and tries again; Stale: it says hello
// again first; neither: baton works with no mod here, and the mod does nothing more.
type modReply struct {
	OK      bool   `json:"ok"`
	Retry   bool   `json:"retry,omitempty"`
	Stale   bool   `json:"stale,omitempty"`
	Why     string `json:"why,omitempty"`
	Ignored string `json:"ignored,omitempty"`
	// A hello's: the run, the bridge's directory the mod polls, the number of the mod's last report
	// recorded (its next follows it), the agent type it registers for phases, and the words of baton's
	// board (`baton board`), which it draws.
	Run   string        `json:"run,omitempty"`
	Dir   string        `json:"dir,omitempty"`
	Seen  int           `json:"seen,omitempty"`
	Agent *modAgentType `json:"agent,omitempty"`
	Board *modBoard     `json:"board,omitempty"`
	// An asking's: how long the question may wait before the mod asks again (WaitMs), or, with none, the
	// answer it gives the question now (Answer); with neither, baton never answers it, and the human does.
	WaitMs int64  `json:"wait_ms,omitempty"`
	Answer string `json:"answer,omitempty"`
}

// modBoard is what the mod says about baton's board: the line of its own command (which opens it while
// Claude works) in Claude Code's list, and its pane's title.
type modBoard struct {
	Description string `json:"description"`
	Title       string `json:"title"`
}

type modAgentType struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

func cmdMod(args []string, io IO) int {
	if len(args) != 1 || args[0] != "report" {
		return fail(io, "usage: baton mod report (baton's mod runs it, with a report on stdin)")
	}
	json.NewEncoder(io.Out).Encode(safeModReport(io))
	return 0
}

// modReport records one report (a variable, so a test can make it panic).
var modReport = recordModReport

func safeModReport(io IO) (r modReply) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(io.Err, "baton: mod report failed open after a panic: %v\n", p)
			r = modReply{Retry: true, Why: fmt.Sprintf("baton failed: %v", p)}
		}
	}()
	return modReport(io)
}

func recordModReport(io IO) modReply {
	if !hosted(io) {
		return modReply{Why: "this session is not hosted by baton"}
	}
	data, rerr := goio.ReadAll(goio.LimitReader(io.In, maxReport+1))
	var rep bridge.Report
	perr := json.Unmarshal(data, &rep)
	p, err := project(io)
	if err != nil {
		return modReply{Why: err.Error()}
	}
	inst := instance(io)
	s, ok := p.Lookup(rep.Session, inst)
	if !ok {
		return modReply{Retry: true, Why: "this session has no run yet"}
	}
	if why := malformed(data, rerr, perr, rep); why != "" {
		s.Event("mod_report_bad", map[string]any{"error": why, "bytes": len(data)})
		return modReply{OK: true, Ignored: why}
	}
	rep.Instance = inst // the claude the mod runs in is the one this host instance runs
	b := bridge.Open(s.Dir, io.Now, func(kind string, fields map[string]any) { s.Event(kind, fields) })
	b.Drop = decide.ReportKept
	switch rep.Kind {
	case bridge.Hello:
		return modHello(io, s, b, rep)
	case bridge.Asking:
		return modAsking(io, s, b, rep)
	case bridge.Deadline:
		// Recorded in the lead's state before the report is (Settle does nothing twice), so that a failure
		// leaves the mod to send it again.
		if rec, err := b.Peek(); err != nil || !rec.Hello.Ready(inst) {
			return modReply{Stale: true, Why: "say hello first"}
		}
		if _, err := hooks.Settle(s, rep.Question, rep.Answer, io.Now(), io.Env); err != nil {
			return modReply{Retry: true, Why: err.Error()}
		}
	}
	a, err := b.Accept(rep)
	switch {
	case errors.Is(err, bridge.ErrStale):
		return modReply{Stale: true, Why: "say hello first"}
	case err != nil:
		return modReply{Retry: true, Why: err.Error()}
	case a.Dup:
		return modReply{OK: true, Ignored: "recorded already"}
	}
	// The mod's word on a compaction of the lead is counted as it comes, with the bridge's lock let go: a
	// proof of it begun or over (state.Report), or that a try at it did not compact (state.Declined).
	now := io.Now()
	switch {
	case rep.Kind == bridge.Ack && a.Acked != nil && a.Acked.Kind == bridge.Compact:
		// The mod started the compaction baton asked for, or Claude Code refused it at once. One the mod was
		// reloaded in the middle of may or may not have run: its proof, if it did, says so.
		switch {
		case rep.OK:
			reported(s, state.Ran(a.Acked.ID, a.Acked.Epoch, false, now), now)
		case !rep.Interrupted:
			declined(s, a.Acked.Epoch, a.Acked.ID, rep.Error, now)
		}
	case rep.Kind == bridge.Compacted && rep.Command != 0:
		// The compaction the mod ran for baton's command is over, or never happened.
		switch {
		case rep.Error != "":
			declined(s, rep.Epoch, rep.Command, rep.Error, now)
		case rep.Skipped:
			why := decide.CompactVetoed
			if rep.Result != "" {
				why += ": " + rep.Result // the hook's reason
			}
			declined(s, rep.Epoch, rep.Command, why, now)
		default:
			reported(s, state.Ran(rep.Command, rep.Epoch, true, now), now)
		}
	case rep.Kind == bridge.Compacted && rep.Skipped:
		declined(s, 0, 0, decide.CompactVetoed, now)
	case (rep.Kind == bridge.Compacting || rep.Kind == bridge.Compacted) && !rep.Skipped:
		reported(s, state.Reported(rep.Kind == bridge.Compacted, rep.Trigger, now), now)
	}
	return modReply{OK: true}
}

// reported counts the mod's proof p of a compaction of the lead, begun or over (state.Report), and logs it.
func reported(s *state.Store, p state.Proof, now time.Time) {
	var changed, started bool
	var comp state.Compaction
	s.Update(func(st *state.State) error {
		was := st.Run.Compaction.Status
		changed = state.Report(st, p, now)
		comp = st.Run.Compaction
		started = changed && !p.Over() && was != state.CompactActive && comp.Status == state.CompactActive
		return nil
	})
	if changed {
		s.Event("compact_reported", map[string]any{"over": p.Over(), "trigger": p.Trigger()})
	}
	if started {
		// The start is proven here, so the compaction's end does not log it (state.After.Unseen): it is
		// logged as the hooks' proof of it would be.
		s.Event("compact_started", map[string]any{"trigger": comp.Trigger, "by_baton": comp.ByBaton, "epoch": comp.Epoch, "reason": comp.Reason, "proof": "mod"})
	}
}

// declined records that a try at the lead's compaction of epoch, the mod's for command (0: a /compact),
// did not compact it, for why (state.Declined), and logs it: the host asks again.
func declined(s *state.Store, epoch, command int, why string, now time.Time) {
	var changed bool
	s.Update(func(st *state.State) error {
		changed = state.Declined(st, epoch, command, why, now)
		return nil
	})
	if changed {
		fields := map[string]any{"error": why}
		if command != 0 {
			fields["command"], fields["epoch"] = command, epoch
		}
		s.Event("compact_declined", fields)
	}
}

// malformed says what is wrong with a report as read ("" if nothing is).
func malformed(data []byte, rerr, perr error, rep bridge.Report) string {
	switch {
	case rerr != nil:
		return "unreadable: " + rerr.Error()
	case len(data) > maxReport:
		return "too long"
	case perr != nil:
		return "not JSON: " + perr.Error()
	}
	switch rep.Kind {
	case bridge.Hello:
		return ""
	case bridge.Asking:
		if rep.Question == "" {
			return "asking names no question"
		}
		return ""
	case bridge.Ack:
		if rep.Command <= 0 {
			return rep.Kind + " names no command"
		}
	case bridge.Answered:
		if rep.Command <= 0 || rep.Question == "" {
			return "answered names no command or no question"
		}
	case bridge.Deadline:
		if rep.Question == "" || rep.Answer == "" {
			return "deadline names no question or no answer"
		}
	case bridge.Turn, bridge.Dropped:
		if rep.Agent == "" {
			return rep.Kind + " names no agent"
		}
	case bridge.Failed:
		if rep.Command <= 0 {
			return rep.Kind + " names no command"
		}
	case bridge.Compacting, bridge.Compacted:
	default:
		return fmt.Sprintf("no such report: %q", rep.Kind)
	}
	if rep.Seq <= 0 {
		return "not numbered"
	}
	return ""
}

// modAsking answers the mod's asking: how long the question of baton's it shows the human may wait, and
// what baton answers it then (hooks.Bound). Nothing is recorded but when the question was first seen in
// front of the human. A mod baton does not work with is told nothing: the human answers.
func modAsking(io IO, s *state.Store, b *bridge.Bridge, rep bridge.Report) modReply {
	if rec, err := b.Peek(); err != nil || !rec.Hello.Ready(rep.Instance) {
		return modReply{OK: true}
	}
	now := io.Now()
	warn := valve.FromEnv(io.Env).WarnTimeout
	if warn == 0 {
		warn = valve.DefaultWarnTimeout
	}
	w, err := hooks.Bound(s, rep.Question, now.Add(-time.Duration(rep.Elapsed)*time.Millisecond), now, warn, config.EscalationFromEnv(io.Env).Timeout)
	switch {
	case err != nil:
		return modReply{OK: true, WaitMs: 5000} // ask again in a moment
	case w.Answer == "":
		return modReply{OK: true}
	case w.For > 0:
		return modReply{OK: true, WaitMs: max(w.For.Milliseconds(), 1)}
	}
	return modReply{OK: true, Answer: w.Answer}
}

// modHello records the mod's hello. A mod of another protocol is recorded as refused, and does nothing
// more; one in a claude whose host does not own the run waits.
func modHello(io IO, s *state.Store, b *bridge.Bridge, rep bridge.Report) modReply {
	h := bridge.HelloInfo{Instance: rep.Instance, Session: rep.Session, Version: rep.Version, Protocol: rep.Protocol}
	if rep.Protocol != bridge.Protocol {
		h.Refused = decide.ModMismatch(rep.Version, version.Version)
		if _, err := b.SayHello(h); err != nil {
			return modReply{Retry: true, Why: err.Error()}
		}
		return modReply{Why: h.Refused}
	}
	st, err := s.Load()
	if err != nil {
		return modReply{Retry: true, Why: err.Error()}
	}
	if st.Owner != nil && !st.IsOwner(rep.Instance, io.Now()) {
		return modReply{Retry: true, Why: "another baton session drives this run"}
	}
	seen, err := b.SayHello(h)
	if err != nil {
		return modReply{Retry: true, Why: err.Error()}
	}
	return modReply{OK: true, Run: s.Ref, Dir: b.Dir, Seen: seen,
		Agent: &modAgentType{Name: "phase", Description: decide.PhaseAgentDescription, Prompt: decide.PhaseAgentPrompt},
		Board: &modBoard{Description: decide.BoardCommand, Title: decide.BoardTitle}}
}
