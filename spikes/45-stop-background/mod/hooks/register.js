// Spike 45: stopping a background shell as baton's mod would (Claude Code 2.1.318).
// Spike 43's mod, cut to what this spike asks: a command, /spk-stop <task id>, that calls TaskStop with that id
// from a timer, as baton's mod runs its `stop` command from its poll ($.tool.call({ tool: 'TaskStop', task_id })),
// and a log of every prompt Claude Code submits on its own (task notifications), none dropped, so the spike sees
// what the model is told when a task ends or is stopped. Every event it sees and every call it makes is logged as
// JSON lines to $SPK_LOG.<instance>.<part>.

const PART = 400
const L = { log: [], written: 0, path: null, inst: null, dirty: false, flushing: false }

async function rec($, kind, data) {
  try {
    const t = await $.clock.now()
    if (L.inst === null) L.inst = String(t)
    if (L.path === null) L.path = (await $.env.get('SPK_LOG')) || ''
    L.log.push({ t, inst: L.inst, kind, ...JSON.parse(JSON.stringify(data ?? {})) })
    L.dirty = true
    if (L.flushing || !L.path) return
    L.flushing = true
    try {
      while (L.dirty) {
        L.dirty = false
        const n = L.log.length
        for (let p = Math.floor(L.written / PART); p <= Math.floor((n - 1) / PART); p++) {
          const chunk = L.log.slice(p * PART, Math.min(n, (p + 1) * PART))
          await $.fs.write(`${L.path}.${L.inst}.${String(p).padStart(3, '0')}`, chunk.map((x) => JSON.stringify(x)).join('\n') + '\n')
        }
        L.written = n
      }
    } finally {
      L.flushing = false
    }
  } catch (err) {
    // the log is the spike's only output; nothing to do but go on
  }
}

function cut(s, n = 600) {
  s = String(s ?? '')
  return s.length > n ? s.slice(0, n) + '…' : s
}

async function caught($, e, next) {
  const err = next.error || {}
  await rec($, 'caught', { kind: err.kind, cause: err.cause, message: cut(err.message, 200), ev: e && e.hook_event_name, origin: next.origin })
  return next(e)
}

async function stop($, id) {
  try {
    const r = await $.tool.call({ tool: 'TaskStop', task_id: id })
    await rec($, 'stopped', { id, r: cut(JSON.stringify(r), 1500) })
  } catch (err) {
    await rec($, 'stop-failed', { id, err: cut(String(err), 1500) })
  }
}

export function register(on) {
  on('session.start', async ($, e, next) => {
    await rec($, 'session.start', { e: cut(JSON.stringify(e)) })
    try {
      await $.command.register({ name: 'spk-stop', description: 'spike: TaskStop a task by its id', immediate: true })
    } catch (err) {
      await rec($, 'command-register-failed', { err: String(err) })
    }
    return next(e)
  })

  // /spk-stop <task id> — TaskStop from a timer, as baton's mod runs its commands from its poll
  on('command.run', { command: 'spk-stop' }, async ($, e) => {
    const id = (e.args || '').trim()
    await rec($, 'stop-asked', { id })
    $.clock.after(10, () => stop($, id))
    return { text: 'spk: stopping ' + id }
  })

  on('tool.call', async ($, e, next) => {
    await rec($, 'call', { agentId: e.agentId, tool: e.tool, id: e.tool_use_id })
    return next(e)
  }).catch(caught)

  on('turn.complete', async ($, e, next) => {
    await rec($, 'turn.complete', { agentId: e.agentId, reason: e.reason, ms: e.durationMs, answer: cut(e.answer, 200) })
    return next(e)
  }).catch(caught)

  // Every prompt, the task notifications Claude Code submits on its own among them, logged and let through.
  on('prompt.submit', async ($, e, next) => {
    await rec($, 'prompt', { origin: e.origin || null, text: cut(e.text, 1500) })
    return next(e)
  }).catch(caught)
}
