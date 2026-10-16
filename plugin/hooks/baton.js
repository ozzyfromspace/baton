// baton's mod: what only something inside the lead's claude can do, on baton's say-so. It starts and
// messages phase agents, asks the human, keeps phase agents' reports from the lead's model, compacts the
// lead's conversation and submits baton's lines to it (so that baton types nothing into the session, and
// the human's draft stays in the input box: spike 35), and reports what it sees. It decides nothing and words nothing: the baton host does both (internal/bridge). It does
// nothing unless baton hosts this session and answers its hello, and every hook lets its event through
// when anything goes wrong.
//
// The host writes numbered commands to <dir>/out/<id>.json, each for the host instance it runs as; the
// mod runs those of its own claude's host, in order, one at a time, from its poll timer. It reports by
// running `baton mod report` with one numbered report on stdin, and keeps each report until the host has
// recorded it. In the store it keeps, per host instance, the last command it ran, the one it is running,
// the compaction it holds for the lead's turn's end, and the reports not recorded yet, so that once
// reloaded it neither repeats nor skips one.
//
// Claude Code compacts between turns only, and refuses a compaction asked for while the lead's turn runs:
// the host asks for one once the lead's Stop hook has run, and the turn ends a moment later. A refusal
// while the lead's turn runs, by the mod's own account of its turn.start and turn.complete, is not acked:
// the command is held, and run from that turn's turn.complete, once, for up to HOLD_MS; past that its ack
// carries the refusal, and the host asks again after the turn. This is timing, not a choice.
//
// It draws baton's view of the run (internal/board) as the host words it: every second it reads `baton
// board --band` for the band above the prompt, and, while the /baton board pane is open, `baton board`
// for the pane.
//
// It asks the human baton's questions from inside the band's render hook, one at a time: only there does
// it see its own question's call (from a timer it would not), and the band is never drawn while another
// dialog is open, so a question of baton's never waits behind a permission prompt. Its tool.call hook
// bounds every question in the lead's session, baton's and the lead model's own: it asks the host how
// long the question may wait, and at the deadline answers the call itself with the answer the host gives,
// which closes the dialog. Nothing types into a dialog (spike 34).

const PROTOCOL = 2 // what the mod and the host say to each other (bridge.Protocol)
const POLL_MS = 500
const HELLO_FAST_MS = 1000 // how often a hello is tried at first,
const HELLO_FAST_TRIES = 30 // this many times,
const HELLO_SLOW_MS = 10000 // and then
const REPORT_TIMEOUT_MS = 5000
const KEEP_MS = 7 * 24 * 3600 * 1000 // the store forgets a host instance unused this long
const CUT = 1000 // the most of an agent's last words a turn report carries
const AT_ONCE_MS = 250 // how long a compaction or a line is watched for a refusal at once (a turn running), which its ack carries
const HOLD_MS = 30000 // how long a compaction refused while the lead's turn runs waits for that turn's end (within the host's modAckWait)
const VIEW_MS = 1000 // how often the band, and the board while its pane is open, are read again
const VIEW_TIMEOUT_MS = 5000
const ASK_RETRY_MS = 5000 // how soon the host is asked about a question again when it gave no answer
const PANE = 'baton-board' // the pane of baton's board
const SKILL = 'baton:baton' // the /baton skill's command, as Claude Code names it to a command.run hook
const BOARD = 'baton-board' // the board's own command, which runs while Claude works (/baton waits for the turn)

// What the mod knows. env: what the host set for this claude; ready: the host answered its hello; dir: the
// bridge's directory; agents: baton's agents by id ({ phase, drop }); keep: what is kept in the store.
const M = {
  env: null,
  ready: false,
  off: false,
  version: '',
  dir: '',
  agents: {},
  agentsText: '',
  keep: null,
  key: '',
  polling: false,
  flushing: null,
  hello: null,
  helloing: false,
  tries: 0,
  poll: null,
  registered: false,
  spec: null,
  view: null,
  viewing: false,
  band: '', // the band as the host last worded it ('' for none)
  board: '', // the board as the host last worded it
  width: 0, // the columns the band was last drawn in
  paneWidth: 0, // and the board's pane
  boardSpec: null, // the board command's and pane's words, from the host's hello
  boardOn: false, // the board command is registered
  asks: [], // the host's questions waiting for the band to ask them, oldest first
  asking: null, // the question on the human's screen now
  settled: {}, // the questions the mod answered at their deadlines, by text
  held: null, // baton's compaction held for the end of the lead's turn: { c, error, timer }
  turn: '', // the lead's turn running, by its turn.start and turn.complete ('' for none)
  own: false, // the mod's own compaction, called from its turn.complete hook, runs (its session.compact hook sees it)
}

// env reads what the baton host set for this claude ($.env.get takes literal names only).
async function env($) {
  if (M.env) return M.env
  M.env = {
    host: await $.env.get('BATON_HOST'),
    bin: await $.env.get('BATON_BIN'),
    instance: await $.env.get('BATON_INSTANCE'),
  }
  return M.env
}

// hosted says whether baton hosts this session; elsewhere the mod does nothing at all.
async function hosted($) {
  const e = await env($)
  return e.host === '1' && !!e.bin && !!e.instance
}

// call runs `baton mod report` with one report, and resolves its reply, or null when it gave none.
async function call($, report) {
  const e = await env($)
  let out
  try {
    const session = await $.session.id()
    out = await $.process.run([e.bin, 'mod', 'report'], { stdin: JSON.stringify({ ...report, session }), timeoutMs: REPORT_TIMEOUT_MS })
  } catch (err) {
    return null
  }
  try {
    const reply = JSON.parse(out.stdout)
    return reply && typeof reply === 'object' ? reply : null
  } catch (err) {
    return null
  }
}

// load reads what the store keeps for this claude's host instance, and acks as interrupted a command the
// mod was running when it was reloaded: it may or may not have happened, and the host decides what next.
// A compaction it held for the lead's turn's end, and had not called again, is acked with its refusal.
async function load($) {
  const e = await env($)
  M.key = 'baton:' + e.instance
  const kept = await $.store.get(M.key)
  M.keep = kept && typeof kept === 'object' ? kept : { last: 0, running: 0, seq: 0, outbox: [] }
  if (!Array.isArray(M.keep.outbox)) M.keep.outbox = []
  if (M.keep.running > M.keep.last) {
    const id = M.keep.running
    M.keep.last = id
    M.keep.running = 0
    await report($, { kind: 'ack', command: id, interrupted: true })
  }
  const h = M.keep.held
  M.keep.held = null
  if (h && typeof h === 'object') {
    await report($, h.called ? { kind: 'ack', command: h.id, interrupted: true } : { kind: 'ack', command: h.id, ok: false, error: String(h.error || '') })
  }
  await save($)
}

async function save($) {
  M.keep.at = await $.clock.now()
  await $.store.set(M.key, M.keep)
}

// forget drops what the store keeps for host instances unused for a week (the store is shared by every
// session that loads the plugin).
async function forget($) {
  const now = await $.clock.now()
  for (const key of await $.store.keys()) {
    if (!key.startsWith('baton:') || key === M.key) continue
    const kept = await $.store.get(key)
    if (!kept || typeof kept !== 'object' || !(now - (kept.at || 0) < KEEP_MS)) await $.store.delete(key)
  }
}

// sayHello tries the hello until the host answers. OK: the mod starts polling. Asked to wait (no run yet,
// another baton session drives the run): it tries again, often at first, then every few seconds. No reply
// it can read (a baton too old for a mod), or a no: it does nothing more.
async function sayHello($) {
  if (M.ready || M.off || M.helloing) return
  M.helloing = true
  try {
    await hello($)
  } finally {
    M.helloing = false
  }
}

async function hello($) {
  M.tries++
  const r = await call($, { kind: 'hello', version: M.version, protocol: PROTOCOL })
  if (r && r.ok) {
    M.ready = true
    M.dir = String(r.dir || '')
    if (M.hello) M.hello.cancel()
    M.hello = null
    const run = String(r.run || '')
    if (M.keep.run && M.keep.run !== run) {
      // Another run's bridge (the host drives another run now): its commands are numbered afresh, and
      // what the mod kept to report was the other run's.
      M.keep.last = 0
      M.keep.running = 0
      M.keep.outbox = []
      forsake()
    }
    M.keep.run = run
    if (M.keep.seq < (r.seen || 0)) M.keep.seq = r.seen
    await save($)
    await registerType($, r.agent)
    await registerBoard($, r.board)
    if (!M.poll) M.poll = $.clock.every(POLL_MS, () => poll($))
    if (!M.view) M.view = $.clock.every(VIEW_MS, () => view($))
    await flush($)
    return
  }
  if (!r || !r.retry) {
    M.off = true
    if (M.hello) M.hello.cancel()
    M.hello = null
    return
  }
  if (M.tries === HELLO_FAST_TRIES && M.hello) {
    M.hello.cancel()
    M.hello = $.clock.every(HELLO_SLOW_MS, () => sayHello($))
  }
}

// register makes the agent type phases run as (baton:phase), from the host's spec, until it holds.
async function registerType($, spec) {
  if (M.registered || !spec) return
  try {
    await $.agent.register({ name: spec.name, description: spec.description, prompt: spec.prompt, isolation: 'worktree' })
    M.registered = true
  } catch (err) {
    M.spec = spec // the session has not bound yet: the next poll tries again
  }
}

// registerBoard registers the board's own command, which opens the pane while Claude works, with the
// host's words, until it holds.
async function registerBoard($, spec) {
  if (spec) M.boardSpec = spec
  if (M.boardOn || !M.boardSpec) return
  try {
    await $.command.register({ name: BOARD, description: M.boardSpec.description, immediate: true })
    M.boardOn = true
  } catch (err) {
    // the next poll tries again
  }
}

// keep keeps a report, numbered, until the host has recorded it (flush sends it).
async function keep($, rep) {
  M.keep.seq++
  M.keep.outbox.push({ ...rep, seq: M.keep.seq })
  await save($)
}

// report keeps a report and sends what is kept.
async function report($, rep) {
  await keep($, rep)
  await flush($)
}

// flush sends the kept reports in order, one at a time, and stops at the first the host did not record.
// One flush runs at a time; a report kept meanwhile goes with it.
function flush($) {
  if (!M.ready) return Promise.resolve()
  if (!M.flushing) {
    M.flushing = sendKept($).finally(() => {
      M.flushing = null
    })
  }
  return M.flushing
}

// sendKept sends the kept reports. A stale reply (the host knows another mod) says hello again first; a
// no ends the mod's work.
async function sendKept($) {
  try {
    while (M.ready && M.keep.outbox.length) {
      const r = await call($, M.keep.outbox[0])
      if (!r) return
      if (r.ok) {
        M.keep.outbox.shift()
        await save($)
        continue
      }
      if (r.stale) {
        helloAgain($)
      } else if (!r.retry) {
        M.ready = false
        M.off = true
      }
      return
    }
  } catch (err) {
    // the store could not be written: the next poll sends what is kept
  }
}

// helloAgain has the mod say hello again before anything else, as it did first.
function helloAgain($) {
  M.ready = false
  M.tries = 0
  if (M.hello) M.hello.cancel()
  M.hello = $.clock.every(HELLO_FAST_MS, () => sayHello($))
}

// agentsNow reads baton's agents as the host lists them.
async function agentsNow($) {
  let text
  try {
    text = await $.fs.read(M.dir + '/agents.json')
  } catch (err) {
    text = ''
  }
  if (text === M.agentsText || text === '') return
  M.agentsText = text
  const agents = {}
  try {
    for (const a of JSON.parse(text).agents || []) if (a && a.id) agents[a.id] = { phase: a.phase, drop: a.drop }
  } catch (err) {
    // none until the host writes the list whole
  }
  M.agents = agents
}

// commands lists the command files in the bridge's directory, in order of their ids.
async function commands($) {
  let entries
  try {
    entries = await $.fs.list(M.dir + '/out')
  } catch (err) {
    return []
  }
  const ids = []
  for (const e of entries) {
    const m = /^([0-9]+)\.json$/.exec(e.name)
    if (m && e.kind === 'file') ids.push(Number(m[1]))
  }
  return ids.sort((a, b) => a - b)
}

// poll runs the host's commands after the last one the mod ran, in order and one at a time. A spawn and a
// message are made from here, outside any hook: the mod then sees of an agent only its turns and its
// reports to the lead, which is all baton needs of it.
async function poll($) {
  if (!M.ready || M.polling) return
  M.polling = true
  try {
    if (M.spec && !M.registered) await registerType($, M.spec)
    if (M.boardSpec && !M.boardOn) await registerBoard($)
    await agentsNow($)
    await flush($)
    const e = await env($)
    for (const id of await commands($)) {
      if (!M.ready || id <= M.keep.last) continue
      let c
      try {
        c = JSON.parse(await $.fs.read(M.dir + '/out/' + String(id).padStart(6, '0') + '.json'))
      } catch (err) {
        continue // acked meanwhile, or not written whole: the next poll reads it again
      }
      if (!c || c.instance !== e.instance || c.id !== id) continue
      if (c.kind === 'moved') {
        // The host drives another run now: the hello gives the mod that run's bridge. Nothing is acked
        // here, as this bridge is no longer the mod's.
        M.keep.last = id
        await save($)
        helloAgain($)
        return
      }
      M.keep.running = id
      await save($)
      const { later, held, ...result } = await run($, c)
      M.keep.last = id
      M.keep.running = 0
      await save($)
      if (held) continue // acked once it runs at the lead's turn's end, or with its refusal past HOLD_MS
      if (c.kind === 'spawn' && result.agent) M.agents[result.agent] = { phase: c.phase, drop: c.drop } // until the host lists it
      await report($, { kind: 'ack', command: id, ...result })
      if (c.kind === 'ask') queueAsk($, c)
      if (later) afterwards($, later)
    }
  } finally {
    M.polling = false
  }
}

// run runs one command, and resolves what it answered: ok and its result, or the error.
async function run($, c) {
  try {
    switch (c.kind) {
      case 'spawn': {
        const r = await $.agent.spawn({ subagentType: 'baton:phase', prompt: c.prompt, description: c.description, name: c.name })
        return r && r.agentId ? { ok: true, agent: r.agentId } : { ok: false, error: String((r && r.deny) || 'no agent started') }
      }
      case 'send': {
        const r = await $.tool.call({ tool: 'SendMessage', to: c.agent, message: c.text, summary: c.summary || 'baton' })
        return answer(r)
      }
      case 'stop': {
        const r = await $.tool.call({ tool: 'TaskStop', task_id: c.agent })
        return answer(r)
      }
      case 'ask':
        return { ok: true, result: 'queued' } // the band asks it once the ack is kept
      case 'compact':
        return await compact($, c)
      case 'submit':
        return await started($, $.prompt.submit({ text: c.text, asUser: true }), () => null, (err) => ({ kind: 'failed', command: c.id, error: cut(String(err), CUT) }), 'submitted')
      default:
        return { ok: false, error: 'no such command: ' + c.kind }
    }
  } catch (err) {
    return { ok: false, error: cut(String(err), CUT) }
  }
}

// started acks a call the poll does not wait for: a compaction runs for seconds or minutes, and a line
// submitted while a turn runs waits for the turn's end, and the poll would run nothing else meanwhile. A
// refusal at once (Claude Code refuses a compaction while a turn runs) is the ack's error; otherwise the
// ack says it started (as), and what the call comes to is reported after it (later: the report done or
// failed makes, or null for none).
async function started($, call, done, failed, as) {
  const outcome = Promise.resolve(call).then(
    (r) => ({ r }),
    (err) => ({ err }),
  )
  const early = await Promise.race([outcome, $.clock.sleep(AT_ONCE_MS).then(() => null)])
  if (early && 'err' in early) return { ok: false, error: cut(String(early.err), CUT) }
  return { ok: true, result: as, later: outcome.then((o) => ('err' in o ? failed(o.err) : done(o.r))) }
}

// vetoed is rep, saying whether a hook vetoed the compaction r is the outcome of ({ skip: why }), and why.
function vetoed(rep, r) {
  if (!(r && r.skip)) return { ...rep, skipped: false }
  return { ...rep, skipped: true, result: cut(String(r.skip), CUT) }
}

// compact runs baton's compact command c from the poll. One Claude Code refuses while the lead's turn runs
// is held for that turn's end (hold), and resolves { held: true }: it is acked then. If the lead's turn
// ended while the call was refused, there is nothing to wait for: it is called again at once instead.
async function compact($, c) {
  const turn = M.turn
  const r = await compactWith($, c, $.session.compact({}))
  if (r.ok) return r
  if (M.turn) {
    hold($, c, r.error)
    return { held: true }
  }
  return turn ? await compactWith($, c, $.session.compact({})) : r
}

// compactWith is what compaction, one call of $.session.compact for c, comes to (started): its ack, and
// what is reported once it is over.
function compactWith($, c, compaction) {
  return started($, compaction, (r) => vetoed({ kind: 'compacted', command: c.id, epoch: c.epoch }, r), (err) => ({ kind: 'compacted', command: c.id, epoch: c.epoch, error: cut(String(err), CUT) }), 'compacting')
}

// hold keeps compaction c, refused for error, for the lead's turn's end (unhold), and in the store, until
// HOLD_MS has passed (release). It is set at once, with nothing awaited, so that a turn.complete coming
// any time after the refusal finds it.
function hold($, c, error) {
  if (M.held) release($)
  const h = { c, error, timer: null }
  h.timer = $.clock.after(HOLD_MS, () => {
    if (M.held !== h) return
    h.timer = null // fired: nothing to cancel
    release($)
  })
  M.held = h
  M.keep.held = { id: c.id, error }
}

// unset cancels the bound of hold h, if it has not passed.
function unset(h) {
  try {
    if (h.timer) h.timer.cancel()
  } catch (err) {
    // it fired meanwhile, and found the hold gone
  }
  h.timer = null
}

// release acks the compaction held, past HOLD_MS, with the refusal it had: the host asks again after the
// turn, as it did before any hold.
function release($) {
  const h = M.held
  if (!h) return
  M.held = null
  unset(h)
  M.keep.held = null
  report($, { kind: 'ack', command: h.c.id, ok: false, error: h.error }).catch(() => {})
}

// forsake drops a compaction held for another run's bridge, unacked: the host that asked for it drives
// another run now, and takes it back (bridge.Void).
function forsake() {
  if (M.held) unset(M.held)
  M.held = null
  M.keep.held = null
}

// unhold runs the compaction held, from the lead's turn.complete hook once the turn has ended: Claude
// Code takes a compaction called there ("call it from turn.complete or later"). The call is made in the
// hook, so its own session.compact hook sees it (own); the hook does not wait on it. Whatever comes of it
// is acked as the poll would have, a refusal too: the host asks again after that.
async function unhold($) {
  const h = M.held
  M.held = null
  unset(h)
  M.keep.held = { id: h.c.id, called: true } // reloaded from here on, it may or may not have run
  try {
    await save($)
  } catch (err) {
    // kept in memory: the next save writes it
  }
  M.own = true
  let compaction
  try {
    compaction = $.session.compact({})
  } catch (err) {
    compaction = Promise.reject(err)
  }
  const own = () => {
    M.own = false
  }
  Promise.resolve(compaction).then(own, own)
  compactWith($, h.c, compaction)
    .then(async ({ later, ...result }) => {
      M.keep.held = null
      await report($, { kind: 'ack', command: h.c.id, ...result })
      if (later) afterwards($, later)
    })
    .catch(() => {})
}

// afterwards reports what a call the poll did not wait for came to, once its ack is kept.
function afterwards($, later) {
  later
    .then(async (rep) => {
      if (rep) await report($, rep)
    })
    .catch(() => {})
}

// answer is what a tool call answered: whether it succeeded, and its message. A tool's error resolves
// the call too (isError), and a refusal (SendMessage to an agent the human stopped) says success: false.
function answer(r) {
  if (!r || r.deny) return { ok: false, error: cut(String((r && r.deny) || 'no answer'), CUT) }
  const res = r.result
  if (r.isError) return { ok: false, error: cut(String(r.text || (typeof res === 'string' ? res : JSON.stringify(res))), CUT) }
  if (res && typeof res === 'object') {
    return { ok: res.success !== false, result: cut(String(res.message || JSON.stringify(res)), CUT) }
  }
  return { ok: true, result: cut(String(r.text || ''), CUT) }
}

// queueAsk keeps the host's question c for the band to ask: a question asked already, or waiting, is
// the same question asked again (the host asks again what it fears the mod lost), and takes its place.
function queueAsk($, c) {
  if (c.question === M.asking) return
  M.asks = M.asks.filter((a) => a.question !== c.question)
  M.asks.push(c)
  $.ui.invalidate('ui.render')
}

// ask asks the human the front question in Claude Code's own dialog, from inside the band's render hook:
// it calls $.ui.ask before it awaits anything, while that hook still runs. The answer, or that the dialog
// closed without one, is reported once it comes, and the band asks the next.
function ask($) {
  const c = M.asks.shift()
  M.asking = c.question
  let asked
  try {
    asked = $.ui.ask(c.question, { options: c.options || [], header: c.header || 'baton' })
  } catch (err) {
    asked = Promise.reject(err)
  }
  asked
    .then(
      (a) => ({ kind: 'answered', command: c.id, question: c.question, answer: String(a), deadline: !!M.settled[c.question] }),
      (err) => ({ kind: 'answered', command: c.id, question: c.question, dismissed: true, error: cut(String(err), CUT) }),
    )
    .then(async (rep) => {
      delete M.settled[c.question]
      M.asking = null
      $.ui.invalidate('ui.render')
      await report($, rep)
    })
    .catch(() => {})
}

// bounded is the tool.call hook of AskUserQuestion in the lead's session: it asks the host about every
// question put to the human there, with the call's questions and their options. One the host answers
// waits for the human as long as it says, then the mod answers it with the host's answer (one of baton's)
// or answers (one per question, a call of the lead model's own), which closes its dialog, and reports that
// it did (Claude Code fires no PostToolUse then). The host asks nothing of the questions it never answers,
// and the human's answer in time stands.
async function bounded($, e, next) {
  const qs = e.questions
  if (!live() || e.agentId || !Array.isArray(qs) || !qs.length || !qs.every((x) => x && typeof x.question === 'string')) return next(e)
  const q = qs[0].question
  const asked = qs.map((x) => ({ question: x.question, options: (Array.isArray(x.options) ? x.options : []).map((o) => String((o && o.label) ?? '')), multi: !!x.multiSelect }))
  const t0 = await $.clock.now()
  let done = null
  const beneath = next(e).then(
    (v) => (done = { v }),
    (err) => (done = { err }),
  )
  for (;;) {
    const r = await call($, { kind: 'asking', question: q, questions: asked, elapsed: (await $.clock.now()) - t0 })
    if (done) break
    const answers = r && r.ok && r.answers && typeof r.answers === 'object' && !Array.isArray(r.answers) ? r.answers : null
    if (r && r.ok && answers && !r.wait_ms) {
      await keep($, { kind: 'deadline', question: q, answer: String(answers[q] ?? ''), questions: asked, answers })
      flush($)
      return { result: { questions: qs, answers } } // AskUserQuestion's own output
    }
    if (r && r.ok && r.answer && !r.wait_ms) {
      M.settled[q] = true
      await keep($, { kind: 'deadline', question: q, answer: r.answer })
      flush($)
      return { result: { questions: qs, answers: { [q]: r.answer } } } // AskUserQuestion's own output
    }
    if (r && r.ok && !r.wait_ms) break // baton never answers it: the human does
    const ms = r && r.wait_ms > 0 ? r.wait_ms : ASK_RETRY_MS
    const slept = $.clock.sleep(ms, { signal: next.signal }).then(
      () => true,
      () => false,
    )
    if (!(await Promise.race([beneath.then(() => false), slept])) || done) break
  }
  await beneath
  return 'v' in done ? done.v : next(e) // next replays what it rejected with
}

// board runs `baton board` with args, and resolves what it printed, without its last newline, or null
// when it failed.
async function board($, args) {
  const e = await env($)
  try {
    const session = await $.session.id()
    const out = await $.process.run([e.bin, 'board', ...args], { env: { CLAUDE_CODE_SESSION_ID: session }, timeoutMs: VIEW_TIMEOUT_MS })
    return out.exitCode === 0 ? out.stdout.replace(/\r?\n$/, '') : null
  } catch (err) {
    return null
  }
}

// view reads the band again, and the board while its pane is open, and redraws what changed.
async function view($) {
  if (!live() || M.viewing) return
  M.viewing = true
  try {
    let changed = false
    const band = await board($, ['--band', '--width', String(M.width || 80)])
    if (band !== null && band !== M.band) {
      M.band = band
      changed = true
    }
    if (await paneUp($)) {
      const text = await board($, ['--width', String(M.paneWidth || 80)])
      if (text !== null && text !== M.board) {
        M.board = text
        changed = true
      }
    }
    if (changed) $.ui.invalidate('ui.render')
  } finally {
    M.viewing = false
  }
}

// paneUp says whether the board's pane is open.
async function paneUp($) {
  try {
    return (await $.ui.panes()).some((p) => p.id === PANE)
  } catch (err) {
    return false
  }
}

function cut(s, n) {
  s = String(s ?? '')
  return s.length > n ? s.slice(0, n) + '…' : s
}

// live says whether the mod works for baton: the host has answered its hello once, and has not said no.
// Its hooks keep reports from then on, also while it says hello again; they are sent once it holds.
function live() {
  return !M.off && M.dir !== ''
}

// ours is the id of the baton agent a notification is about, or '' if it is about none.
function ours(text) {
  for (const id of Object.keys(M.agents)) if (text.includes(id)) return id
  return ''
}

// HANDBACK is the frame Claude Code puts an agent's report in when it hands it back to the lead as a peer
// message: the agent's name, or its id where it has none, then the hand-back's line.
const HANDBACK = /^<agent-message from="([^"]*)">\r?\n\[Subagent hand-back\]/

// handedBack is the id of the baton agent whose report a peer message hands back, or '' if it is none of
// theirs. baton names each of its agents by its phase.
function handedBack(text) {
  const m = HANDBACK.exec(text.trimStart())
  if (!m) return ''
  for (const [id, a] of Object.entries(M.agents)) if (m[1] === id || m[1] === a.phase) return id
  return ''
}

// letThrough is every hook's .catch: whatever went wrong, the event goes on as if the mod were not there
// (next is replay-safe there: what was asked of it already is not asked again).
function letThrough($, e, next) {
  return next(e)
}

async function start($, e, next) {
  const started = await next(e)
  if (!(await hosted($)) || M.ready || M.hello) return started
  try {
    const pj = JSON.parse(await $.fs.read($.plugin.root + '/.claude-plugin/plugin.json'))
    M.version = String(pj.version || '')
  } catch (err) {
    M.version = ''
  }
  await load($)
  await forget($)
  M.tries = 0
  M.off = false
  M.hello = $.clock.every(HELLO_FAST_MS, () => sayHello($))
  return started
}

// A turn of the lead's began: the mod notes it, for a compaction Claude Code refuses meanwhile (compact).
async function begun($, e, next) {
  if (!e.agentId) M.turn = String(e.turnId || '?')
  return next(e)
}

// A turn of one of baton's agents ended: the host hears how (an aborted or failed turn has no SubagentStop).
// A turn of the lead's ended: the compaction held for it runs now (unhold).
async function turned($, e, next) {
  if (!e.agentId) M.turn = ''
  const r = await next(e)
  if (!e.agentId && M.held) await unhold($)
  if (live() && e.agentId && M.agents[e.agentId]) {
    const rep = { kind: 'turn', agent: e.agentId, reason: e.reason, ms: e.durationMs, text: cut(e.answer, CUT) }
    if (e.reason === 'refusal' && e.refusal) rep.refusal = e.refusal.category || ''
    await keep($, rep)
    flush($)
  }
  return r
}

// A report of one of baton's agents never reaches the lead's model: baton has it. It comes as a task
// notification, where only its id tells it from the brief baton's rewake gives the lead, and in auto mode
// also handed back as a peer message, framed with the agent's name.
async function prompted($, e, next) {
  if (live() && e.origin) {
    const text = e.text || ''
    const id = e.origin.kind === 'task-notification' ? ours(text) : e.origin.kind === 'peer' ? handedBack(text) : ''
    if (id) {
      const a = M.agents[id]
      await keep($, { kind: 'dropped', agent: id })
      flush($)
      return { drop: a.drop || a.phase || id }
    }
  }
  return next(e)
}

// The lead's conversation compacting, before and after: what proves to baton that the compaction is the
// lead's. A phase agent's is proven by its own transcript, which the mod never reports for. (The mod's own
// call, for baton's compact command, is not reported here either: the command's reports say how it went.
// Called from the poll, it does not come here, as Claude Code runs it through every hook but the caller's;
// called from the turn.complete hook, it does, while own says so.)
async function compacting($, e, next) {
  if (!live() || e.agentId || M.own) return next(e)
  await report($, { kind: 'compacting', trigger: e.trigger })
  const r = await next(e)
  await report($, { kind: 'compacted', trigger: e.trigger, skipped: !!(r && r.skip) })
  return r
}

// The band above the prompt: the run in one line, as the host words it, fitted to the columns it is
// drawn in (the next read fits a new width). It is also where baton's questions are asked, one at a
// time, before anything here awaits.
async function band($, e, next) {
  if (M.asks.length && !M.asking && live()) ask($)
  if (!live() || !M.band || e.props.hasSurvey) return next(e)
  if (e.props.bodyColumns > 0) M.width = e.props.bodyColumns
  const { Text } = $.ui.resolve(e)
  return Text({ children: [M.band], wrap: 'truncate-end' })
}

// The board's pane: the plan step by step, as the host words it.
async function pane($, e, next) {
  if (!live()) return next(e)
  if (e.props.bodyColumns > 0) M.paneWidth = e.props.bodyColumns
  const { Box, Text } = $.ui.resolve(e)
  const lines = M.board === '' ? [] : M.board.split('\n')
  return Box({ flexDirection: 'column', children: lines.map((l) => Text({ children: [l], wrap: 'truncate-end' })) })
}

// /baton board, and the board's own command: the pane opens with the board in it, and nothing reaches
// the model. Where the pane cannot be placed yet, the board is the command's output. Anything else typed
// to /baton is the skill's.
async function boardCommand($, e, next) {
  if (!live() || (e.command === SKILL && String(e.args || '').trim().toLowerCase() !== 'board')) return next(e)
  if (!M.paneWidth && e.presentation) M.paneWidth = e.presentation.columns
  const text = await board($, ['--width', String(M.paneWidth || 80)])
  if (text === null) return next(e)
  M.board = text
  const title = (M.boardSpec && M.boardSpec.title) || 'baton'
  const opened = await $.ui.open({ id: PANE, title })
  return opened && opened.isPlaced ? {} : { text }
}

export function register(on) {
  on('session.start', start).catch(letThrough)
  on('turn.start', begun).catch(letThrough)
  on('turn.complete', turned).catch(letThrough)
  on('prompt.submit', prompted).catch(letThrough)
  on('session.compact', compacting).catch(letThrough)
  on('ui.render', { component: 'AbovePrompt' }, band).catch(letThrough)
  on('tool.call', { tool: 'AskUserQuestion' }, bounded).catch(letThrough)
  on('ui.render', { component: 'Pane', requestId: PANE }, pane).catch(letThrough)
  on('command.run', { command: SKILL }, boardCommand).catch(letThrough)
  on('command.run', { command: BOARD }, boardCommand).catch(letThrough)
  // The phase agent type stays offered: Claude Code resumes no agent by a message whose type it does not
  // offer. baton's own hook refuses the models' calls to it, so only baton starts phases.
}
