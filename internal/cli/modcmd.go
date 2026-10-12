package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	goio "io"

	"github.com/ozzyfromspace/baton/internal/bridge"
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
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
	// recorded (its next follows it), and the agent type it registers for phases.
	Run   string        `json:"run,omitempty"`
	Dir   string        `json:"dir,omitempty"`
	Seen  int           `json:"seen,omitempty"`
	Agent *modAgentType `json:"agent,omitempty"`
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
	switch {
	case !hosted(io):
		return modReply{Why: "this session is not hosted by baton"}
	case worker(io):
		return modReply{Why: "a worker's session starts no phase agents"}
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
	if rep.Kind == bridge.Hello {
		return modHello(io, s, b, rep)
	}
	switch dup, err := b.Record(rep); {
	case errors.Is(err, bridge.ErrStale):
		return modReply{Stale: true, Why: "say hello first"}
	case err != nil:
		return modReply{Retry: true, Why: err.Error()}
	case dup:
		return modReply{OK: true, Ignored: "recorded already"}
	}
	if (rep.Kind == bridge.Compacting || rep.Kind == bridge.Compacted) && !rep.Skipped {
		// The mod's word on a compaction of the lead is a proof of it (state.Reported), counted as it comes,
		// with the bridge's lock let go.
		over, now := rep.Kind == bridge.Compacted, io.Now()
		var changed bool
		s.Update(func(st *state.State) error {
			changed = state.Report(st, state.Reported(over, rep.Trigger, now), now)
			return nil
		})
		if changed {
			s.Event("compact_reported", map[string]any{"over": over, "trigger": rep.Trigger})
		}
	}
	return modReply{OK: true}
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
	case bridge.Ack, bridge.Answered:
		if rep.Command <= 0 {
			return rep.Kind + " names no command"
		}
	case bridge.Turn, bridge.Dropped:
		if rep.Agent == "" {
			return rep.Kind + " names no agent"
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
		Agent: &modAgentType{Name: "phase", Description: decide.PhaseAgentDescription, Prompt: decide.PhaseAgentPrompt}}
}
