package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A Project is a working tree's .baton directory. It keeps one run per Claude Code session, so every
// baton terminal in the same working tree runs a plan of its own:
//
//	.baton/runs/<run>/       one run: plan.json, state.json, events.jsonl, handoff.md, drafts/ (see Store)
//	.baton/sessions/<id>     the run a session belongs to (one line: the run's id)
//	.baton/hosts/<instance>  the run a baton host drives (one line: the run's id)
//	.baton/pending           a run attached outside any session, for the next baton session started here
//	.baton/lock              held while sessions and hosts are bound to runs
//	.baton/baton.log         the hosts' log, each line tagged with its instance
//
// A run is named after the session it started in. A session keeps its run when it is resumed, and the
// conversation keeps it when /clear or a "clear context" plan approval gives it a new session id in the
// same terminal (docs/research/sessions.md). Until v0.3 a project had a single run kept in .baton/
// itself; OpenProject moves such a run into runs/ once no older baton is driving it.
type Project struct {
	Dir  string // the .baton directory
	Root string // the working tree
	Now  func() time.Time
}

const (
	runsDir     = "runs"
	sessionsDir = "sessions"
	hostsDir    = "hosts"
	pendingFile = "pending"
)

// OpenProject returns the project whose state lives in dir, creating the directory if needed.
func OpenProject(dir string, now func() time.Time) (*Project, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := &Project{Dir: dir, Root: filepath.Dir(dir), Now: now}
	if err := p.migrate(); err != nil {
		return nil, fmt.Errorf("moving the run in %s into %s/: %w", dir, runsDir, err)
	}
	return p, nil
}

func (p *Project) path(parts ...string) string {
	return filepath.Join(append([]string{p.Dir}, parts...)...)
}

func (p *Project) withLock(fn func() error) error { return withLockFile(p.path("lock"), fn) }

// Run opens the run with this id, writing as instance.
func (p *Project) Run(id, instance string) (*Store, error) {
	if !validID(id) {
		return nil, fmt.Errorf("no run %q here", id)
	}
	return openIn(p.path(runsDir, id), p.Root, instance, p.Now)
}

func (p *Project) runExists(id string) bool {
	if !validID(id) {
		return false
	}
	fi, err := os.Stat(p.path(runsDir, id))
	return err == nil && fi.IsDir()
}

// validID keeps ids (session ids, run ids, instances) to names that are safe as one path element.
func validID(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, `/\`+"\x00")
}

func (p *Project) readLink(parts ...string) string {
	b, err := os.ReadFile(p.path(parts...))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (p *Project) writeLink(id string, parts ...string) error {
	path := p.path(parts...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, []byte(id+"\n"))
}

// RunOf returns the id of the run session belongs to, or "".
func (p *Project) RunOf(session string) string {
	if !validID(session) {
		return ""
	}
	if id := p.readLink(sessionsDir, session); p.runExists(id) {
		return id
	}
	return ""
}

// HostRun returns the id of the run the host instance drives, or "".
func (p *Project) HostRun(instance string) string {
	if !validID(instance) {
		return ""
	}
	if id := p.readLink(hostsDir, instance); p.runExists(id) {
		return id
	}
	return ""
}

// Binding says who is asking for a run.
type Binding struct {
	Session  string // the Claude Code session id ("" when unknown)
	Instance string // the baton host's instance ("" outside a hosted session)
	// Carry: the session just replaced the host's previous one in the same conversation (/clear, or a
	// plan approved with "clear context"), so it continues that session's run.
	Carry bool
}

// ErrNoSession means a run was asked for with neither a session nor a host to find it by.
var ErrNoSession = errors.New("no Claude Code session to find a run for")

// Bind returns the run b's session belongs to, creating or adopting one if it has none, and makes the
// host instance (if any) its driver unless another live host drives it already. A run driven by
// another host is still returned: its owner check (IsOwner) is what keeps this session's hooks out of
// it, until that host goes away and this one takes over.
//
// A new session takes, in order: the run its host carries over (b.Carry), a run attached outside any
// session (pending, hosted sessions only), or a new run of its own.
func (p *Project) Bind(b Binding) (*Store, error) {
	if b.Session != "" && !validID(b.Session) {
		return nil, fmt.Errorf("unusable session id %q", b.Session)
	}
	// The common case, every hook of a session already bound to its host, takes no lock.
	if id := p.RunOf(b.Session); id != "" && (b.Instance == "" || p.HostRun(b.Instance) == id) {
		return p.Run(id, b.Instance)
	}
	var s *Store
	err := p.withLock(func() error {
		id := p.RunOf(b.Session)
		if id == "" && b.Carry {
			id = p.HostRun(b.Instance)
		}
		if id == "" && b.Session != "" && b.Instance != "" {
			if pend := p.readLink(pendingFile); p.runExists(pend) {
				id = pend
				os.Remove(p.path(pendingFile))
			}
		}
		if id == "" && b.Session == "" {
			if id = p.HostRun(b.Instance); id == "" {
				return ErrNoSession
			}
		}
		if id == "" {
			id = b.Session
		}
		var err error
		if s, err = p.Run(id, b.Instance); err != nil {
			return err
		}
		if b.Session != "" && p.readLink(sessionsDir, b.Session) != id {
			if err := p.writeLink(id, sessionsDir, b.Session); err != nil {
				return err
			}
		}
		if b.Instance == "" {
			return nil
		}
		now := p.Now()
		if _, err := s.Update(func(st *State) error { return Claim(st, b.Instance, os.Getpid(), now) }); err != nil {
			return nil // another live host drives this run
		}
		if prev := p.HostRun(b.Instance); prev != "" && prev != id {
			// The host moved to another session (/resume inside Claude Code): let its old run go.
			if old, err := p.Run(prev, b.Instance); err == nil {
				old.Update(func(st *State) error { Release(st, b.Instance); return nil })
			}
		}
		return p.writeLink(id, hostsDir, b.Instance)
	})
	return s, err
}

// Lookup returns the run session (or else the host instance) belongs to, without binding anything.
func (p *Project) Lookup(session, instance string) (*Store, bool) {
	id := p.RunOf(session)
	if id == "" {
		id = p.HostRun(instance)
	}
	if id == "" {
		return nil, false
	}
	s, err := p.Run(id, instance)
	return s, err == nil
}

// Unbind forgets which run the host instance drives, and lets the run go if the host held it.
func (p *Project) Unbind(instance string) {
	if !validID(instance) {
		return
	}
	p.withLock(func() error {
		if id := p.HostRun(instance); id != "" {
			if s, err := p.Run(id, instance); err == nil {
				s.Update(func(st *State) error { Release(st, instance); return nil })
			}
		}
		return os.Remove(p.path(hostsDir, instance))
	})
}

// Pending returns the run attached outside any session, creating it if there is none. The next baton
// session started in this project takes it over.
func (p *Project) Pending(instance string) (*Store, error) {
	var s *Store
	err := p.withLock(func() error {
		id := p.readLink(pendingFile)
		if !p.runExists(id) {
			id = "shell-" + p.Now().UTC().Format("20060102T150405Z") + "-" + randomHex(3)
			if err := p.writeLink(id, pendingFile); err != nil {
				return err
			}
		}
		var err error
		s, err = p.Run(id, instance)
		return err
	})
	return s, err
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// RunInfo describes one run, for listings.
type RunInfo struct {
	ID       string
	Sessions []string // the sessions that have belonged to it
	Pending  bool     // attached outside any session; the next session started here takes it
	Title    string   // the attached plan's title, "" without one
	State    State
	Updated  time.Time // when its state last changed
}

// Live reports whether a host drives the run right now.
func (r RunInfo) Live(now time.Time) bool { return r.State.Owner.Live(now) }

// HasPlan reports whether the run has a plan attached (running, paused or complete).
func (r RunInfo) HasPlan() bool { return r.Title != "" && r.State.Mode != ModeIdle }

// Runs lists the project's runs, most recently changed first.
func (p *Project) Runs() []RunInfo {
	entries, _ := os.ReadDir(p.path(runsDir))
	sessions := map[string][]string{}
	links, _ := os.ReadDir(p.path(sessionsDir))
	for _, l := range links {
		if id := p.readLink(sessionsDir, l.Name()); id != "" {
			sessions[id] = append(sessions[id], l.Name())
		}
	}
	pending := p.readLink(pendingFile)
	var out []RunInfo
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		dir := p.path(runsDir, e.Name())
		info := RunInfo{ID: e.Name(), Sessions: sessions[e.Name()], Pending: e.Name() == pending, State: New()}
		if b, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
			json.Unmarshal(b, &info.State)
		}
		if fi, err := os.Stat(filepath.Join(dir, "state.json")); err == nil {
			info.Updated = fi.ModTime()
		}
		if b, err := os.ReadFile(filepath.Join(dir, "plan.json")); err == nil {
			var pl struct {
				Title string `json:"title"`
			}
			json.Unmarshal(b, &pl)
			info.Title = pl.Title
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// Others lists the runs other than id that a host drives right now: sessions working in the same
// working tree at the same time.
func (p *Project) Others(id string) []RunInfo {
	now := p.Now()
	var out []RunInfo
	for _, r := range p.Runs() {
		if r.ID != id && r.Live(now) {
			out = append(out, r)
		}
	}
	return out
}

// ErrSeveralRuns means a command run outside any session could mean more than one run.
type ErrSeveralRuns struct{ Runs []RunInfo }

func (e ErrSeveralRuns) Error() string {
	var b strings.Builder
	b.WriteString("this project has several runs; run this inside the session whose run you mean:")
	for _, r := range e.Runs {
		fmt.Fprintf(&b, "\n  %s  %q (%s)", r.ID, r.Title, r.State.Mode)
	}
	return b.String()
}

// Pick returns the run a command run outside any session means: the run attached from a shell if there
// is one, or else the one run with a plan.
func (p *Project) Pick(instance string) (*Store, error) {
	if id := p.readLink(pendingFile); p.runExists(id) {
		return p.Run(id, instance)
	}
	var withPlan []RunInfo
	for _, r := range p.Runs() {
		if r.HasPlan() {
			withPlan = append(withPlan, r)
		}
	}
	switch len(withPlan) {
	case 0:
		return nil, ErrNoPlan
	case 1:
		return p.Run(withPlan[0].ID, instance)
	}
	return nil, ErrSeveralRuns{withPlan}
}

// sweepAge is how old a run with nothing in it must be before Sweep removes it, so that a run being
// created right now (a session attaching its first plan) is never swept from under it.
const sweepAge = 10 * time.Minute

// Sweep removes what no one can use any more: runs that never had a plan, drafts or notes and that no
// host drives (every hosted session starts one), and links to runs or hosts that are gone. Hosts call
// it when they start.
func (p *Project) Sweep() {
	p.withLock(func() error {
		now := p.Now()
		pending := p.readLink(pendingFile)
		for _, r := range p.Runs() {
			dir := p.path(runsDir, r.ID)
			if r.ID == pending || r.Title != "" || r.Live(now) || now.Sub(r.Updated) < sweepAge || r.Updated.IsZero() && !olderThan(dir, now) {
				continue
			}
			if exists(filepath.Join(dir, "plan.json")) || exists(filepath.Join(dir, "handoff.md")) || hasEntries(filepath.Join(dir, DraftsDirName)) {
				continue
			}
			os.RemoveAll(dir)
		}
		for _, sub := range []string{sessionsDir, hostsDir} {
			links, _ := os.ReadDir(p.path(sub))
			for _, l := range links {
				id := p.readLink(sub, l.Name())
				if !p.runExists(id) {
					os.Remove(p.path(sub, l.Name()))
					continue
				}
				if sub == hostsDir {
					if s, err := p.Run(id, ""); err == nil {
						if st, err := s.peek(); err == nil && !(st.Owner != nil && st.Owner.Instance == l.Name() && st.Owner.Live(now)) {
							os.Remove(p.path(sub, l.Name()))
						}
					}
				}
			}
		}
		return nil
	})
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func hasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func olderThan(dir string, now time.Time) bool {
	fi, err := os.Stat(dir)
	return err == nil && now.Sub(fi.ModTime()) >= sweepAge
}

// legacyFiles are what a run kept directly in .baton/ before v0.3.
var legacyFiles = []string{"state.json", "plan.json", "events.jsonl", "handoff.md", DraftsDirName}

// migrate moves a run kept in .baton/ itself (before v0.3) into runs/, named after its session. While an
// older baton still drives it, it stays where that baton expects it; it moves once that host is gone.
// Both versions lock .baton/lock, so the move never meets one of the older baton's writes.
func (p *Project) migrate() error {
	if !exists(p.path("state.json")) {
		return nil
	}
	return p.withLock(func() error {
		b, err := os.ReadFile(p.path("state.json"))
		if errors.Is(err, os.ErrNotExist) {
			return nil // another process moved it first
		}
		if err != nil {
			return err
		}
		var st State
		if err := json.Unmarshal(b, &st); err != nil {
			return err
		}
		if st.Owner.Live(p.Now()) {
			return nil
		}
		id := st.Run.SessionID
		if !validID(id) {
			id = "legacy-" + p.Now().UTC().Format("20060102T150405Z")
		}
		if p.runExists(id) {
			id += "-" + randomHex(3)
		}
		dst := p.path(runsDir, id)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		for _, name := range legacyFiles {
			if err := os.Rename(p.path(name), filepath.Join(dst, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if validID(st.Run.SessionID) {
			return p.writeLink(id, sessionsDir, st.Run.SessionID)
		}
		return nil
	})
}
