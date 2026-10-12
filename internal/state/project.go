package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A Project is a working tree's .baton directory. It keeps one run per Claude Code session, so every
// baton terminal in the same working tree runs a plan of its own:
//
//	.baton/runs/<run>/       one run: plan.json, state.json, events.jsonl, handoff.md, drafts/, jobs/ (see Store)
//	.baton/runs/<run>/workers/<phase>/
//	                         a worker's sub-run: phase of <run>, worked on by a session of its own in a git
//	                         worktree of its own (named in the sub-run's worktree file; see NewWorker)
//	.baton/sessions/<id>     the run a session belongs to (one line: the run's ref)
//	.baton/hosts/<instance>  the run a baton host drives (one line: the run's ref)
//	.baton/agents/<agent>    the sub-run a phase agent works in (one line: the sub-run's ref; LinkAgent)
//	.baton/pending           a run attached outside any session, for the next baton session started here
//	.baton/lock              held while sessions and hosts are bound to runs
//	.baton/baton.log         the hosts' log, each line tagged with its instance
//
// A run is named after the session it started in. A session keeps its run when it is resumed, and the
// conversation keeps it when /clear or a "clear context" plan approval gives it a new session id in the
// same terminal (docs/research/sessions.md). A ref names a run: "<run>" for a run of its own, or
// "<run>/<phase>" for a worker's sub-run (WorkerRef). Until v0.3 a project had a single run kept in .baton/
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
	agentsDir   = "agents"
	pendingFile = "pending"
	workersDir  = "workers"
	treeFile    = "worktree" // in a worker's sub-run: the worktree it works in
	sessionFile = "session"  // in a worker's sub-run: its session's id
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

// WorkerRef is the ref of the sub-run of run's worker on phase.
func WorkerRef(run, phase string) string { return run + "/" + phase }

// WorkerInstance is the instance a worker's session runs as, under the host instance that started it.
func WorkerInstance(instance, phase string) string { return instance + "-" + phase }

// splitRef returns the run and the phase (for a worker's sub-run) a ref names, and whether each is safe
// as one path element.
func splitRef(ref string) (run, phase string, ok bool) {
	run, phase, sub := strings.Cut(ref, "/")
	if !validID(run) || sub && !validID(phase) {
		return "", "", false
	}
	return run, phase, true
}

// isWorker reports whether ref names a worker's sub-run.
func isWorker(ref string) bool {
	_, phase, ok := splitRef(ref)
	return ok && phase != ""
}

func (p *Project) refDir(run, phase string) string {
	if phase == "" {
		return p.path(runsDir, run)
	}
	return p.path(runsDir, run, workersDir, phase)
}

// Run opens the run ref names, writing as instance. A run of its own is created if there is none yet; a
// worker's sub-run is only ever made by NewWorker, and works in the worktree it names.
func (p *Project) Run(ref, instance string) (*Store, error) {
	run, phase, ok := splitRef(ref)
	if !ok {
		return nil, fmt.Errorf("no run %q here", ref)
	}
	if phase == "" {
		return p.open(p.refDir(run, ""), p.Root, ref, "", instance)
	}
	dir := p.refDir(run, phase)
	tree := readLine(filepath.Join(dir, treeFile))
	if tree == "" {
		return nil, fmt.Errorf("no worker %q here", ref)
	}
	s, err := p.open(dir, tree, ref, run, instance)
	if err == nil {
		s.Session = readLine(filepath.Join(dir, sessionFile))
	}
	return s, err
}

func (p *Project) open(dir, root, ref, parent, instance string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{Dir: dir, Root: root, Checkout: p.Root, Project: p.Dir, Ref: ref, Parent: parent, Instance: instance, Now: p.Now}, nil
}

// ExistingRun opens the run ref names, writing as instance, if there is one.
func (p *Project) ExistingRun(ref, instance string) (*Store, error) {
	if !p.runExists(ref) {
		return nil, fmt.Errorf("no run %q here", ref)
	}
	return p.Run(ref, instance)
}

func (p *Project) runExists(ref string) bool {
	run, phase, ok := splitRef(ref)
	if !ok {
		return false
	}
	if phase != "" {
		return exists(filepath.Join(p.refDir(run, phase), treeFile))
	}
	fi, err := os.Stat(p.refDir(run, ""))
	return err == nil && fi.IsDir()
}

// validID keeps ids (session ids, run ids, instances) to names that are safe as one path element.
func validID(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, `/\`+"\x00")
}

func (p *Project) readLink(parts ...string) string { return readLine(p.path(parts...)) }

// readLine reads a file of one line, or "".
func readLine(path string) string {
	b, err := os.ReadFile(path)
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

// RunOf returns the ref of the run session belongs to, or "".
func (p *Project) RunOf(session string) string {
	if !validID(session) {
		return ""
	}
	if id := p.readLink(sessionsDir, session); p.runExists(id) {
		return id
	}
	return ""
}

// HostRun returns the ref of the run the host instance drives, or "".
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
	// Worker: the session is a worker's (BATON_ROLE=worker), which its host linked to its sub-run before
	// starting it (NewWorker).
	Worker bool
}

// ErrNoSession means a run was asked for with neither a session nor a host to find it by.
var ErrNoSession = errors.New("no Claude Code session to find a run for")

// ErrNotLinked means a worker's session has no sub-run linked to it.
var ErrNotLinked = errors.New("this worker's session has no run linked to it")

// Bind returns the run b's session belongs to, creating or adopting one if it has none, and makes the
// host instance (if any) its driver unless another live host drives it already. A run driven by
// another host is still returned: its owner check (IsOwner) is what keeps this session's hooks out of
// it, until that host goes away and this one takes over.
//
// A new session takes, in order: the run its host carries over (b.Carry), a run attached outside any
// session (pending, hosted sessions only), or a new run of its own.
//
// A worker's sub-run is only ever found: its host links it before the worker starts and drives it, so
// Bind never creates, adopts or claims one, and a worker's session never gets any other run. The one link
// it adds is a worker's /clear (b.Carry), which keeps the sub-run the worker's host instance drives.
func (p *Project) Bind(b Binding) (*Store, error) {
	if b.Session != "" && !validID(b.Session) {
		return nil, fmt.Errorf("unusable session id %q", b.Session)
	}
	id, host := p.RunOf(b.Session), p.HostRun(b.Instance)
	if b.Session == "" && isWorker(host) {
		id = host
	}
	switch {
	case isWorker(id):
		return p.Run(id, b.Instance)
	case b.Carry && b.Session != "" && isWorker(host):
		return p.carryWorker(b.Session, host, b.Instance)
	case b.Worker || isWorker(host):
		return nil, ErrNotLinked // a worker's instance never binds anything else
	}
	// The common case, every hook of a session already bound to its host, takes no lock.
	if id != "" && (b.Instance == "" || host == id) {
		return p.Run(id, b.Instance)
	}
	var s *Store
	err := p.withLock(func() error {
		id := p.RunOf(b.Session)
		if id == "" && b.Carry {
			if id = p.HostRun(b.Instance); isWorker(id) {
				id = ""
			}
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
		if isWorker(id) { // linked as a worker's since the check above
			s, err = p.Run(id, b.Instance)
			return err
		}
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

// carryWorker links session, which /clear started in the conversation of the worker whose host instance
// drives the sub-run ref, to that sub-run: a worker's conversation keeps its sub-run, as a lead's keeps its
// run. The worker's roster entry keeps the session its host started it with.
func (p *Project) carryWorker(session, ref, instance string) (*Store, error) {
	var s *Store
	err := p.withLock(func() error {
		if p.HostRun(instance) != ref || p.RunOf(session) != "" && p.RunOf(session) != ref {
			return ErrNotLinked
		}
		run, phase, _ := splitRef(ref)
		if err := writeAtomic(filepath.Join(p.refDir(run, phase), sessionFile), []byte(session+"\n")); err != nil {
			return err
		}
		if err := p.writeLink(ref, sessionsDir, session); err != nil {
			return err
		}
		var err error
		s, err = p.Run(ref, instance)
		return err
	})
	return s, err
}

// Link links session to run, a run of its own, so that the session's first hook binds it (Bind finds it
// by RunOf) rather than a new run named after it: the host starts a fresh session, under an id it chose,
// for a run whose conversation could not be resumed. It fails for a run that does not exist, and for a
// session that belongs to another run.
func (p *Project) Link(session, run string) error {
	if !validID(session) || isWorker(run) {
		return fmt.Errorf("cannot link session %q to run %q", session, run)
	}
	return p.withLock(func() error {
		if !p.runExists(run) {
			return fmt.Errorf("no run %q here", run)
		}
		if cur := p.RunOf(session); cur != "" && cur != run {
			return fmt.Errorf("session %s belongs to run %s", session, cur)
		}
		return p.writeLink(run, sessionsDir, session)
	})
}

// LinkAgent links agentID, the phase agent of the lead's session working on phase of run, to that
// phase's sub-run, which must exist, so that every hook the agent fires finds the sub-run by the agent's
// id (AgentRun) and writes as its worker instance (WorkerInstance). Agents are linked by id, never by
// session: they share the lead's session, and /clear gives their hooks the next session's id (spike 32).
// Linking another agent to a phase (one started again) leaves the first linked as well.
func (p *Project) LinkAgent(run, phase, agentID string) error {
	ref := WorkerRef(run, phase)
	if !validID(agentID) || !isWorker(ref) {
		return fmt.Errorf("cannot link agent %q to %q", agentID, ref)
	}
	return p.withLock(func() error {
		if !p.runExists(ref) {
			return fmt.Errorf("no worker %q here", ref)
		}
		return p.writeLink(ref, agentsDir, agentID)
	})
}

// AgentRun returns the ref of the sub-run the phase agent agentID is linked to (LinkAgent), or "".
func (p *Project) AgentRun(agentID string) string {
	if !validID(agentID) {
		return ""
	}
	if ref := p.readLink(agentsDir, agentID); isWorker(ref) && p.runExists(ref) {
		return ref
	}
	return ""
}

// WorktreeOf returns the phase of run whose sub-run's worktree holds dir (a real path, or one that
// resolves to it), or "" for none.
func (p *Project) WorktreeOf(run, dir string) string {
	if dir == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	for _, w := range p.Workers(run) {
		if w.Worktree == "" {
			continue
		}
		if rel, err := filepath.Rel(w.Worktree, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			_, phase, _ := splitRef(w.ID)
			return phase
		}
	}
	return ""
}

// PhaseAt returns the sub-run of the phase whose worktree holds dir, among the workers of the run session
// (or else the host instance) belongs to, writing as that phase's worker instance (WorkerInstance of
// instance). A phase agent shares the lead's session and its environment: where it works is what says
// which phase it is, as its worktree is the phase's alone.
func (p *Project) PhaseAt(session, instance, dir string) (*Store, bool) {
	run := p.RunOf(session)
	if run == "" {
		run = p.HostRun(instance)
	}
	if run == "" || isWorker(run) || instance == "" {
		return nil, false
	}
	phase := p.WorktreeOf(run, dir)
	if phase == "" {
		return nil, false
	}
	s, err := p.Run(WorkerRef(run, phase), WorkerInstance(instance, phase))
	return s, err == nil
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

// Pended returns the run attached outside any session, if there is one.
func (p *Project) Pended(instance string) (*Store, bool) {
	id := p.readLink(pendingFile)
	if !p.runExists(id) {
		return nil, false
	}
	s, err := p.Run(id, instance)
	return s, err == nil
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

// NewSessionID returns a random (version 4) UUID, the form Claude Code's session ids take: baton picks
// the id of a session it starts (--session-id), so that its run is linked before claude has drawn
// anything.
func NewSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// RunInfo describes one run, for listings.
type RunInfo struct {
	ID       string   // its ref
	Sessions []string // the sessions that have belonged to it
	Worktree string   // a worker's sub-run: the worktree it works in
	Pending  bool     // attached outside any session; the next session started here takes it
	Title    string   // the attached plan's title, "" without one
	State    State
	Updated  time.Time // when its state last changed
}

// Live reports whether a host drives the run right now.
func (r RunInfo) Live(now time.Time) bool { return r.State.Owner.Live(now) }

// HasPlan reports whether the run has a plan attached (running, paused or complete).
func (r RunInfo) HasPlan() bool { return r.Title != "" && r.State.Mode != ModeIdle }

// Runs lists the project's runs, most recently changed first. Workers' sub-runs belong to their runs and
// are not listed (Workers lists them).
func (p *Project) Runs() []RunInfo {
	entries, _ := os.ReadDir(p.path(runsDir))
	sessions := p.sessions()
	pending := p.readLink(pendingFile)
	var out []RunInfo
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		info := p.info(e.Name(), p.refDir(e.Name(), ""), sessions)
		info.Pending = e.Name() == pending
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// Workers lists the sub-runs of run's workers, by phase.
func (p *Project) Workers(run string) []RunInfo {
	if !validID(run) {
		return nil
	}
	entries, _ := os.ReadDir(filepath.Join(p.refDir(run, ""), workersDir))
	sessions := p.sessions()
	var out []RunInfo
	for _, e := range entries {
		ref := WorkerRef(run, e.Name())
		if !e.IsDir() || !p.runExists(ref) {
			continue
		}
		dir := p.refDir(run, e.Name())
		info := p.info(ref, dir, sessions)
		info.Worktree = readLine(filepath.Join(dir, treeFile))
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sessions maps each run's ref to the sessions linked to it.
func (p *Project) sessions() map[string][]string {
	sessions := map[string][]string{}
	links, _ := os.ReadDir(p.path(sessionsDir))
	for _, l := range links {
		if id := p.readLink(sessionsDir, l.Name()); id != "" {
			sessions[id] = append(sessions[id], l.Name())
		}
	}
	return sessions
}

// info describes the run ref, whose directory is dir, without its lock.
func (p *Project) info(ref, dir string, sessions map[string][]string) RunInfo {
	info := RunInfo{ID: ref, Sessions: sessions[ref], State: New()}
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
	return info
}

// NewWorker makes the sub-run of a worker on phase of run, before the worker's session starts: its
// directory, the worktree it works in (by its real path), and the links that find it from session and
// from the worker's instance (WorkerInstance of the host instance), which it claims. The worker's hooks
// and CLI then find the sub-run by their session id, and never bind anything (Bind). Making it again, for
// a worker started anew on the same phase, relinks it and keeps what it holds.
func (p *Project) NewWorker(run, phase, session, instance, worktree string) (*Store, error) {
	ref, inst := WorkerRef(run, phase), WorkerInstance(instance, phase)
	switch {
	case !isWorker(ref) || !validID(session) || !validID(instance) || !validID(inst):
		return nil, fmt.Errorf("unusable worker %q (session %q, instance %q)", ref, session, instance)
	case !p.runExists(run):
		return nil, fmt.Errorf("no run %q here", run)
	case worktree == "":
		return nil, fmt.Errorf("worker %s has no worktree", ref)
	}
	if abs, err := filepath.Abs(worktree); err == nil {
		worktree = abs
	}
	if real, err := filepath.EvalSymlinks(worktree); err == nil {
		worktree = real
	}
	var s *Store
	err := p.withLock(func() error {
		dir := p.refDir(run, phase)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(dir, treeFile), []byte(worktree+"\n")); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(dir, sessionFile), []byte(session+"\n")); err != nil {
			return err
		}
		var err error
		if s, err = p.Run(ref, inst); err != nil {
			return err
		}
		now := p.Now()
		if _, err := s.Update(func(st *State) error { return Claim(st, inst, os.Getpid(), now) }); err != nil {
			return err
		}
		if err := p.writeLink(ref, sessionsDir, session); err != nil {
			return err
		}
		return p.writeLink(ref, hostsDir, inst)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Others lists the runs other than id that a host drives right now: sessions working in the same
// working tree at the same time. Workers work in worktrees of their own, and are not among them.
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
// is one, or else the one run under way (running or paused), or else the one run with a plan at all.
func (p *Project) Pick(instance string) (*Store, error) {
	if id := p.readLink(pendingFile); p.runExists(id) {
		return p.Run(id, instance)
	}
	var underWay, withPlan []RunInfo
	for _, r := range p.Runs() {
		if r.HasPlan() {
			withPlan = append(withPlan, r)
			if r.State.Mode == ModeRunning || r.State.Mode == ModePaused {
				underWay = append(underWay, r)
			}
		}
	}
	for _, runs := range [][]RunInfo{underWay, withPlan} {
		switch len(runs) {
		case 0:
			continue
		case 1:
			return p.Run(runs[0].ID, instance)
		}
		return nil, ErrSeveralRuns{runs}
	}
	return nil, ErrNoPlan
}

// sweepAge is how old a run with nothing in it must be before Sweep removes it, so that a run being
// created right now (a session attaching its first plan) is never swept from under it.
const sweepAge = 10 * time.Minute

// Sweep removes what no one can use any more: runs that never had a plan, drafts or notes and that no
// host drives (every hosted session starts one), links to runs (workers' sub-runs among them) that are
// gone, from sessions and from phase agents, and links to hosts that no longer drive their run. Hosts call
// it when they start.
func (p *Project) Sweep() { p.Swept(false, nil) }

// Swept is Sweep, saying what it removed, each as its path under the project's .baton directory, with
// forward slashes on every platform (runs/<run>, sessions/<session>, hosts/<instance>, agents/<agent>). With dry, it removes nothing, and says what Sweep
// would remove. keep (nil: none) names hosts whose links stay, though they do not drive their run now.
func (p *Project) Swept(dry bool, keep func(instance string) bool) []string {
	var out []string
	p.withLock(func() error {
		now := p.Now()
		pending := p.readLink(pendingFile)
		gone := map[string]bool{} // runs a dry sweep would remove
		for _, r := range p.Runs() {
			dir := p.path(runsDir, r.ID)
			if r.ID == pending || r.Title != "" || r.Live(now) || now.Sub(r.Updated) < sweepAge || r.Updated.IsZero() && !olderThan(dir, now) {
				continue
			}
			if exists(filepath.Join(dir, "plan.json")) || exists(filepath.Join(dir, "handoff.md")) || hasEntries(filepath.Join(dir, DraftsDirName)) {
				continue
			}
			out, gone[r.ID] = append(out, path.Join(runsDir, r.ID)), true
			if !dry {
				os.RemoveAll(dir)
			}
		}
		for _, sub := range []string{sessionsDir, hostsDir, agentsDir} {
			links, _ := os.ReadDir(p.path(sub))
			for _, l := range links {
				id := p.readLink(sub, l.Name())
				run, _, _ := splitRef(id)
				stale := !p.runExists(id) || gone[run]
				if !stale && sub == hostsDir && (keep == nil || !keep(l.Name())) {
					if s, err := p.Run(id, ""); err == nil {
						if st, err := s.peek(); err == nil && !(st.Owner != nil && st.Owner.Instance == l.Name() && st.Owner.Live(now)) {
							stale = true
						}
					}
				}
				if stale {
					out = append(out, path.Join(sub, l.Name()))
					if !dry {
						os.Remove(p.path(sub, l.Name()))
					}
				}
			}
		}
		return nil
	})
	return out
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
	// Checked without the lock first: while an older baton drives the run, every hook of every newer
	// session in the project would otherwise queue on the project's lock just to find that out.
	var legacy State
	if b, err := os.ReadFile(p.path("state.json")); err != nil || json.Unmarshal(b, &legacy) != nil || legacy.Owner.Live(p.Now()) {
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
