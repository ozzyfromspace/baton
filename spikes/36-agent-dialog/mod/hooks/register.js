// Spike 36: a phase agent's permission prompt on the lead's screen (Claude Code 2.1.300, Haiku), and how a stopped
// agent looks to a mod. Spike 35's mod, with: the verdict of every tool call (tool.check, with the loop's agentId)
// and when each call ends (tool.call); when the band above the prompt is drawn (invalidated every second); a
// question asked from the band's render hook, as baton's mod asks; a plan of timed actions that starts when one of
// its agents' calls is put to the human (SPK_ON_ASK), since keys typed while that dialog is up would go to it; and
// messages and stops as baton's mod sends them ($.tool.call SendMessage, TaskStop). Every event it sees and every
// call it makes is logged as JSON lines to $SPK_LOG.<instance>.<part>.

const PART = 400
const L = { log: [], written: 0, path: null, inst: null, dirty: false, flushing: false }
let agents = {} // name -> agentId
let scen = 'x'
let flags = {}
let last = ''
let onAsk = {} // the n-th agent call put to the human -> [[ms, what, arg], ...]
let agentAsks = 0
let renders = 0
let renderLogged = 0
let lastView = ''
let askFromRender = null // a question the band asks at its next render

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
  // two commands no rule allows, in default mode: two permission prompts, one after the other
  A: 'You are working on phase NAME of a plan. Run this exact Bash command: sh ask.sh. When it has finished, run this exact Bash command: sh ask2.sh. Then reply with only the word DONE.',
  // three long commands a rule allows: the agent is at work for about three minutes, with no prompt
  L: 'You are working on phase NAME of a plan. Run this exact Bash command: sh long.sh. When it has finished, run this exact Bash command again: sh long.sh. When that has finished, run it a third time: sh long.sh. Then reply with only the word DONE.',
  // quick, then idle
  Q: 'You are working on phase NAME of a plan. Run this exact Bash command: pwd. Then reply with only the word DONE.',
}

async function save($) {
  await $.store.set(scen + ':agents', agents)
}

function who(s) {
  return agents[s] || s
}

function ours(id) {
  return !!id && Object.values(agents).includes(id)
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
    if (r.agentId) {
      agents[name] = r.agentId
      await save($)
    }
    await rec($, 'spawned', { name, key, how, r })
  } catch (err) {
    await rec($, 'spawn-failed', { name, err: String(err) })
  }
}

// msg messages an agent as baton's mod does: the SendMessage tool, called by the mod.
async function msg($, to, text) {
  const agentId = who(to)
  const t0 = await $.clock.now()
  try {
    const r = await $.tool.call({ tool: 'SendMessage', to: agentId, message: text, summary: 'spk' })
    await rec($, 'sent', { to, agentId, ms: (await $.clock.now()) - t0, r: cut(JSON.stringify(r), 800) })
  } catch (err) {
    await rec($, 'send-failed', { to, agentId, ms: (await $.clock.now()) - t0, err: cut(String(err), 800) })
  }
}

// One timed action, run from a timer. Each logs when it started and how its promise ended.
async function act($, id, what, arg) {
  const t0 = await $.clock.now()
  const done = async (kind, data) => rec($, kind, { id, what, ms: (await $.clock.now()) - t0, ...data })
  await rec($, 'act-start', { id, what, arg: cut(arg, 120) })
  try {
    if (what === 'compact') {
      $.session.compact(arg ? { instructions: arg } : {}).then(
        (r) => done('compact-resolved', { skip: r && r.skip, n: r && r.messages ? r.messages.length : null, tokensBefore: r && r.tokensBefore, tokensAfter: r && r.tokensAfter }),
        (err) => done('compact-rejected', { err: cut(String(err), 400) }),
      )
    } else if (what === 'submit') {
      $.prompt.submit({ text: arg || 'Reply with only the word SUBMITTED.', asUser: true }).then(
        (r) => done('submit-resolved', { r: cut(JSON.stringify(r), 400) }),
        (err) => done('submit-rejected', { err: cut(String(err), 400) }),
      )
    } else if (what === 'read') {
      const b = await $.prompt.read()
      await done('read', { text: b.text, cursor: b.cursor })
    } else if (what === 'renderask') {
      askFromRender = arg || 'spk: the worker on P4 asks: may it commit unsigned?'
      $.ui.invalidate('ui.render')
      await done('renderask-set', {})
    } else if (what === 'msg') {
      const [to, ...rest] = arg.split(' ')
      await msg($, to, rest.join(' '))
      await done('act-done', {})
    } else if (what === 'stop') {
      const r = await $.tool.call({ tool: 'TaskStop', task_id: who(arg) })
      await done('stopped', { r: cut(JSON.stringify(r), 600) })
    } else if (what === 'spawn') {
      const [name, key] = arg.split(':')
      await spawn($, name, key || 'Q', '@timer')
      await done('act-done', {})
    } else if (what === 'list') {
      const list = await $.agent.list()
      await done('listed', { list })
    } else await done('act-unknown', {})
  } catch (err) {
    await done('act-failed', { err: cut(String(err), 400) })
  }
}

let acts = 0

function schedule($, plan, why) {
  for (const [ms, what, arg] of plan) {
    const id = ++acts
    rec($, 'act-queued', { id, ms, what, arg: arg || '', why })
    $.clock.after(ms, () => act($, id, what, arg || ''))
  }
}

function command(i) {
  const c = i && typeof i === 'object' ? i.command : undefined
  return typeof c === 'string' ? cut(c, 80) : undefined
}

export function register(on) {
  on('session.start', async ($, e, next) => {
    scen = (await $.env.get('SPK_SCEN')) || 'x'
    flags = {
      DROP_NOTES: (await $.env.get('SPK_DROP_NOTES')) === '1',
    }
    try {
      onAsk = JSON.parse((await $.env.get('SPK_ON_ASK')) || '{}')
    } catch (err) {
      onAsk = {}
    }
    agents = (await $.store.get(scen + ':agents')) ?? {}
    await rec($, 'session.start', { e: cut(JSON.stringify(e)), agents, flags, onAsk })
    for (const name of ['spk-go', 'spk-msg', 'spk-at', 'spk-list']) {
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
    // the band asks to be drawn every second, as baton's does: a gap in the renders is a time it was not drawn
    $.clock.every(1000, () => $.ui.invalidate('ui.render'))
    return next(e)
  })

  // /spk-go P1:A P2:L — spawned inside this hook, so the mod sees the agents' own events (spike 32, S0)
  on('command.run', { command: 'spk-go' }, async ($, e) => {
    const specs = (e.args || '').trim().split(/\s+/).filter(Boolean)
    for (const s of specs) {
      const [name, key] = s.split(':')
      await spawn($, name, key || 'Q', '@hook')
    }
    return { text: 'spk: spawned ' + specs.join(' ') }
  })

  // /spk-msg P1 text — from a timer, as baton's bridge sends
  on('command.run', { command: 'spk-msg' }, async ($, e) => {
    const [to, ...rest] = (e.args || '').trim().split(' ')
    $.clock.after(10, () => msg($, to, rest.join(' ')))
    return { text: 'spk: msg ' + to }
  })

  // /spk-at <ms> <what> [arg] — run one action from a timer <ms> from now; several may be pending
  on('command.run', { command: 'spk-at' }, async ($, e) => {
    const [ms, what, ...rest] = (e.args || '').trim().split(' ')
    schedule($, [[Number(ms), what, rest.join(' ')]], 'spk-at')
    return { text: `spk: ${what} in ${ms} ms` }
  })

  on('command.run', { command: 'spk-list' }, async ($, e) => {
    const list = await $.agent.list()
    await rec($, 'list-cmd', { list })
    return { text: 'spk: ' + JSON.stringify(list.map((a) => [a.name, a.status])) }
  })

  // Every call's verdict, with the loop it is decided in. An ask of one of this mod's agents starts the plan
  // SPK_ON_ASK gives for it (the first, the second, ...).
  on('tool.check', async ($, e, next) => {
    const r = await next(e)
    await rec($, 'tool.check', { agentId: e.agentId, name: nameOf(e.agentId), tool: e.tool, command: command(e.input), id: e.tool_use_id, decision: r && r.decision, reason: cut(r && r.reason, 200), hook: r && r.hook, decider: r && r.decider })
    if (r && r.decision === 'ask' && ours(e.agentId)) {
      agentAsks++
      const plan = onAsk[String(agentAsks)]
      if (plan) schedule($, plan, 'agent ask ' + agentAsks)
    }
    return r
  }).catch(caught)

  // When each call starts, and when its chain (the permission prompt, then the tool) ends.
  on('tool.call', async ($, e, next) => {
    const t0 = await $.clock.now()
    const base = { agentId: e.agentId, name: nameOf(e.agentId), tool: e.tool, command: command(e.input), id: e.tool_use_id }
    await rec($, 'call-start', base)
    try {
      const r = await next(e)
      await rec($, 'call-end', { ...base, ms: (await $.clock.now()) - t0, isError: r && r.isError, deny: r && r.deny, text: cut(r && r.text, 160) })
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
    const all = JSON.parse(JSON.stringify(e, (k, v) => (typeof v === 'string' ? cut(v, 200) : v)))
    await rec($, 'turn.complete', { agentId: e.agentId, name: nameOf(e.agentId), reason: e.reason, isAborted: e.isAborted, ms: e.durationMs, answer: cut(e.answer, 200), e: all })
    return next(e)
  }).catch(caught)

  on('session.compact', async ($, e, next) => {
    await rec($, 'session.compact', { phase: 'before', trigger: e.trigger, agentId: e.agentId })
    const r = await next(e)
    await rec($, 'session.compact', { phase: 'after', trigger: e.trigger, agentId: e.agentId, skip: r && r.skip })
    return r
  }).catch(caught)

  on('session.end', async ($, e, next) => {
    await rec($, 'session.end', { e: cut(JSON.stringify(e), 400) })
    return next(e)
  }).catch(caught)

  on('classic.*', async ($, e, next) => {
    const ev = e.hook_event_name || '(envelope)'
    if (['PreCompact', 'PostCompact', 'SessionStart', 'SessionEnd', 'UserPromptSubmit', 'Stop', 'PermissionRequest', 'Notification', 'SubagentStop'].includes(ev)) {
      await rec($, 'classic', { ev, agent_id: e.agent_id, source: e.source, trigger: e.trigger, tool: e.tool_name, type: e.notification_type, message: cut(e.message, 160), prompt: cut(e.prompt, 120), reason: e.reason })
    }
    return next(e)
  }).catch(caught)

  on('prompt.submit', async ($, e, next) => {
    const o = e.origin || {}
    const named = Object.values(agents).some((id) => (e.text || '').includes(id))
    await rec($, 'prompt.submit', { origin: JSON.parse(JSON.stringify(o, (k, v) => (k === 'body' ? cut(v, 120) : v))), turnId: e.turnId, named, text: cut(e.text, 300) })
    if (flags.DROP_NOTES && named && o.kind === 'task-notification') return { drop: 'spk: a phase agent reported to the mod, not to Claude' }
    return next(e)
  }).catch(caught)

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    renders++
    if (askFromRender) {
      // as baton's band asks: $.ui.ask called while this hook still runs, before anything is awaited after
      const q = askFromRender
      askFromRender = null
      let asked
      try {
        asked = $.ui.ask(q, { options: ['Go ahead', 'Wait for me', 'Let baton decide'], header: 'spk' })
      } catch (err) {
        asked = Promise.reject(err)
      }
      rec($, 'asking', { from: 'render' })
      asked.then(
        (a) => rec($, 'asked', { a: String(a) }),
        (err) => rec($, 'ask-failed', { err: cut(String(err), 200) }),
      )
    }
    const view = JSON.stringify(e.props.view || {})
    const t = await $.clock.now()
    if (t - renderLogged >= 1000 || view !== lastView) {
      renderLogged = t
      lastView = view
      rec($, 'render', { n: renders, view: e.props.view, isWorking: e.props.isWorking, hasSurvey: e.props.hasSurvey })
    }
    const { Text } = $.ui.resolve(e)
    return Text({ children: ['◆ spk band: ' + (last || '-') + ' · r' + renders] })
  }).catch(caught)
}
