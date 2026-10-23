package board

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// sep separates the band's parts.
const sep = " · "

// part is one part of the band, and how much it matters when the band is short of room: the parts that
// matter least go first, and the phases that went are counted (decide.BandMore).
type part struct {
	text  string
	rank  int
	phase bool
}

// What a band's part is worth (part.rank).
const (
	rankWaits  = iota + 1 // a phase that waits on others
	rankReady             // one that could start: ready, held back
	rankAtWork            // one under way: the lead's, an agent's, verifying, landing
	rankRun               // the lead, the run waiting on a usage limit
	rankNeeds             // a phase that needs the human
	rankHead              // baton's mark and the step: always there
)

// Band is the run in one line, at most width cells wide (0: no limit): baton's mark, the step the run is
// at, the phases under way or next, and the lead. "" with no plan, for no band at all.
//
//	◆ baton · step 2/4 · P3 ⚙ 12m · P4 verifying 3m · P5 waits on P3 · lead: waiting on j4
func (b Board) Band(width int) string {
	switch b.Mode {
	case state.ModeIdle:
		return ""
	case state.ModeComplete:
		return clip(decide.BandMark+sep+decide.BandComplete, width)
	}
	parts := []part{{text: decide.BandMark, rank: rankHead}}
	if b.Mode == state.ModePaused {
		parts = append(parts, part{text: decide.BandPaused, rank: rankHead})
	}
	if len(b.Steps) > 0 {
		parts = append(parts, part{text: decide.BandStep(b.Step, len(b.Steps)), rank: rankHead})
	}
	if b.Limit != nil {
		parts = append(parts, part{text: decide.BandLimit(b.Limit.Until, b.Limit.Resumes, b.Limit.Cancelled, b.At), rank: rankRun})
	}
	if b.Serial != "" {
		parts = append(parts, part{text: decide.BandSerial, rank: rankReady})
	}
	for _, s := range b.Steps {
		for _, p := range s.Phases {
			if r := rank(p.Stand.Kind, s.N, b.Step); r > 0 {
				parts = append(parts, part{text: p.Stand.Band(p.ID, b.At), rank: r, phase: true})
			}
		}
	}
	parts = append(parts, part{text: b.Lead.Band(), rank: rankRun})
	return fit(parts, width)
}

// rank is what a phase standing so is worth on the band, in step n while the run is at step at: 0 for a
// phase the band leaves out, done, or waiting further down the plan than the step after this one.
func rank(kind string, n, at int) int {
	switch kind {
	case decide.StandDone:
		return 0
	case decide.StandNeedsYou:
		return rankNeeds
	case decide.StandWaits:
		if n > at+1 {
			return 0
		}
		return rankWaits
	case decide.StandReady, decide.StandHeld:
		if n > at+1 {
			return 0
		}
		return rankReady
	}
	return rankAtWork
}

// fit joins parts into a line at most width cells wide (0: no limit). Short of room, it leaves out the
// phases worth least, the later first, and counts them; then, if need be, it cuts the line.
func fit(parts []part, width int) string {
	kept := make([]bool, len(parts))
	for i := range kept {
		kept[i] = true
	}
	left := 0
	for {
		line := join(parts, kept, left)
		if width <= 0 || cells(line) <= width {
			return line
		}
		drop := -1
		for i := len(parts) - 1; i >= 0; i-- {
			if kept[i] && parts[i].phase && (drop < 0 || parts[i].rank < parts[drop].rank) {
				drop = i
			}
		}
		if drop < 0 {
			return clip(line, width)
		}
		kept[drop] = false
		left++
	}
}

// join joins the parts kept, with the count of the phases left out after the last phase kept (or where
// the phases would be).
func join(parts []part, kept []bool, left int) string {
	var out []string
	at := -1 // where the count goes: after the last phase kept, or after the parts before the phases
	for i, p := range parts {
		if kept[i] {
			out = append(out, p.text)
			if p.phase || at < 0 && i+1 < len(parts) && parts[i+1].phase {
				at = len(out)
			}
		}
	}
	if left > 0 {
		if at < 0 {
			at = max(len(out)-1, 0)
		}
		out = append(out[:at], append([]string{decide.BandMore(left)}, out[at:]...)...)
	}
	return strings.Join(out, sep)
}

// Lines is the board: the plan step by step, a line per phase with how it stands, then the lead and the
// run's notes, each line at most width cells wide (0: no limit).
//
//	◆ baton · "Workers you can see" · step 2 of 4
//	step 1  ✓ P0  What the bridge needs — done
//	step 2  ⚙ P3  Phase agents — agent at work 12m · agent a1b2c3d4
//	        ⧗ P4  Gates and questions — verifying 3m (job j4)
//	step 3  · P5  baton's view — waits on P3
//	lead: its turn over, waiting on j4
func (b Board) Lines(width int) []string {
	if b.Mode == state.ModeIdle {
		return []string{clip(decide.BoardNoPlan, width)}
	}
	out := []string{clip(decide.BoardHead(b.Title, b.Mode, b.Step, len(b.Steps)), width)}
	label, id := 0, 0
	for _, s := range b.Steps {
		label = max(label, cells(decide.BoardStep(s.N)))
		for _, p := range s.Phases {
			id = max(id, cells(p.ID))
		}
	}
	handles := false
	for _, s := range b.Steps {
		for i, p := range s.Phases {
			step := ""
			if i == 0 {
				step = decide.BoardStep(s.N)
			}
			stand := p.Stand.Board(b.At)
			if p.Agent != "" && p.Worker == state.WorkerWorking {
				stand += sep + decide.Handle(p.ID) // how the human writes to it, before its id, which a narrow board cuts
				handles = true
			}
			if p.Agent != "" {
				stand += sep + "agent " + short(p.Agent)
			}
			head := fmt.Sprintf("%s  %s %s  ", pad(step, label), p.Stand.Mark(), pad(p.ID, id))
			out = append(out, row(head, p.Title, stand, width))
		}
	}
	out = append(out, clip(b.Lead.Board(b.At), width))
	if b.Serial != "" {
		out = append(out, clip(decide.BoardSerial(b.Serial), width))
	}
	if l := b.Limit; l != nil {
		out = append(out, clip(decide.BoardLimit(l.By, l.Until, l.Resumes, l.Cancelled, b.At), width))
	}
	if r := b.Risk; r != nil {
		out = append(out, clip(decide.RiskLine(r.Total, r.Held), width))
	}
	if handles {
		for _, line := range decide.BoardWrite {
			out = append(out, wrap(line, width)...)
		}
	}
	return out
}

// wrap breaks s into lines at most width cells wide (0: no limit), between words: a word wider than a line
// is cut.
func wrap(s string, width int) []string {
	if width <= 0 || cells(s) <= width {
		return []string{s}
	}
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case cells(line)+1+cells(w) <= width:
			line += " " + w
		default:
			out = append(out, clip(line, width))
			line = w
		}
	}
	if line != "" {
		out = append(out, clip(line, width))
	}
	return out
}

// row is a phase's line on the board: head, its title, and how it stands, cutting the title for room
// (leaving it out when too little of it would show), then the line.
func row(head, title, stand string, width int) string {
	line := head + title + " — " + stand
	if width <= 0 || cells(line) <= width {
		return line
	}
	if room := width - cells(head) - cells(" — "+stand); room >= 12 {
		return head + clip(title, room) + " — " + stand
	}
	return clip(head+stand, width)
}

// short is the start of an agent's id, enough to tell it apart.
func short(id string) string {
	if r := []rune(id); len(r) > 8 {
		return string(r[:8])
	}
	return id
}

// pad is s padded with spaces to n cells.
func pad(s string, n int) string {
	if c := cells(s); c < n {
		return s + strings.Repeat(" ", n-c)
	}
	return s
}

// cells is how many cells s takes in a terminal, taking each character for one.
func cells(s string) int { return utf8.RuneCountInString(s) }

// clip cuts s to width cells (0: no limit), ending what it cut with "…".
func clip(s string, width int) string {
	if width <= 0 || cells(s) <= width {
		return s
	}
	r := []rune(s)
	return string(r[:max(width-1, 0)]) + "…"
}
