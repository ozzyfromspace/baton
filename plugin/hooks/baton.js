// baton's mod: what only something inside the lead's claude can do, on baton's say-so. It starts and
// messages phase agents, asks the human, keeps phase agents' reports from the lead's model, and reports
// what it sees. It decides nothing and words nothing: the baton host does both (internal/bridge). It does
// nothing unless baton hosts this session and answers its hello, and every hook lets its event through
// when anything goes wrong.
//
// The host writes numbered commands to <dir>/out/<id>.json, each for the host instance it runs as; the
// mod runs those of its own claude's host, in order, one at a time, from its poll timer. It reports by
// running `baton mod report` with one numbered report on stdin, and keeps each report until the host has
// recorded it. In the store it keeps, per host instance, the last command it ran, the one it is running,
// and the reports not recorded yet, so that once reloaded it neither repeats nor skips one.

const PROTOCOL = 1 // what the mod and the host say to each other (bridge.Protocol)
const POLL_MS = 500
const HELLO_FAST_MS = 1000 // how often a hello is tried at first,
const HELLO_FAST_TRIES = 30 // this many times,
const HELLO_SLOW_MS = 10000 // and then
const REPORT_TIMEOUT_MS = 5000
const KEEP_MS = 7 * 24 * 3600 * 1000 // the store forgets a host instance unused this long
const CUT = 1000 // the most of an agent's last words a turn report carries

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
    if (M.keep.seq < (r.seen || 0)) {
      M.keep.seq = r.seen
      await save($)
    }
    await registerType($, r.agent)
    if (!M.poll) M.poll = $.clock.every(POLL_MS, () => poll($))
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
        M.ready = false
        M.tries = 0
        M.hello = $.clock.every(HELLO_FAST_MS, () => sayHello($))
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
      M.keep.running = id
      await save($)
      const result = await run($, c)
      M.keep.last = id
      M.keep.running = 0
      await save($)
      if (c.kind === 'spawn' && result.agent) M.agents[result.agent] = { phase: c.phase, drop: c.drop } // until the host lists it
      await report($, { kind: 'ack', command: id, ...result })
      if (c.kind === 'ask') ask($, c).catch(() => {}) // not awaited: the human may take a while
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
        return { ok: true, result: 'asked' } // poll opens the dialog once the ack is kept
      default:
        return { ok: false, error: 'no such command: ' + c.kind }
    }
  } catch (err) {
    return { ok: false, error: cut(String(err), CUT) }
  }
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

// ask asks the human the host's question in Claude Code's own dialog, and reports the answer, or that the
// dialog closed without one.
async function ask($, c) {
  let rep
  try {
    const a = await $.ui.ask(c.question, { options: c.options || [], header: c.header || 'baton' })
    rep = { kind: 'answered', command: c.id, answer: String(a) }
  } catch (err) {
    rep = { kind: 'answered', command: c.id, dismissed: true, error: cut(String(err), CUT) }
  }
  await report($, rep)
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

// A turn of one of baton's agents ended: the host hears how (an aborted or failed turn has no SubagentStop).
async function turned($, e, next) {
  const r = await next(e)
  if (live() && e.agentId && M.agents[e.agentId]) {
    const rep = { kind: 'turn', agent: e.agentId, reason: e.reason, ms: e.durationMs, text: cut(e.answer, CUT) }
    if (e.reason === 'refusal' && e.refusal) rep.refusal = e.refusal.category || ''
    await keep($, rep)
    flush($)
  }
  return r
}

// A report of one of baton's agents never reaches the lead's model: baton has it. Only its id tells it
// from the brief baton's rewake gives the lead, which comes as a task notification too.
async function prompted($, e, next) {
  if (live() && e.origin && e.origin.kind === 'task-notification') {
    const id = ours(e.text || '')
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
// lead's. A phase agent's is proven by its own transcript, which the mod never reports for.
async function compacting($, e, next) {
  if (!live() || e.agentId) return next(e)
  await report($, { kind: 'compacting', trigger: e.trigger })
  const r = await next(e)
  await report($, { kind: 'compacted', trigger: e.trigger, skipped: !!(r && r.skip) })
  return r
}

export function register(on) {
  on('session.start', start).catch(letThrough)
  on('turn.complete', turned).catch(letThrough)
  on('prompt.submit', prompted).catch(letThrough)
  on('session.compact', compacting).catch(letThrough)
  // Only baton starts phases: the model is never offered the type.
  on('agent.offer', { agent: 'baton:phase' }, () => ({ isOffered: false })).catch(hidden)
}

// hidden is the type's offer when its hook fails: still not offered.
function hidden($, e, next) {
  return next.called ? next(e) : { isOffered: false }
}
