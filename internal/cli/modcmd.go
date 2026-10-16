package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	goio "io"
	"slices"
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
	// answer it gives the question now (Answer, for one of baton's; Answers, one per question by its text,
	// for a call of the lead model's own); with neither, baton never answers it, and the human does.
	WaitMs  int64             `json:"wait_ms,omitempty"`
	Answer  string            `json:"answer,omitempty"`
	Answers map[string]string `json:"answers,omitempty"`
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
		var err error
		if len(rep.Answers) > 0 {
			err = hooks.SettleOwn(s, askedOf(rep), rep.Answers, io.Now()) // a call of the lead model's own
		} else {
			_, err = hooks.Settle(s, rep.Question, rep.Answer, io.Now(), io.Env)
		}
		if err != nil {
			return modReply{Retry: true, Why: err.Error()}
		}
	case bridge.Ack, bridge.Compacting, bridge.Compacted:
		// Counted in the lead's state before the report is recorded, once (state.CountOnce), as a deadline's
		// is: a report process killed between the two leaves the mod to send it again, and nothing is lost.
		if r, ok := compactionFirst(io, s, b, rep); !ok {
			return r
		}
	}
	reportStep("bridge")
	a, err := b.Accept(rep)
	switch {
	case errors.Is(err, bridge.ErrStale):
		return modReply{Stale: true, Why: "say hello first"}
	case err != nil:
		return modReply{Retry: true, Why: err.Error()}
	case a.Dup:
		return modReply{OK: true, Ignored: "recorded already"}
	}
	return modReply{OK: true}
}

// reportStep is called at each step of recording a report that a killed report process could stop before:
// "state", the run's state counting it, and "bridge", the bridge recording it. A test makes it panic there.
var reportStep = func(step string) {}

// compactionFirst counts the mod's word on a compaction of the lead in report rep, if it says one, before
// the bridge records the report: a proof of it begun or over (state.Report), or that a try at it did not
// compact (state.Declined). An ack says one only of a compact command the bridge has open, which recording
// the ack closes; a report the bridge recorded already was counted before it was. It returns false, with
// the reply, when the report goes no further now.
//
// The bridge's record is read without its lock (Peek), so that a report waits on two locks at most, the
// run's and the bridge's: the record is replaced whole, and the mod acks a compact command a while after
// its record was written (it watches its call for a refusal first).
func compactionFirst(io IO, s *state.Store, b *bridge.Bridge, rep bridge.Report) (modReply, bool) {
	rec, err := b.Peek()
	switch {
	case err != nil:
		return modReply{Retry: true, Why: err.Error()}, false
	case !rec.Hello.Ready(rep.Instance):
		return modReply{Stale: true, Why: "say hello first"}, false
	case rep.Seq <= rec.Seen:
		return modReply{}, true // a repeat, which the bridge says it is
	}
	id := state.ModSeq{Instance: rep.Instance, Seq: rep.Seq, Kind: rep.Kind, Command: rep.Command}
	now := io.Now()
	switch {
	case rep.Kind == bridge.Ack:
		i := slices.IndexFunc(rec.Open, func(o bridge.Issued) bool { return o.ID == rep.Command })
		if i < 0 || rec.Open[i].Kind != bridge.Compact {
			return modReply{}, true
		}
		// The mod started the compaction baton asked for, or Claude Code refused it at once. One the mod was
		// reloaded in the middle of may or may not have run: its proof, if it did, says so.
		c := rec.Open[i]
		switch {
		case rep.OK:
			err = reported(s, id, state.Ran(c.ID, c.Epoch, false, now), now)
		case !rep.Interrupted:
			err = declined(s, id, c.Epoch, c.ID, rep.Error, now)
		}
	case rep.Kind == bridge.Compacted && rep.Command != 0:
		// The compaction the mod ran for baton's command is over, or never happened.
		switch {
		case rep.Error != "":
			err = declined(s, id, rep.Epoch, rep.Command, rep.Error, now)
		case rep.Skipped:
			why := decide.CompactVetoed
			if rep.Result != "" {
				why += ": " + rep.Result // the hook's reason
			}
			err = declined(s, id, rep.Epoch, rep.Command, why, now)
		default:
			err = reported(s, id, state.Ran(rep.Command, rep.Epoch, true, now), now)
		}
	case rep.Kind == bridge.Compacted && rep.Skipped:
		err = declined(s, id, 0, 0, decide.CompactVetoed, now)
	case !rep.Skipped: // compacting, or compacted with no command of baton's
		err = reported(s, id, state.Reported(rep.Kind == bridge.Compacted, rep.Trigger, now), now)
	}
	if err != nil {
		return modReply{Retry: true, Why: err.Error()}, false
	}
	return modReply{}, true
}

// reported counts the mod's proof p of a compaction of the lead, begun or over (state.Report), from its
// report id, once, and logs it.
func reported(s *state.Store, id state.ModSeq, p state.Proof, now time.Time) error {
	reportStep("state")
	var changed, started bool
	var comp state.Compaction
	_, err := s.Update(func(st *state.State) error {
		was := st.Run.Compaction.Status
		changed = state.CountOnce(st, id, func(st *state.State) bool { return state.Report(st, p, now) })
		comp = st.Run.Compaction
		started = changed && !p.Over() && was != state.CompactActive && comp.Status == state.CompactActive
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		s.Event("compact_reported", map[string]any{"over": p.Over(), "trigger": p.Trigger()})
	}
	if started {
		// The start is proven here, so the compaction's end does not log it (state.After.Unseen): it is
		// logged as the hooks' proof of it would be.
		s.Event("compact_started", map[string]any{"trigger": comp.Trigger, "by_baton": comp.ByBaton, "epoch": comp.Epoch, "reason": comp.Reason, "proof": "mod"})
	}
	return nil
}

// declined records that a try at the lead's compaction of epoch, the mod's for command (0: a /compact),
// did not compact it, for why (state.Declined), from the mod's report id, once, and logs it: the host
// asks again.
func declined(s *state.Store, id state.ModSeq, epoch, command int, why string, now time.Time) error {
	reportStep("state")
	var changed bool
	_, err := s.Update(func(st *state.State) error {
		changed = state.CountOnce(st, id, func(st *state.State) bool { return state.Declined(st, epoch, command, why, now) })
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		fields := map[string]any{"error": why}
		if command != 0 {
			fields["command"], fields["epoch"] = command, epoch
		}
		s.Event("compact_declined", fields)
	}
	return nil
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

// modAsking answers the mod's asking: how long the question it shows the human in the lead's session may
// wait, and what baton answers it then (hooks.Bound): one of baton's, or a call of the lead model's own,
// each of whose questions gets an answer. Nothing is recorded but when the question was first seen in
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
	w, err := hooks.Bound(s, askedOf(rep), now.Add(-time.Duration(rep.Elapsed)*time.Millisecond), now, warn, config.EscalationFromEnv(io.Env).Timeout)
	switch {
	case err != nil:
		return modReply{OK: true, WaitMs: 5000} // ask again in a moment
	case w.Answer == "" && len(w.Answers) == 0:
		return modReply{OK: true}
	case w.For > 0:
		return modReply{OK: true, WaitMs: max(w.For.Milliseconds(), 1)}
	}
	return modReply{OK: true, Answer: w.Answer, Answers: w.Answers}
}

// askedOf is the call a report of the mod's asks about: its questions, or, from a mod that sends only the
// first, that one.
func askedOf(rep bridge.Report) []bridge.Asked {
	if len(rep.Questions) > 0 {
		return rep.Questions
	}
	return []bridge.Asked{{Question: rep.Question}}
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
