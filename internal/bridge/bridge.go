// Package bridge is how baton's host and baton's mod talk. The mod (plugin/hooks/baton.js) is JavaScript
// that Claude Code runs in the lead's own process. It does what only something inside the session can do,
// on the host's say-so: spawn and message phase agents, ask the human, keep agents' reports from the
// lead's model, compact the lead's conversation and submit baton's lines to it, with no key typed into the
// session (spike 35). And it reports what it sees. The host decides; the mod acts. They talk through files under
// the run's directory and the CLI, as hooks do, with no network:
//
//	mod/out/<id>.json  the host's commands, numbered, each written whole under a temporary name and then
//	                   renamed; the mod polls the directory and runs them in order
//	mod/agents.json    baton's phase agents: the mod keeps their reports from the lead's model, and
//	                   reports their turns
//	mod/bridge.json    the host's record: the mod's hello, the commands not acked yet, and the reports
//	                   the host has not taken yet
//	mod/lock           held for every change to the record
//
// The mod reports through `baton mod report` (internal/cli), one numbered report at a time (Record).
//
// Exactly once: every command is addressed to the host instance that wrote it, so only the mod in the
// claude that host runs takes it; that mod keeps the id of the last command it ran in its store, so once
// reloaded it neither repeats nor skips one. A command is done when its ack is recorded: its file goes
// then, and until then the mod reports the ack again. Reports are numbered by the mod, and one recorded
// already is recorded no more.
package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/ozzyfromspace/baton/internal/state"
)

// Protocol is the version of what the host and the mod say to each other. The mod sends its own with its
// hello, and the host takes a mod of another for none. 2: the mod compacts the lead and submits baton's lines.
const Protocol = 2

// Command kinds.
const (
	Spawn = "spawn" // start a baton:phase agent on a phase
	Send  = "send"  // message an agent; Claude Code holds it for the agent's next tool round
	Stop  = "stop"  // end a running agent (TaskStop)
	Ask   = "ask"   // ask the human in Claude Code's own question dialog
	// Compact compacts the lead's conversation, between turns, as /compact would; the mod acks it as it
	// starts it, and reports it compacted (or not) when it is over. Claude Code refuses it while a turn runs:
	// one refused while the lead's turn runs the mod holds, and starts at that turn's end, up to 30 s.
	Compact = "compact"
	// Submit submits a line of baton's to the lead's model as a prompt of its own, read as one typed; the
	// draft in the input box stays. One submitted while a turn runs waits for the turn's end.
	Submit = "submit"
	// Moved tells the mod that its host drives another run now (its session carried a run on, or the human
	// switched conversations): the mod says hello again, which gives it that run's bridge, and acks
	// nothing here. Its host takes it back if it drives this run again (Retract).
	Moved = "moved"
)

// Report kinds.
const (
	Hello      = "hello"      // the mod is loaded, and says which it is
	Ack        = "ack"        // the mod ran a command, with what it answered
	Turn       = "turn"       // one of baton's agents ended a turn
	Compacting = "compacting" // the lead's conversation is about to compact
	Compacted  = "compacted"  // and has
	Dropped    = "dropped"    // a report of one of baton's agents was kept from the lead's model
	Failed     = "failed"     // a command the mod acked as started failed afterwards (a submit)
	Answered   = "answered"   // the human answered a question the mod asked
	Deadline   = "deadline"   // nobody answered a question in the lead's session by its deadline, and the mod answered it
	// Asking asks how long a question on the human's screen in the lead's session may wait, and what baton
	// answers it then: a query, not recorded.
	Asking = "asking"
)

// Command is one thing the host has the mod do.
type Command struct {
	ID       int       `json:"id"`
	Instance string    `json:"instance"` // the host's: only the mod in the claude it runs takes the command
	Kind     string    `json:"kind"`
	Phase    string    `json:"phase,omitempty"`
	At       time.Time `json:"at"`
	// Spawn: the agent's task, its line in Claude Code's agent panel, the name it goes by, and the line the
	// human reads when the mod keeps one of its reports from the lead's model (Agent.Drop), until the
	// host lists it among baton's agents.
	Prompt      string `json:"prompt,omitempty"`
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`
	Drop        string `json:"drop,omitempty"`
	// Send and Stop: the agent; for Send, the message and its label in the lead's transcript.
	Agent   string `json:"agent,omitempty"`
	Text    string `json:"text,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Ask: the question, its options and its header.
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	Header   string   `json:"header,omitempty"`
	// Compact: the compaction it is for (state.Compaction.Epoch). Submit: the line, in Text.
	Epoch int `json:"epoch,omitempty"`
}

// Report is what the mod saw or did, as `baton mod report` reads it.
type Report struct {
	Seq      int       `json:"seq"` // the mod's number for it: from 1, one up each time
	Kind     string    `json:"kind"`
	Instance string    `json:"instance,omitempty"` // the host instance of the claude the mod runs in
	Session  string    `json:"session,omitempty"`
	At       time.Time `json:"at,omitzero"` // when the host recorded it
	// Hello: the plugin's version and the mod's Protocol.
	Version  string `json:"version,omitempty"`
	Protocol int    `json:"protocol,omitempty"`
	// Ack and Answered: the command. An ack says whether the call succeeded and what it answered (a
	// spawn's agent in Agent), or Interrupted: the mod was reloaded while it ran the command, which may or
	// may not have happened.
	Command     int    `json:"command,omitempty"`
	OK          bool   `json:"ok,omitempty"`
	Result      string `json:"result,omitempty"`
	Error       string `json:"error,omitempty"`
	Interrupted bool   `json:"interrupted,omitempty"`
	// Ack of a spawn, Turn, Dropped: the agent.
	Agent string `json:"agent,omitempty"`
	// Turn: why it ended (answer, aborted, refusal or error), a refusal's category, the agent's last
	// words (cut), and how long the turn took.
	Reason  string `json:"reason,omitempty"`
	Refusal string `json:"refusal,omitempty"`
	Text    string `json:"text,omitempty"`
	Ms      int64  `json:"ms,omitempty"`
	// Compacting and Compacted: what started the lead's compaction; Skipped: a hook vetoed it. Compacted of a
	// compaction the mod ran for a Compact command: the command, in Command, and its Epoch; Error: it failed.
	Trigger string `json:"trigger,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
	Epoch   int    `json:"epoch,omitempty"`
	// Answered: the question, and the label the human chose, or what they typed; Dismissed: the dialog
	// closed without one; Deadline: nobody answered it, and the mod gave it the answer baton gave it.
	// Deadline (a report of its own) is that too, for any question baton answers in the lead's session: one
	// of baton's, or a call of the lead model's own (with Questions and Answers).
	Question  string `json:"question,omitempty"`
	Answer    string `json:"answer,omitempty"`
	Dismissed bool   `json:"dismissed,omitempty"`
	Deadline  bool   `json:"deadline,omitempty"`
	// Asking (asked, not recorded: `baton mod report` answers it at once): a question is on the human's
	// screen in the lead's session, put to them Elapsed milliseconds ago; the reply says how long it may
	// wait. Question is the call's first question, and Questions every one of it, with its options.
	Elapsed   int64   `json:"elapsed,omitempty"`
	Questions []Asked `json:"questions,omitempty"`
	// Deadline of a call of the lead model's own: the answer baton gave each of its questions (and the
	// first's in Answer), by the question's text as asked.
	Answers map[string]string `json:"answers,omitempty"`
}

// Asked is one question of an AskUserQuestion call in the lead's session, as the mod saw it: its text, its
// options' labels in order, and whether it takes several of them.
type Asked struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Multi    bool     `json:"multi,omitempty"`
}

// Record is the host's side of the bridge (bridge.json).
type Record struct {
	Seq   int        `json:"seq"`             // the last command id issued
	Hello *HelloInfo `json:"hello,omitempty"` // the mod that said hello last
	Seen  int        `json:"seen,omitempty"`  // the last report of Hello's mod recorded
	Open  []Issued   `json:"open,omitempty"`  // the commands not acked yet, oldest first
	// Inbox holds the reports recorded and not taken by the host yet (Take), oldest first; past MaxInbox
	// the oldest go, counted in Lost.
	Inbox []Report `json:"inbox,omitempty"`
	Lost  int      `json:"lost,omitempty"`
}

// HelloInfo is the mod's hello as recorded.
type HelloInfo struct {
	Instance string    `json:"instance"`
	Session  string    `json:"session,omitempty"`
	Version  string    `json:"version,omitempty"`
	Protocol int       `json:"protocol"`
	At       time.Time `json:"at"`
	// Refused is why the host takes this mod for none ("" when it is baton's): it reports nothing the
	// host records, and it runs no command.
	Refused string `json:"refused,omitempty"`
}

// Ready reports whether h is a mod baton works with, in the claude host instance runs.
func (h *HelloInfo) Ready(instance string) bool {
	return h != nil && h.Refused == "" && h.Instance == instance
}

// Issued is a command issued and not acked yet.
type Issued struct {
	ID       int       `json:"id"`
	Instance string    `json:"instance"`
	Kind     string    `json:"kind"`
	Phase    string    `json:"phase,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Epoch    int       `json:"epoch,omitempty"`
	At       time.Time `json:"at"`
}

// Agent is one of baton's phase agents, as the mod reads them (agents.json). Drop is the line the human
// reads in the lead's transcript when the mod keeps one of its reports from the lead's model.
type Agent struct {
	ID    string `json:"id"`
	Phase string `json:"phase"`
	Drop  string `json:"drop,omitempty"`
}

type agentsFile struct {
	Agents []Agent `json:"agents"`
}

// MaxInbox bounds the reports the record keeps for the host.
const MaxInbox = 500

// Errors a command can be refused with. Each leaves everything as it was.
var (
	ErrSpawnOpen    = errors.New("another spawn is not acked yet") // one spawn at a time, so a worktree belongs to one phase
	ErrHasAgent     = errors.New("the phase has an agent")         // forget it before another is spawned
	ErrUnknownAgent = errors.New("not one of baton's agents")      // never sent to: a message would resume it
	ErrStale        = errors.New("the report is not from the mod baton works with")
)

// Bridge is a handle on one run's side of the bridge.
type Bridge struct {
	Dir string // the run's mod directory
	Now func() time.Time
	// Event logs what the bridge records (the run's events.jsonl); nil logs nothing.
	Event func(kind string, fields map[string]any)
	// Drop words the line the human reads when the mod keeps an agent's report from the lead's model
	// (Agent.Drop); nil leaves it to the mod.
	Drop func(phase string) string
}

// Open returns the bridge of the run whose directory is runDir.
func Open(runDir string, now func() time.Time, event func(string, map[string]any)) *Bridge {
	return &Bridge{Dir: filepath.Join(runDir, "mod"), Now: now, Event: event}
}

// Out is the directory the commands are written to, which the mod polls.
func (b *Bridge) Out() string { return filepath.Join(b.Dir, "out") }

// AgentsPath is the file of baton's agents, which the mod reads.
func (b *Bridge) AgentsPath() string { return filepath.Join(b.Dir, "agents.json") }

func (b *Bridge) recordPath() string { return filepath.Join(b.Dir, "bridge.json") }

// CommandPath is where the command id is written.
func (b *Bridge) CommandPath(id int) string {
	return filepath.Join(b.Out(), fmt.Sprintf("%06d.json", id))
}

// commandName is a command file's name; anything else in the directory (a temporary file) is no command.
var commandName = regexp.MustCompile(`^([0-9]+)\.json$`)

func (b *Bridge) event(kind string, fields map[string]any) {
	if b.Event != nil {
		b.Event(kind, fields)
	}
}

// Load reads the record without changing it.
func (b *Bridge) Load() (Record, error) {
	var r Record
	err := b.locked(func() error {
		var err error
		r, err = b.read()
		return err
	})
	return r, err
}

// Peek reads the record without the lock: it is replaced whole, so it is never torn, but it may be a
// moment old.
func (b *Bridge) Peek() (Record, error) { return b.read() }

func (b *Bridge) read() (Record, error) {
	r, _, err := b.readRaw()
	return r, err
}

// readRaw reads the record, returning also the bytes it read: nil for no record yet.
func (b *Bridge) readRaw() (Record, []byte, error) {
	var r Record
	data, err := os.ReadFile(b.recordPath())
	if errors.Is(err, os.ErrNotExist) {
		return r, nil, nil
	}
	if err != nil {
		return r, nil, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, nil, fmt.Errorf("bridge.json: %w", err)
	}
	return r, data, nil
}

func (b *Bridge) locked(fn func() error) error {
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return err
	}
	return state.Locked(filepath.Join(b.Dir, "lock"), state.LockWatch{Now: b.Now, Event: b.Event}, fn)
}

// update applies fn to the record under the lock, and saves it unless fn fails or leaves it as it was:
// the host takes the inbox on every tick (Take), and an empty one changes nothing.
func (b *Bridge) update(fn func(*Record) error) (Record, error) {
	var r Record
	err := b.locked(func() error {
		var (
			old []byte
			err error
		)
		if r, old, err = b.readRaw(); err != nil {
			return err
		}
		if err := fn(&r); err != nil {
			return err
		}
		data, err := encodeJSON(r)
		if err != nil || old != nil && bytes.Equal(data, old) {
			return err
		}
		return writeData(b.recordPath(), data)
	})
	return r, err
}

func writeJSON(path string, v any) error {
	data, err := encodeJSON(v)
	if err != nil {
		return err
	}
	return writeData(path, data)
}

// encodeJSON is v as the bridge's files hold it.
func encodeJSON(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	return append(data, '\n'), err
}

func writeData(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return state.WriteFile(path, data)
}

// Agents lists baton's agents.
func (b *Bridge) Agents() ([]Agent, error) {
	data, err := os.ReadFile(b.AgentsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f agentsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("agents.json: %w", err)
	}
	return f.Agents, nil
}

func (b *Bridge) writeAgents(agents []Agent) error {
	if agents == nil {
		agents = []Agent{}
	}
	return writeJSON(b.AgentsPath(), agentsFile{Agents: agents})
}

// Track makes agent one of baton's agents, on phase: the mod keeps its reports from the lead's model and
// reports its turns. It is idempotent.
func (b *Bridge) Track(agent, phase string) error {
	return b.locked(func() error { return b.track(agent, phase) })
}

func (b *Bridge) track(agent, phase string) error {
	agents, err := b.Agents()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(agents, func(a Agent) bool { return a.ID == agent }) {
		return nil
	}
	a := Agent{ID: agent, Phase: phase}
	if b.Drop != nil {
		a.Drop = b.Drop(phase)
	}
	if err := b.writeAgents(append(agents, a)); err != nil {
		return err
	}
	b.event("mod_agent", map[string]any{"agent": agent, "phase": phase})
	return nil
}

// Forget makes agent none of baton's agents any more: its reports reach the lead's model again, and no
// command is ever sent to it, since a message would resume it.
func (b *Bridge) Forget(agent string) error {
	return b.locked(func() error {
		agents, err := b.Agents()
		if err != nil {
			return err
		}
		kept := slices.DeleteFunc(slices.Clone(agents), func(a Agent) bool { return a.ID == agent })
		if len(kept) == len(agents) {
			return nil
		}
		if err := b.writeAgents(kept); err != nil {
			return err
		}
		b.event("mod_forgot", map[string]any{"agent": agent})
		return nil
	})
}

// Issue writes c for the mod, from host instance c.Instance, and returns it with its id. A spawn waits
// for the one before it to be acked (ErrSpawnOpen), unless it is for the same phase, which it then is (a
// spawn is idempotent); a spawn for a phase that has an agent is refused (ErrHasAgent); and a command for
// an agent baton does not have is refused (ErrUnknownAgent). The file is written whole: the mod never
// reads a part of one.
func (b *Bridge) Issue(c Command) (Command, error) {
	err := b.locked(func() error {
		r, err := b.read()
		if err != nil {
			return err
		}
		agents, err := b.Agents()
		if err != nil {
			return err
		}
		switch c.Kind {
		case Spawn:
			for _, o := range r.Open {
				if o.Kind != Spawn || o.Instance != c.Instance {
					continue
				}
				if o.Phase == c.Phase {
					c.ID, c.At = o.ID, o.At
					return errSame
				}
				return ErrSpawnOpen
			}
			if slices.ContainsFunc(agents, func(a Agent) bool { return a.Phase == c.Phase }) {
				return ErrHasAgent
			}
			if b.Drop != nil {
				c.Drop = b.Drop(c.Phase)
			}
		case Send, Stop:
			if !slices.ContainsFunc(agents, func(a Agent) bool { return a.ID == c.Agent }) {
				return ErrUnknownAgent
			}
		case Ask, Submit, Moved:
		case Compact:
			if c.Epoch <= 0 {
				return fmt.Errorf("a compact names no compaction")
			}
		default:
			return fmt.Errorf("no such command: %q", c.Kind)
		}
		r.Seq++
		c.ID, c.At = r.Seq, b.Now()
		if err := writeJSON(b.CommandPath(c.ID), c); err != nil {
			return err
		}
		r.Open = append(r.Open, Issued{ID: c.ID, Instance: c.Instance, Kind: c.Kind, Phase: c.Phase, Agent: c.Agent, Epoch: c.Epoch, At: c.At})
		if err := writeJSON(b.recordPath(), r); err != nil {
			os.Remove(b.CommandPath(c.ID))
			return err
		}
		fields := map[string]any{"id": c.ID, "command": c.Kind}
		if c.Phase != "" {
			fields["phase"] = c.Phase
		}
		if c.Agent != "" {
			fields["agent"] = c.Agent
		}
		if c.Epoch != 0 {
			fields["epoch"] = c.Epoch
		}
		b.event("mod_command", fields)
		return nil
	})
	if errors.Is(err, errSame) {
		return c, nil
	}
	return c, err
}

// errSame ends Issue for a spawn issued already.
var errSame = errors.New("issued already")

// Claim makes instance the host of the bridge: the commands another host instance issued and the mod did
// not ack are void. Their claude is gone, and nothing will run them.
func (b *Bridge) Claim(instance string) error {
	var void []int
	_, err := b.update(func(r *Record) error {
		void = nil
		kept := r.Open[:0]
		for _, o := range r.Open {
			if o.Instance == instance {
				kept = append(kept, o)
				continue
			}
			void = append(void, o.ID)
			os.Remove(b.CommandPath(o.ID))
		}
		r.Open = kept
		return nil
	})
	if err == nil && len(void) > 0 {
		b.event("mod_voided", map[string]any{"ids": void})
	}
	return err
}

// Retract takes back the commands of kind that instance left open here, as Void does each, and reports
// how many there were.
func (b *Bridge) Retract(instance, kind string) (int, error) {
	rec, err := b.read()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range rec.Open {
		if o.Instance == instance && o.Kind == kind {
			if found, err := b.Void(instance, o.ID); err != nil {
				return n, err
			} else if found {
				n++
			}
		}
	}
	return n, nil
}

// Void takes back command id of instance, not acked yet: its file goes, and the mod never runs it. A spawn
// voided leaves no spawn open, so the worktree of an agent the mod started from it all the same is not its
// phase's, and the agent does not start (hooks.worktreeCreate). It reports whether the command was open.
func (b *Bridge) Void(instance string, id int) (bool, error) {
	var found bool
	_, err := b.update(func(r *Record) error {
		i := slices.IndexFunc(r.Open, func(o Issued) bool { return o.ID == id && o.Instance == instance })
		if found = i >= 0; found {
			r.Open = slices.Delete(r.Open, i, i+1)
			os.Remove(b.CommandPath(id))
		}
		return nil
	})
	if err == nil && found {
		b.event("mod_voided", map[string]any{"ids": []int{id}})
	}
	return found, err
}

// Commands lists the command files the mod finds in the directory now, in order, as it reads them.
func (b *Bridge) Commands() ([]Command, error) {
	entries, err := os.ReadDir(b.Out())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cmds []Command
	for _, e := range entries {
		if !commandName.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(b.Out(), e.Name()))
		if err != nil {
			continue // acked meanwhile
		}
		var c Command
		if json.Unmarshal(data, &c) == nil {
			cmds = append(cmds, c)
		}
	}
	slices.SortFunc(cmds, func(x, y Command) int { return x.ID - y.ID })
	return cmds, nil
}

// SayHello records the mod's hello, and returns the number of its last report recorded, which its next
// must follow. A mod in the claude of the instance that said hello last keeps its count (it was reloaded);
// any other starts over.
func (b *Bridge) SayHello(h HelloInfo) (seen int, err error) {
	h.At = b.Now()
	_, err = b.update(func(r *Record) error {
		if r.Hello == nil || r.Hello.Instance != h.Instance || r.Hello.Refused != h.Refused {
			r.Seen = 0
		}
		r.Hello, seen = &h, r.Seen
		return nil
	})
	if err != nil {
		return 0, err
	}
	fields := map[string]any{"mod_instance": h.Instance, "version": h.Version, "protocol": h.Protocol}
	if h.Refused != "" {
		fields["refused"] = h.Refused
	}
	b.event("mod_hello", fields)
	return seen, nil
}

// Record records report rep, as Accept does, and reports whether it was a repeat.
func (b *Bridge) Record(rep Report) (dup bool, err error) {
	a, err := b.Accept(rep)
	return a.Dup, err
}

// Accepted is what Accept made of a report: a repeat (Dup), or new; and the command a new ack ended, as it
// was issued, if it was open (nil for one voided, or acked already).
type Accepted struct {
	Dup   bool
	Acked *Issued
}

// Accept records report rep: it is new if the mod that said hello last made it and none numbered as it
// was recorded before (a repeat is left out: Dup). An ack ends its command, whose file goes, and an ack
// of a spawn makes its agent one of baton's (Track) before anything else can be reported of it. Every new
// report goes to the inbox for the host, and is logged. ErrStale: the report is not from that mod.
func (b *Bridge) Accept(rep Report) (Accepted, error) {
	rep.At = b.Now()
	var cmd *Issued
	var dup bool
	_, err := b.update(func(r *Record) error {
		dup, cmd = false, nil
		if !r.Hello.Ready(rep.Instance) {
			return ErrStale
		}
		if rep.Seq <= r.Seen {
			dup = true
			return nil
		}
		r.Seen = rep.Seq
		if rep.Kind == Ack {
			if i := slices.IndexFunc(r.Open, func(o Issued) bool { return o.ID == rep.Command }); i >= 0 {
				o := r.Open[i]
				cmd = &o
				r.Open = slices.Delete(r.Open, i, i+1)
			}
			os.Remove(b.CommandPath(rep.Command))
			if cmd != nil && cmd.Kind == Spawn && rep.OK && rep.Agent != "" {
				if err := b.track(rep.Agent, cmd.Phase); err != nil {
					return err
				}
			}
		}
		r.Inbox = append(r.Inbox, rep)
		if n := len(r.Inbox) - MaxInbox; n > 0 {
			r.Inbox = slices.Delete(r.Inbox, 0, n)
			r.Lost += n
		}
		return nil
	})
	if err != nil || dup {
		return Accepted{Dup: dup}, err
	}
	b.event("mod_"+rep.Kind, reportFields(rep, cmd))
	return Accepted{Acked: cmd}, nil
}

// reportFields are what a report's event says.
func reportFields(rep Report, cmd *Issued) map[string]any {
	f := map[string]any{"seq": rep.Seq}
	add := func(k string, v any, keep bool) {
		if keep {
			f[k] = v
		}
	}
	add("command", rep.Command, rep.Command != 0)
	if cmd != nil {
		f["command_kind"] = cmd.Kind
		add("phase", cmd.Phase, cmd.Phase != "")
	} else if rep.Kind == Ack {
		f["unknown"] = true // voided, or acked under another number
	}
	add("ok", rep.OK, rep.Kind == Ack)
	add("result", cut(rep.Result, 300), rep.Result != "")
	add("error", cut(rep.Error, 300), rep.Error != "")
	add("interrupted", true, rep.Interrupted)
	add("agent", rep.Agent, rep.Agent != "")
	add("reason", rep.Reason, rep.Reason != "")
	add("refusal", rep.Refusal, rep.Refusal != "")
	add("ms", rep.Ms, rep.Ms != 0)
	add("trigger", rep.Trigger, rep.Trigger != "")
	add("skipped", true, rep.Skipped)
	if cmd != nil && cmd.Epoch != 0 {
		f["epoch"] = cmd.Epoch
	} else {
		add("epoch", rep.Epoch, rep.Epoch != 0)
	}
	add("question", cut(rep.Question, 300), rep.Question != "")
	add("answer", cut(rep.Answer, 300), rep.Answer != "")
	add("dismissed", true, rep.Dismissed)
	add("deadline", true, rep.Deadline)
	return f
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Take returns the reports recorded since the last Take, oldest first, and empties the inbox.
func (b *Bridge) Take() ([]Report, error) {
	var taken []Report
	_, err := b.update(func(r *Record) error {
		taken, r.Inbox = r.Inbox, nil
		return nil
	})
	return taken, err
}

// Overdue lists the commands of instance issued longer than after ago and not acked yet.
func (r Record) Overdue(instance string, now time.Time, after time.Duration) []Issued {
	var late []Issued
	for _, o := range r.Open {
		if o.Instance == instance && now.Sub(o.At) >= after {
			late = append(late, o)
		}
	}
	return late
}

// PendingSpawn is instance's spawn not acked yet, if there is one (there is at most one at a time).
func (r Record) PendingSpawn(instance string) (Issued, bool) {
	for _, o := range r.Open {
		if o.Kind == Spawn && o.Instance == instance {
			return o, true
		}
	}
	return Issued{}, false
}
