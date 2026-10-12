// Spike 32: what a mod that starts and steers phase agents still needs to know (Claude Code 2.1.296).
// Every event it sees and every call it makes is logged as JSON lines to $SPK_LOG.<instance>.<part>: one
// instance per load of this module (a reload starts another), 400 records per part file.

const PART = 400
const L = { log: [], written: 0, path: null, inst: null, dirty: false, flushing: false }
let agents = {} // name -> agentId, kept in $.store so a reloaded module still knows them
let scen = 'x'
let flags = {}
let last = ''
const compacting = [] // the loops compacting now, newest last (the stamp variant)
let soak = null // { until, names, n }
const pending = [] // work for the next ui.render (@render): { spawn: specs } or { send: [how, to, text] }

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
  // one long command: the agent is running for about a minute
  W: 'You are working on phase NAME of a plan. Run this exact Bash command: sh long.sh. Then use the Write tool to create the file NAME.txt in the current directory containing exactly the word done. Then reply with the codeword you were given, if any, and the word DONE.',
  // two long commands, one after the other
  W2: 'You are working on phase NAME of a plan. Run this exact Bash command: sh long.sh. When it has finished, run this exact Bash command again: sh long.sh. Then use the Write tool to create the file NAME.txt in the current directory containing exactly the word done. Then reply DONE.',
  // quick
  Q: 'You are working on phase NAME of a plan. Use the Write tool to create the file NAME.txt in the current directory containing exactly the word done. Then reply with the codeword you were given, if any, and the word DONE.',
  // fills a small context window
  BIG: 'You are working on phase NAME of a plan. Use the Read tool to read big1.txt, then big2.txt, then big3.txt, then big4.txt, all in the current directory, one at a time and in full. Then use the Write tool to create the file NAME.txt in the current directory containing the codeword you were given at the start, or UNKNOWN if you no longer know it. Then reply with that codeword and DONE.',
  // the proxy fails this agent's requests (the marker is in its prompt)
  ERR: 'SPKFAIL-MARKER. You are working on phase NAME of a plan. Use the Write tool to create the file NAME.txt in the current directory containing exactly the word done. Then reply DONE.',
  // one round of the soak
  S: 'You are working on phase NAME of a plan. Run this exact Bash command: sh slow.sh. Then reply DONE.',
}

async function save($) {
  await $.store.set(scen + ':agents', agents)
}

function who(s) {
  return agents[s] || s
}

// A hook's .catch: logs why it was asked (a throw, a timeout, or `re-entry`: an event from inside one of this mod's
// own API calls), then lets the event through. Under SPK_THROW* a throw is rethrown, as if there were no .catch.
async function caught($, e, next) {
  const err = next.error || {}
  const keys = e && typeof e === 'object' ? Object.keys(e).slice(0, 12).join(',') : ''
  await rec($, 'caught', { kind: err.kind, cause: err.cause, message: cut(err.message, 200), called: next.called, agentId: e && e.agentId, ev: e && e.hook_event_name, tool: e && e.tool, trigger: e && e.trigger, keys, origin: next.origin })
  if (err.kind === 'throw' && (flags.THROW || flags.THROW_COMPACT)) throw new Error('spk: rethrown, as if the hook had no .catch')
  return next(e)
}

async function spawnAll($, specs, how) {
  for (const s of specs) {
    const [name, key] = s.split(':')
    await spawn($, name, key || 'Q', how)
  }
}

async function runPending($, p) {
  if (p.spawn) await spawnAll($, p.spawn, '@render')
  else await send($, p.send[0], p.send[1], p.send[2])
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

async function send($, how, to, text) {
  const agentId = who(to)
  const t0 = await $.clock.now()
  try {
    let r
    if (how === 'session') r = await $.session.send({ to: { agentId }, text })
    else r = await $.tool.call({ tool: 'SendMessage', to: agentId, message: text, summary: 'spk' })
    await rec($, 'sent', { how, to, agentId, ms: (await $.clock.now()) - t0, r: cut(JSON.stringify(r), 600) })
  } catch (err) {
    await rec($, 'send-failed', { how, to, agentId, ms: (await $.clock.now()) - t0, err: cut(String(err), 600) })
  }
}

export function register(on) {
  on('session.start', async ($, e, next) => {
    scen = (await $.env.get('SPK_SCEN')) || 'x'
    flags = {
      DROP_NOTES: (await $.env.get('SPK_DROP_NOTES')) === '1',
      STAMP: (await $.env.get('SPK_STAMP')) === '1',
      SEND_ON_COMPACT: (await $.env.get('SPK_SEND_ON_COMPACT')) === '1',
      THROW: (await $.env.get('SPK_THROW')) === '1',
      THROW_COMPACT: (await $.env.get('SPK_THROW_COMPACT')) === '1',
      TICK: (await $.env.get('SPK_TICK')) === '1',
    }
    const boots = Number((await $.store.get(scen + ':boots')) ?? 0) + 1
    await $.store.set(scen + ':boots', boots)
    agents = ((await $.store.get(scen + ':agents')) ?? {})
    await rec($, 'session.start', { e: cut(JSON.stringify(e)), boots, agents, flags, keys: await $.store.keys() })
    const cmds = ['spk-go', 'spk-send', 'spk-msg', 'spk-stop', 'spk-list', 'spk-ask', 'spk-compact', 'spk-submit', 'spk-store', 'spk-soak']
    for (const name of cmds) {
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
          $.ui.invalidate('ui.render')
        }
      } catch (err) {
        await rec($, 'list-failed', { err: String(err) })
      }
    })
    if (flags.TICK) {
      let n = 0
      $.clock.every(5000, async () => {
        n++
        await rec($, 'tick', { n, store: await $.store.get(scen + ':counter') })
      })
    }
    return next(e)
  })

  on('command.run', { command: 'spk-go' }, async ($, e, next) => {
    // /spk-go [@timer|@hook|@cmd] P1:W P2:Q — one at a time, as baton would. @timer (the default) spawns from a
    // $.clock.after callback, @hook inside this command.run hook, @cmd from a timer that runs /spk-go @hook.
    let specs = (e.args || '').trim().split(/\s+/).filter(Boolean)
    let how = '@timer'
    if (specs[0] && specs[0].startsWith('@')) how = specs.shift()
    await rec($, 'go', { how, specs, origin: next.origin })
    if (how === '@hook') await spawnAll($, specs, how)
    else if (how.startsWith('@rtimer')) {
      // as a command poll would: a timer queues the spawn and asks for a render; the render hook spawns
      const ms = Number(how.split(':')[1] || 1000)
      $.clock.after(ms, async () => {
        pending.push({ spawn: specs })
        await rec($, 'invalidate', { specs })
        $.ui.invalidate('ui.render')
      })
    } else if (how === '@render') {
      pending.push({ spawn: specs })
      $.ui.invalidate('ui.render')
    } else if (how === '@cmd') {
      $.clock.after(10, async () => {
        try {
          const r = await $.command.run({ command: 'spk-go', args: '@hook ' + specs.join(' ') })
          await rec($, 'command-ran', { r: cut(JSON.stringify(r)) })
        } catch (err) {
          await rec($, 'command-run-failed', { err: String(err) })
        }
      })
    } else $.clock.after(10, () => spawnAll($, specs, how))
    return { text: 'spk: spawning ' + how + ' ' + specs.join(' ') }
  })

  for (const [cmd, how] of [['spk-send', 'session'], ['spk-msg', 'tool']]) {
    on('command.run', { command: cmd }, async ($, e) => {
      let [to, ...rest] = (e.args || '').trim().split(' ')
      if (to === '@hook') {
        to = rest.shift()
        await send($, how, to, rest.join(' '))
      } else if (to === '@render') {
        to = rest.shift()
        const text = rest.join(' ')
        pending.push({ send: [how, to, text] })
        $.ui.invalidate('ui.render')
      } else $.clock.after(10, () => send($, how, to, rest.join(' ')))
      return { text: `spk: ${cmd} ${to}` }
    })
  }

  on('command.run', { command: 'spk-stop' }, async ($, e) => {
    const to = (e.args || '').trim()
    $.clock.after(10, async () => {
      try {
        const r = await $.tool.call({ tool: 'TaskStop', task_id: who(to) })
        await rec($, 'stopped', { to, agentId: who(to), r: cut(JSON.stringify(r), 600) })
      } catch (err) {
        await rec($, 'stop-failed', { to, agentId: who(to), err: cut(String(err), 600) })
      }
    })
    return { text: 'spk: stop ' + to }
  })

  on('command.run', { command: 'spk-list' }, async ($, e) => {
    const list = await $.agent.list()
    await rec($, 'list-cmd', { list })
    return { text: 'spk: ' + JSON.stringify(list.map((a) => [a.name, a.status])) }
  })

  on('command.run', { command: 'spk-ask' }, async ($, e) => {
    const ms = Number((e.args || '').trim() || 500)
    $.clock.after(ms, async () => {
      const t0 = await $.clock.now()
      await rec($, 'asking', {})
      try {
        const a = await $.ui.ask('spk: the worker on P4 asks: may it commit unsigned?', { options: ['Go ahead', 'Wait for me', 'Let baton decide'], header: 'spk' })
        await rec($, 'asked', { a, ms: (await $.clock.now()) - t0 })
      } catch (err) {
        await rec($, 'ask-failed', { err: String(err), ms: (await $.clock.now()) - t0 })
      }
    })
    return {}
  })

  on('command.run', { command: 'spk-compact' }, async ($, e) => {
    $.clock.after(500, async () => {
      try {
        const r = await $.session.compact({ instructions: 'Keep it to one sentence.' })
        await rec($, 'compacted', { r: cut(JSON.stringify(r)) })
      } catch (err) {
        await rec($, 'compact-failed', { err: String(err) })
      }
    })
    return {}
  })

  on('command.run', { command: 'spk-submit' }, async ($, e) => {
    const text = e.args || 'Reply with only the word woken.'
    $.clock.after(500, async () => {
      try {
        const r = await $.prompt.submit({ text })
        await rec($, 'submitted', { r: cut(JSON.stringify(r)) })
      } catch (err) {
        await rec($, 'submit-failed', { err: String(err) })
      }
    })
    return {}
  })

  on('command.run', { command: 'spk-store' }, async ($, e) => {
    const n = Number((await $.store.get(scen + ':counter')) ?? 0) + 1
    await $.store.set(scen + ':counter', n)
    await rec($, 'store', { n, agents: await $.store.get(scen + ':agents') })
    return { text: 'spk: counter ' + n }
  })

  on('command.run', { command: 'spk-soak' }, async ($, e) => {
    // /spk-soak <minutes> P1 P2 ...: each time one of them ends a turn, send it the next round
    const [min, ...names] = (e.args || '').trim().split(/\s+/)
    const now = await $.clock.now()
    soak = { until: now + Number(min) * 60000, names, n: {} }
    await rec($, 'soak', { soak })
    return { text: 'spk: soak ' + JSON.stringify(soak) }
  })

  on('agent.spawn', async ($, e, next) => {
    const r = await next(e)
    await rec($, 'agent.spawn', { name: e.name, cwd: e.cwd, background: e.background, type: e.subagentType, origin: next.origin, r })
    return r
  }).catch(caught)

  on('turn.complete', async ($, e, next) => {
    await rec($, 'turn.complete', { agentId: e.agentId, reason: e.reason, isAborted: e.isAborted, refusal: e.refusal, usage: e.usage, ms: e.durationMs, answer: cut(e.answer, 200) })
    if (soak && e.agentId) {
      const name = Object.keys(agents).find((k) => agents[k] === e.agentId)
      if (name && soak.names.includes(name)) {
        const now = await $.clock.now()
        if (now < soak.until) {
          soak.n[name] = (soak.n[name] || 0) + 1
          const round = soak.n[name]
          $.clock.after(2000, () => send($, 'session', name, `Round ${round}: run this exact Bash command: sh slow.sh. Then reply DONE ${round}.`))
        } else {
          await rec($, 'soak-over', { name, n: soak.n[name] || 0 })
        }
      }
    }
    return next(e)
  }).catch(caught)

  on('tool.call', async ($, e, next) => {
    await rec($, 'tool.call', { agentId: e.agentId, tool: e.tool, command: e.command, file_path: e.file_path, id: e.tool_use_id })
    if (flags.THROW && String(e.command || '').includes('THROWME')) throw new Error('spk: a tool.call hook threw')
    return next(e)
  }).catch(caught)

  on('session.compact', async ($, e, next) => {
    const loop = e.agentId || 'main'
    const t0 = await $.clock.now()
    await rec($, 'session.compact', { phase: 'before', trigger: e.trigger, agentId: e.agentId, n: (e.messages || []).length })
    if (flags.THROW_COMPACT) throw new Error('spk: a session.compact hook threw')
    if (flags.SEND_ON_COMPACT && e.agentId) {
      send($, 'session', e.agentId, 'While you were compacting, baton sent this: use the Write tool to create the file during-compact.txt in your current directory containing exactly the word c. Then reply DONE.')
    }
    compacting.push(loop)
    let r
    try {
      r = await next(e)
    } finally {
      compacting.splice(compacting.lastIndexOf(loop), 1)
    }
    await rec($, 'session.compact', { phase: 'after', trigger: e.trigger, agentId: e.agentId, ms: (await $.clock.now()) - t0, skip: r && r.skip, n: r && r.messages ? r.messages.length : null, tokensBefore: r && r.tokensBefore, tokensAfter: r && r.tokensAfter })
    return r
  }).catch(caught)

  on('classic.*', async ($, e, next) => {
    const ev = e.hook_event_name || '(envelope)'
    const top = compacting.length ? compacting[compacting.length - 1] : null
    if (['PreCompact', 'PostCompact', 'SessionStart', 'SubagentStart', 'SubagentStop', 'StopFailure', 'Stop', 'UserPromptSubmit', 'PermissionRequest', 'Notification'].includes(ev)) {
      await rec($, 'classic', { ev, agent_id: e.agent_id, agent_type: e.agent_type, source: e.source, trigger: e.trigger, prompt_id: e.prompt_id, compacting: compacting.slice(), keys: Object.keys(e).join(',') })
    }
    if (flags.STAMP && ['PreCompact', 'PostCompact', 'SessionStart'].includes(ev)) {
      return next({ ...e, spk_loop: top || 'none' })
    }
    return next(e)
  }).catch(caught)

  on('prompt.submit', async ($, e, next) => {
    const ours = Object.values(agents).some((id) => (e.text || '').includes(id))
    await rec($, 'prompt.submit', { origin: e.origin, ours, text: cut(e.text, 400) })
    if (flags.THROW && (e.text || '').includes('THROWPROMPT')) throw new Error('spk: a prompt.submit hook threw')
    if (flags.DROP_NOTES && ours && e.origin && e.origin.kind === 'task-notification') {
      return { drop: 'spk: a phase agent reported to the mod, not to Claude' }
    }
    return next(e)
  }).catch(caught)

  on('session.receive', async ($, e, next) => {
    await rec($, 'session.receive', { origin: e.origin, text: cut(e.text) })
    return next(e)
  }).catch(caught)

  on('session.end', async ($, e, next) => {
    await rec($, 'session.end', { reason: e.reason, sessionId: e.sessionId })
    return next(e)
  }).catch(caught)

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    // @render: start what is pending from inside this hook (an engine frame), not awaited
    while (pending.length) {
      const p = pending.shift()
      rec($, 'render-run', { p })
      runPending($, p).then(
        () => rec($, 'render-done', {}),
        (err) => rec($, 'render-failed', { err: String(err) }),
      )
    }
    if (!last) return next(e)
    const { Text } = $.ui.resolve(e)
    return Text({ children: ['◆ spk band: ' + last] })
  })
}
