package transcript

import (
	"errors"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// A rewind (Esc Esc, or /rewind) fires no hook, and leaves the session id and the transcript file as they
// were. What shows it is the next prompt: it follows the entry the conversation went back to, not the last
// one written before it, and the entries in between stay in the file, a branch left behind
// (docs/research/recovery.md). A prompt is written to the transcript after its UserPromptSubmit hook, so
// baton marks the transcript there (Leaf), and looks at a later hook of the turn (Forked).

// Mark is where a transcript stood when a prompt was submitted, before the prompt was written to it.
type Mark struct {
	Offset int64  // its size
	Leaf   string // the uuid of its last entry in the conversation ("" when none is written yet)
}

// leafBytes is how much of the end of a transcript Leaf reads first: its last entries, at the end of a
// turn, are small.
const leafBytes = 64 << 10

// Leaf marks the transcript at path as it is now.
func Leaf(path string) (Mark, error) {
	f, size, err := open(path)
	if err != nil {
		return Mark{}, err
	}
	defer f.Close()
	for n := int64(leafBytes); ; n *= 8 {
		entries, err := read(f, max(size-n, 0), size)
		if err != nil {
			return Mark{}, err
		}
		for i := len(entries) - 1; i >= 0; i-- {
			if e := entries[i]; conversation(e) {
				return Mark{Offset: size, Leaf: e.UUID}, nil
			}
		}
		if n >= size {
			return Mark{Offset: size}, nil
		}
	}
}

// conversation reports an entry of the conversation itself, as the next prompt follows it: the main
// agent's message, a system entry, or an attachment, and not a subagent's.
func conversation(e Entry) bool {
	switch e.Type {
	case "user", "assistant", "system", "attachment":
		return e.UUID != "" && !e.Sidechain
	}
	return false
}

// Fork is what the transcript shows of the prompt submitted after a Mark.
type Fork struct {
	// Prompt says the prompt is in the transcript; until it is, there is nothing to tell (look again at a
	// later hook).
	Prompt bool
	// Left is the branch the conversation left behind when the prompt does not go on from the mark's
	// leaf: the human rewound it. nil when it goes on.
	Left *Branch
}

// Branch is what a branch the conversation left behind held, as far as baton reads it.
type Branch struct {
	Since time.Time // its first entry's time: the conversation remembers nothing from then on
	Until time.Time // the time of the prompt the conversation went on with
	// Baton are the baton commands run there through Bash, in order.
	Baton []Command
	// Files are the files Claude's edit tools wrote there, as the tools named them, in the order of their
	// first edit.
	Files []string
	// Brief says a SessionStart hook's context was there: baton's brief, or its primer.
	Brief bool
}

// Errors for a mark that does not fit the transcript (the file is not the one marked), and for a transcript
// whose entries' parents go round in a loop.
var (
	ErrShrunk = errors.New("transcript: shorter than when it was marked")
	ErrNoLeaf = errors.New("transcript: the marked entry is not in it")
	ErrLoop   = errors.New("transcript: its entries' parents go round in a loop")
)

// Forked reads the transcript at path for the first prompt after mark m, and for whether it went on from
// the mark's leaf. In 1,274 real prompts, a prompt's parent was the leaf every time except after a rewind;
// when it is not, Forked walks back from both to the entry they share, and the leaf's side of that is the
// branch left behind. Each entry's parent is written before it, so the walk reads only as far back in the
// file as that entry.
func Forked(path string, m Mark) (Fork, error) {
	f, size, err := open(path)
	if err != nil {
		return Fork{}, err
	}
	defer f.Close()
	if size < m.Offset {
		return Fork{}, ErrShrunk
	}
	after, err := read(f, m.Offset, size)
	if err != nil {
		return Fork{}, err
	}
	var prompt *Entry
	for i, e := range after {
		if e.Prompt() && !e.Meta && !e.Sidechain {
			prompt = &after[i]
			break
		}
	}
	switch {
	case prompt == nil:
		return Fork{}, nil
	case m.Leaf == "" || prompt.Parent == m.Leaf:
		return Fork{Prompt: true}, nil
	}
	for n := size - m.Offset + 1<<20; ; n *= 8 {
		entries, err := read(f, max(size-n, 0), size)
		if err != nil {
			return Fork{}, err
		}
		left, ok, err := branch(entries, prompt.Parent, m.Leaf, n >= size)
		if err != nil {
			return Fork{}, err
		}
		if ok {
			return Fork{Prompt: true, Left: summarize(left, prompt.Time)}, nil
		}
	}
}

// branch walks back from the prompt's parent (from) and from the leaf to the entry they share, and returns
// the entries on the leaf's side, oldest first: none when the leaf is the prompt's ancestor. It reports
// false when entries, the end of the transcript, do not reach back that far, unless they are all of it
// (whole): a parent missing from the whole file counts as the start of the conversation.
func branch(entries []Entry, from, leaf string, whole bool) ([]Entry, bool, error) {
	at := map[string]int{}
	for i, e := range entries {
		if e.UUID != "" && !e.Sidechain {
			at[e.UUID] = i
		}
	}
	if _, ok := at[leaf]; !ok {
		if whole {
			return nil, false, ErrNoLeaf
		}
		return nil, false, nil
	}
	pos := func(id string) (int, bool) {
		if id == "" {
			return -1, true
		}
		i, ok := at[id]
		if !ok && whole {
			return -1, true
		}
		return i, ok
	}
	var left []Entry
	// Each step goes to an entry earlier in the file, so the walk ends within 2·len(entries) steps; the bound
	// holds for a file whose parents do not come first, too.
	for a, b, steps := from, leaf, 0; a != b; steps++ {
		ia, oka := pos(a)
		ib, okb := pos(b)
		switch {
		case !oka || !okb:
			return nil, false, nil
		case steps > 2*len(entries):
			return nil, false, ErrLoop
		case ia < 0 && ib < 0:
			a = b // two starts: nothing shared
		case ia > ib:
			a = entries[ia].Parent
		default:
			left = append(left, entries[ib])
			b = entries[ib].Parent
		}
	}
	for i, j := 0, len(left)-1; i < j; i, j = i+1, j-1 {
		left[i], left[j] = left[j], left[i]
	}
	return left, true, nil
}

// summarize is what a branch left behind held, or nil when it held no turn: the human rewinds to before a
// turn, so a branch with none of one (a prompt, a prompt typed while the model worked, which Claude Code
// writes as a queued_command attachment, or the model's reply) is an entry beside the conversation, not a
// rewind.
func summarize(left []Entry, until time.Time) *Branch {
	b := &Branch{Until: until}
	turn := false
	seen := map[string]bool{}
	for _, e := range left {
		turn = turn || e.Prompt() && !e.Meta || e.Type == "assistant" || e.Attachment == "queued_command"
		if !e.Time.IsZero() && (b.Since.IsZero() || e.Time.Before(b.Since)) {
			b.Since = e.Time
		}
		if e.Type == "attachment" && e.Attachment == "hook_additional_context" && e.HookEvent == "SessionStart" {
			b.Brief = true
		}
		for _, t := range e.Tools {
			switch t.Name {
			case "Bash":
				b.Baton = append(b.Baton, batonCommands(t.Command)...)
			case "Write", "Edit", "MultiEdit", "NotebookEdit":
				if t.File != "" && !seen[t.File] {
					seen[t.File] = true
					b.Files = append(b.Files, t.File)
				}
			}
		}
	}
	if !turn {
		return nil
	}
	return b
}

// Command is a baton command a Bash call ran: its subcommand, and the words after it.
type Command struct {
	Sub  string
	Args []string
}

// batonCommands finds the baton commands in a Bash command line: each simple command whose program's base
// name is baton (baton, ~/.baton/bin/v0.6.0/baton, baton.exe), after any VAR=value words. The line is read
// as a shell would, just far enough for that (lane.Commands): quotes and backslashes protect what they
// cover, ;, &, |, parentheses and newlines outside them separate commands, and a here-document's body (a
// commit message, say) holds none.
func batonCommands(line string) []Command {
	var out []Command
	for _, words := range lane.Commands(line) {
		for len(words) > 0 && assignment(words[0]) {
			words = words[1:]
		}
		if len(words) < 2 {
			continue
		}
		prog := words[0][strings.LastIndexAny(words[0], `/\`)+1:]
		if prog == "baton" || strings.EqualFold(prog, "baton.exe") {
			out = append(out, Command{Sub: words[1], Args: words[2:]})
		}
	}
	return out
}

// assignment reports a VAR=value word, which sets the command's environment rather than naming it.
func assignment(word string) bool {
	i := strings.IndexByte(word, '=')
	if i <= 0 {
		return false
	}
	for j, c := range word[:i] {
		if !(c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || j > 0 && '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}
