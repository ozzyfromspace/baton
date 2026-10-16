// Spike 40: writing to a phase agent from the lead's prompt (Claude Code 2.1.301, Haiku). Spike 36's mod, cut to
// what this spike asks: agents spawned as baton's mod spawns them (a registered type in a worktree of its own, with a
// name), messages and stops as baton's mod sends them ($.tool.call SendMessage, TaskStop), a message to a name as
// given (not an id), the agent list, and every prompt, turn and settings event it sees. Every event it sees and every
// call it makes is logged as JSON lines to $SPK_LOG.<instance>.<part>.

const PART = 400
const L = { log: [], written: 0, path: null, inst: null, dirty: false, flushing: false }
let agents = {} // name -> agentId
let scen = 'x'
let last = ''

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

const T = {
  // three long commands: the agent is at work for about three minutes
  L: 'You are working on phase NAME of a plan. Run this exact Bash command: sh long.sh. When it has finished, run this exact Bash command again: sh long.sh. When that has finished, run it a third time: sh long.sh. Then reply with only the word DONE.',
  // a commit in its worktree, then 25 seconds of work, as a phase agent leaves its worktree when it is done
  C: 'You are working on phase NAME of a plan. Run this exact Bash command: sh commit.sh. When it has finished, run this exact Bash command: sh slow.sh. Then reply with only the word DONE.',
  // quick
  Q: 'You are working on phase NAME of a plan. Run this exact Bash command: pwd. Then reply with only the word DONE.',
}

async function save($) {
  await $.store.set(scen + ':agents', agents)
}

function who(s) {
  return agents[s] || s
}

function nameOf(id) {
  return Object.keys(agents).find((n) => agents[n] === id) || ''
}

async function caught($, e, next) {
  const err = next.error || {}
  await rec($, 'caught', { kind: err.kind, cause: err.cause, message: cut(err.message, 200), ev: e && e.hook_event_name, origin: next.origin })
  return next(e)
}

async function spawn($, name, key, how) {
  try {
    const r = await $.agent.spawn({ subagentType: 'spk:phase', prompt: T[key].replaceAll('NAME', name), description: name + ' — spike phase', name })
    const as = agents[name] ? name + '#2' : name // a second agent of the same name is kept apart
    if (r.agentId) {
      agents[as] = r.agentId
      await save($)
    }
    await rec($, 'spawned', { name: as, key, how, r })
  } catch (err) {
    await rec($, 'spawn-failed', { name, err: String(err) })
  }
}

// msg messages an agent as baton's mod does: the SendMessage tool, called by the mod. raw sends to `to` as given
// (a name the mod may not know), otherwise to the agent id the mod spawned under that name.
async function msg($, to, text, raw) {
  const target = raw ? to : who(to)
  const t0 = await $.clock.now()
  try {
    const r = await $.tool.call({ tool: 'SendMessage', to: target, message: text, summary: 'spk' })
    await rec($, 'sent', { to, target, raw: !!raw, ms: (await $.clock.now()) - t0, r: cut(JSON.stringify(r), 800) })
  } catch (err) {
    await rec($, 'send-failed', { to, target, raw: !!raw, ms: (await $.clock.now()) - t0, err: cut(String(err), 800) })
  }
}

async function act($, what, arg) {
  try {
    if (what === 'stop') {
      const r = await $.tool.call({ tool: 'TaskStop', task_id: who(arg) })
      await rec($, 'stopped', { name: arg, r: cut(JSON.stringify(r), 600) })
    }
  } catch (err) {
    await rec($, 'act-failed', { what, err: cut(String(err), 400) })
  }
}

// box reads the prompt box after a prompt a hook blocked, from a timer, as a host's command would come: what it holds,
// and, for a prompt with [clear] in it, empties it once, only while it still holds that prompt.
function box($, text) {
  for (const ms of [0, 200, 1000, 2500]) {
    $.clock.after(ms, async () => {
      try {
        const b = await $.prompt.read()
        await rec($, 'box', { ms, same: b.text === text, text: cut(b.text, 120), cursor: b.cursor })
        if (ms === 1000 && text.includes('[clear]') && b.text === text) {
          const f = await $.prompt.fill({ text: '', mode: 'replace' })
          await rec($, 'box-cleared', { f: cut(JSON.stringify(f), 300) })
        }
      } catch (err) {
        await rec($, 'box-failed', { ms, err: cut(String(err), 300) })
      }
    })
  }
}

function command(i) {
  const c = i && typeof i === 'object' ? i.command : undefined
  return typeof c === 'string' ? cut(c, 80) : undefined
}

export function register(on) {
  on('session.start', async ($, e, next) => {
    scen = (await $.env.get('SPK_SCEN')) || 'x'
    agents = (await $.store.get(scen + ':agents')) ?? {}
    await rec($, 'session.start', { e: cut(JSON.stringify(e)), agents })
    for (const name of ['spk-go', 'spk-msg', 'spk-raw', 'spk-stop', 'spk-list']) {
      try {
        await $.command.register({ name, description: 'spike: ' + name, immediate: true })
      } catch (err) {
        await rec($, 'register-failed', { name, err: String(err) })
      }
    }
    try {
      const r = await $.agent.register({
        name: 'phase',
        description: 'A worker on one phase of a plan, in a git worktree of its own',
        prompt: 'You work on one phase of a plan, in a git worktree of your own. Do what the task says, in the current directory, and reply as it asks.',
        isolation: 'worktree',
      })
      await rec($, 'agent-registered', { r })
    } catch (err) {
      await rec($, 'agent-register-failed', { err: String(err) })
    }
    $.clock.every(2000, async () => {
      try {
        const list = await $.agent.list()
        const s = list.map((a) => `${a.name || a.id.slice(0, 6)}:${a.status}`).join(' ')
        if (s !== last) {
          last = s
          await rec($, 'list', { list })
        }
      } catch (err) {
        await rec($, 'list-failed', { err: String(err) })
      }
    })
    return next(e)
  })

  // /spk-go P1:L P2:C — spawned inside this hook, so the mod sees the agents' own events (spike 32, S0)
  on('command.run', { command: 'spk-go' }, async ($, e) => {
    const specs = (e.args || '').trim().split(/\s+/).filter(Boolean)
    for (const s of specs) {
      const [name, key] = s.split(':')
      await spawn($, name, key || 'Q', '@hook')
    }
    return { text: 'spk: spawned ' + specs.join(' ') }
  })

  // /spk-msg P1 text — to the agent the mod spawned as P1, from a timer, as baton's bridge sends
  on('command.run', { command: 'spk-msg' }, async ($, e) => {
    const [to, ...rest] = (e.args || '').trim().split(' ')
    $.clock.after(10, () => msg($, to, rest.join(' '), false))
    return { text: 'spk: msg ' + to }
  })

  // /spk-raw P1 text — SendMessage to the name as given, from a timer
  on('command.run', { command: 'spk-raw' }, async ($, e) => {
    const [to, ...rest] = (e.args || '').trim().split(' ')
    $.clock.after(10, () => msg($, to, rest.join(' '), true))
    return { text: 'spk: raw msg ' + to }
  })

  // /spk-stop P1 — TaskStop the agent the mod spawned as P1, from a timer
  on('command.run', { command: 'spk-stop' }, async ($, e) => {
    const name = (e.args || '').trim()
    $.clock.after(10, () => act($, 'stop', name))
    return { text: 'spk: stop ' + name }
  })

  on('command.run', { command: 'spk-list' }, async ($, e) => {
    const list = await $.agent.list()
    await rec($, 'list-cmd', { list })
    return { text: 'spk: ' + JSON.stringify(list.map((a) => [a.name, a.id.slice(0, 8), a.status])) }
  })

  on('tool.call', async ($, e, next) => {
    const t0 = await $.clock.now()
    const base = { agentId: e.agentId, name: nameOf(e.agentId), tool: e.tool, command: command(e.input), id: e.tool_use_id }
    await rec($, 'call-start', base)
    try {
      const r = await next(e)
      await rec($, 'call-end', { ...base, ms: (await $.clock.now()) - t0, isError: r && r.isError, deny: r && r.deny, text: cut(r && r.text, 300) })
      return r
    } catch (err) {
      await rec($, 'call-threw', { ...base, ms: (await $.clock.now()) - t0, err: cut(String(err), 200) })
      throw err
    }
  }).catch(caught)

  on('turn.start', async ($, e, next) => {
    await rec($, 'turn.start', { agentId: e.agentId, name: nameOf(e.agentId), turnId: e.turnId })
    return next(e)
  }).catch(caught)

  on('turn.complete', async ($, e, next) => {
    await rec($, 'turn.complete', { agentId: e.agentId, name: nameOf(e.agentId), reason: e.reason, isAborted: e.isAborted, ms: e.durationMs, answer: cut(e.answer, 200) })
    return next(e)
  }).catch(caught)

  on('session.receive', async ($, e, next) => {
    await rec($, 'session.receive', { e: cut(JSON.stringify(e), 600) })
    return next(e)
  }).catch(caught)

  on('classic.*', async ($, e, next) => {
    const ev = e.hook_event_name || '(envelope)'
    if (['SessionStart', 'SessionEnd', 'UserPromptSubmit', 'Stop', 'SubagentStart', 'SubagentStop'].includes(ev)) {
      await rec($, 'classic', { ev, agent_id: e.agent_id, source: e.source, prompt: cut(e.prompt, 160), cwd: e.cwd })
    }
    return next(e)
  }).catch(caught)

  // every prompt, with its origin: whether the human typed it, and what the mod sees of a prompt a hook blocks. An
  // agent's report to the lead is dropped, as baton's mod drops it, so the lead has no turn but the ones typed.
  on('prompt.submit', async ($, e, next) => {
    const o = e.origin || {}
    await rec($, 'prompt.submit', { origin: JSON.parse(JSON.stringify(o, (k, v) => (k === 'body' ? cut(v, 120) : v))), agentId: e.agentId, turnId: e.turnId, wait: e.wait, isMeta: e.isMeta, text: cut(e.text, 300) })
    if (o.kind === 'task-notification') return { drop: 'spk: an agent reported to the mod, not to Claude' }
    const r = await next(e)
    await rec($, 'prompt.submitted', { text: cut(e.text, 80), r: cut(JSON.stringify(r), 400) })
    if (r && r.drop && (e.text || '').startsWith('@')) box($, e.text)
    return r
  }).catch(caught)
}
