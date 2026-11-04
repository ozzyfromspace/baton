// Spike 43: the model and effort of a session and of its agents (Claude Code 2.1.318, Haiku 5.5 and Sonnet 5.5).
// Spike 40's mod, cut to what this spike asks: agent types registered as baton's mod registers `phase`
// ($.agent.register), each with its own effort and model, agents spawned as baton's mod spawns them ($.agent.spawn,
// a name, a model or none), a type registered again with another effort, and messages to completed agents
// ($.tool.call SendMessage), so the transcripts say which effort and model each request carried. Every event it sees
// and every call it makes is logged as JSON lines to $SPK_LOG.<instance>.<part>.

const PART = 400
const L = { log: [], written: 0, path: null, inst: null, dirty: false, flushing: false }
let agents = {} // name -> agentId
let scen = 'x'

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

function cut(s, n = 300) {
  s = String(s ?? '')
  return s.length > n ? s.slice(0, n) + '…' : s
}

// The types: name -> the definition's effort and model (undefined leaves it out).
const TYPES = {
  't-max': { effort: 'max' },
  't-low': { effort: 'low' },
  't-none': {},
  't-sonnet': { effort: 'medium', model: 'sonnet' },
}

const TASK = 'You are working on phase NAME of a plan. Run this exact Bash command: pwd. Then reply with only the word DONE.'

async function save($) {
  await $.store.set(scen + ':agents', agents)
}

async function caught($, e, next) {
  const err = next.error || {}
  await rec($, 'caught', { kind: err.kind, cause: err.cause, message: cut(err.message, 200), ev: e && e.hook_event_name, origin: next.origin })
  return next(e)
}

async function registerType($, name, def, why) {
  const spec = {
    name,
    description: 'A worker on one phase of a plan (spike type ' + name + ')',
    prompt: 'You work on one phase of a plan. Do what the task says, in the current directory, and reply as it asks.',
    ...def,
  }
  try {
    const r = await $.agent.register(spec)
    await rec($, 'registered', { name, def, why, r })
  } catch (err) {
    await rec($, 'register-failed', { name, def, why, err: cut(String(err), 600) })
  }
}

async function spawn($, name, type, model) {
  const input = { subagentType: 'spk:' + type, prompt: TASK.replaceAll('NAME', name), description: name + ' — spike phase', name }
  if (model) input.model = model
  try {
    const r = await $.agent.spawn(input)
    if (r.agentId) {
      agents[name] = r.agentId
      await save($)
    }
    await rec($, 'spawned', { name, type, model: model || null, r })
  } catch (err) {
    await rec($, 'spawn-failed', { name, type, model: model || null, err: cut(String(err), 600) })
  }
}

async function msg($, to, text) {
  const target = agents[to] || to
  try {
    const r = await $.tool.call({ tool: 'SendMessage', to: target, message: text, summary: 'spk' })
    await rec($, 'sent', { to, target, r: cut(JSON.stringify(r), 800) })
  } catch (err) {
    await rec($, 'send-failed', { to, target, err: cut(String(err), 800) })
  }
}

export function register(on) {
  on('session.start', async ($, e, next) => {
    scen = (await $.env.get('SPK_SCEN')) || 'x'
    agents = (await $.store.get(scen + ':agents')) ?? {}
    await rec($, 'session.start', { e: cut(JSON.stringify(e)), agents })
    for (const name of ['spk-go', 'spk-re', 'spk-msg', 'spk-model']) {
      try {
        await $.command.register({ name, description: 'spike: ' + name, immediate: true })
      } catch (err) {
        await rec($, 'command-register-failed', { name, err: String(err) })
      }
    }
    for (const [name, def] of Object.entries(TYPES)) await registerType($, name, def, 'start')
    try {
      await rec($, 'session.model', { model: await $.session.model() })
    } catch (err) {
      await rec($, 'session.model-failed', { err: cut(String(err), 300) })
    }
    return next(e)
  })

  // /spk-go A1:t-max A2:t-low:sonnet — spawned inside this hook, as spike 40's were
  on('command.run', { command: 'spk-go' }, async ($, e) => {
    const specs = (e.args || '').trim().split(/\s+/).filter(Boolean)
    for (const s of specs) {
      const [name, type, model] = s.split(':')
      await spawn($, name, type, model)
    }
    return { text: 'spk: spawned ' + specs.join(' ') }
  })

  // /spk-re t-max low [model] — the type registered again with that effort (and that model, or its own)
  on('command.run', { command: 'spk-re' }, async ($, e) => {
    const [name, effort, model] = (e.args || '').trim().split(/\s+/)
    const def = { ...(TYPES[name] || {}), effort }
    if (model) def.model = model
    await registerType($, name, def, 'again')
    return { text: 'spk: registered ' + name + ' again at ' + effort + (model ? ' on ' + model : '') }
  })

  // /spk-msg A1 text — to the agent the mod spawned as A1, from a timer, as baton's bridge sends
  on('command.run', { command: 'spk-msg' }, async ($, e) => {
    const [to, ...rest] = (e.args || '').trim().split(' ')
    $.clock.after(10, () => msg($, to, rest.join(' ')))
    return { text: 'spk: msg ' + to }
  })

  on('command.run', { command: 'spk-model' }, async ($, e) => {
    let m
    try {
      m = await $.session.model()
    } catch (err) {
      m = 'error: ' + String(err)
    }
    await rec($, 'session.model', { model: m })
    return { text: 'spk: model ' + JSON.stringify(m) }
  })

  on('tool.call', async ($, e, next) => {
    await rec($, 'call-start', { agentId: e.agentId, tool: e.tool, id: e.tool_use_id })
    return next(e)
  }).catch(caught)

  on('turn.complete', async ($, e, next) => {
    await rec($, 'turn.complete', { agentId: e.agentId, reason: e.reason, isAborted: e.isAborted, ms: e.durationMs, answer: cut(e.answer, 120) })
    return next(e)
  }).catch(caught)

  // An agent's report is dropped, as baton's mod drops it, so the lead has no turn but the ones typed.
  on('prompt.submit', async ($, e, next) => {
    const o = e.origin || {}
    if (o.kind === 'task-notification') {
      await rec($, 'dropped', { text: cut(e.text, 120) })
      return { drop: 'spk: an agent reported to the mod, not to Claude' }
    }
    return next(e)
  }).catch(caught)
}
