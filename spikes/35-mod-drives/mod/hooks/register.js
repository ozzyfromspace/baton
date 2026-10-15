// Spike 35: what a mod can do in baton's place (Claude Code 2.1.300 and 2.1.296): compact the session, submit a
// prompt, read and fill the prompt box, all from a timer as baton's command poll runs; and how agents' reports reach
// the lead (task-notification or a peer hand-back), with the drop a mod can make. Spike 32's mod, cut down. Every
// event it sees and every call it makes is logged as JSON lines to $SPK_LOG.<instance>.<part>.

const PART = 400
const L = { log: [], written: 0, path: null, inst: null, dirty: false, flushing: false }
let agents = {} // name -> agentId
let scen = 'x'
let flags = {}
let last = ''
let stash = null // the draft `stash` took out of the box, for `restore`

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
  // three long commands, one after the other: the agent is at work for about three minutes
  W3: 'You are working on phase NAME of a plan. Run this exact Bash command: sh long.sh. When it has finished, run this exact Bash command again: sh long.sh. When that has finished, run it a third time: sh long.sh. Then use the Write tool to create the file NAME.txt in the current directory containing exactly the word done. Then reply DONE.',
  // one long command
  W: 'You are working on phase NAME of a plan. Run this exact Bash command: sh long.sh. Then use the Write tool to create the file NAME.txt in the current directory containing exactly the word done. Then reply DONE.',
  // quick
  Q: 'You are working on phase NAME of a plan. Use the Write tool to create the file NAME.txt in the current directory containing exactly the word done. Then reply with the word DONE and one short sentence about what you did.',
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

// handedBack is the name of the agent of this mod whose report a peer prompt hands back, or '': the event carries
// only origin { kind: 'peer' } (no sender), so the text's frame is all there is to go on.
function handedBack(text) {
  const m = /^<agent-message from="([^"]+)">\n\[Subagent hand-back\]/.exec(text || '')
  if (!m) return ''
  return Object.keys(agents).find((n) => n === m[1] || agents[n] === m[1]) || ''
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

async function send($, to, text) {
  const agentId = who(to)
  const t0 = await $.clock.now()
  try {
    const r = await $.session.send({ to: { agentId }, text })
    await rec($, 'sent', { to, agentId, ms: (await $.clock.now()) - t0, r: cut(JSON.stringify(r), 600) })
  } catch (err) {
    await rec($, 'send-failed', { to, agentId, ms: (await $.clock.now()) - t0, err: cut(String(err), 600) })
  }
}

// One action of /spk-at, run from a timer. Each logs when it started and how its promise ended.
async function act($, id, what, arg) {
  const t0 = await $.clock.now()
  const done = async (kind, data) => rec($, kind, { id, what, ms: (await $.clock.now()) - t0, ...data })
  await rec($, 'act-start', { id, what, arg: cut(arg, 120) })
  try {
    if (what === 'compact') {
      // not awaited by the caller's poll: the promise is followed on its own, as P2's bridge would
      $.session.compact(arg ? { instructions: arg } : {}).then(
        (r) => done('compact-resolved', { skip: r && r.skip, n: r && r.messages ? r.messages.length : null, tokensBefore: r && r.tokensBefore, tokensAfter: r && r.tokensAfter }),
        (err) => done('compact-rejected', { err: cut(String(err), 400) }),
      )
    } else if (what === 'submit' || what === 'submituser') {
      const args = { text: arg || 'Reply with only the word SUBMITTED.' }
      if (what === 'submituser') args.asUser = true
      $.prompt.submit(args).then(
        (r) => done('submit-resolved', { r: cut(JSON.stringify(r), 400) }),
        (err) => done('submit-rejected', { err: cut(String(err), 400) }),
      )
    } else if (what === 'read') {
      const b = await $.prompt.read()
      await done('read', { text: b.text, cursor: b.cursor })
    } else if (what === 'fill') {
      const r = await $.prompt.fill({ text: arg || '', mode: 'replace' })
      await done('filled', { text: arg || '', r })
    } else if (what === 'stash') {
      const b = await $.prompt.read()
      stash = b
      const r = await $.prompt.fill({ text: '', mode: 'replace' })
      const after = await $.prompt.read()
      await done('stashed', { text: b.text, cursor: b.cursor, r, after: after.text })
    } else if (what === 'restore') {
      const r = await $.prompt.fill({ text: stash ? stash.text : '', mode: 'replace' })
      const after = await $.prompt.read()
      await done('restored', { text: stash ? stash.text : null, r, after: after.text, cursor: after.cursor })
    } else if (what === 'spawn') {
      // from a timer: the mod then sees only the agent's turn.complete (spike 32, S0), which is enough here
      const [name, key] = arg.split(':')
      await spawn($, name, key || 'Q', '@timer')
      await done('act-done', {})
    } else if (what === 'list') {
      const list = await $.agent.list()
      await done('listed', { list: list.map((a) => [a.name, a.status, a.id]) })
    } else await done('act-unknown', {})
  } catch (err) {
    await done('act-failed', { err: cut(String(err), 400) })
  }
}

let acts = 0

export function register(on) {
  on('session.start', async ($, e, next) => {
    scen = (await $.env.get('SPK_SCEN')) || 'x'
    flags = {
      DROP_NOTES: (await $.env.get('SPK_DROP_NOTES')) === '1',
      DROP_PEER: (await $.env.get('SPK_DROP_PEER')) === '1',
    }
    agents = (await $.store.get(scen + ':agents')) ?? {}
    await rec($, 'session.start', { e: cut(JSON.stringify(e)), agents, flags })
    for (const name of ['spk-go', 'spk-send', 'spk-at', 'spk-ask', 'spk-list']) {
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
    return next(e)
  })

  // /spk-go P1:W3 P2:Q — spawned inside this hook, so the mod sees the agents' own events (spike 32, S0)
  on('command.run', { command: 'spk-go' }, async ($, e) => {
    const specs = (e.args || '').trim().split(/\s+/).filter(Boolean)
    for (const s of specs) {
      const [name, key] = s.split(':')
      await spawn($, name, key || 'Q', '@hook')
    }
    return { text: 'spk: spawned ' + specs.join(' ') }
  })

  // /spk-send P1 text — from a timer, as baton's bridge sends
  on('command.run', { command: 'spk-send' }, async ($, e) => {
    const [to, ...rest] = (e.args || '').trim().split(' ')
    $.clock.after(10, () => send($, to, rest.join(' ')))
    return { text: 'spk: send ' + to }
  })

  // /spk-at <ms> <what> [arg] — run one action from a timer <ms> from now; several may be pending
  on('command.run', { command: 'spk-at' }, async ($, e) => {
    const [ms, what, ...rest] = (e.args || '').trim().split(' ')
    const id = ++acts
    await rec($, 'act-queued', { id, ms: Number(ms), what, arg: rest.join(' ') })
    $.clock.after(Number(ms), () => act($, id, what, rest.join(' ')))
    return { text: `spk: ${what} in ${ms} ms (#${id})` }
  })

  on('command.run', { command: 'spk-ask' }, async ($, e) => {
    const ms = Number((e.args || '').trim() || 500)
    $.clock.after(ms, async () => {
      await rec($, 'asking', {})
      try {
        const a = await $.ui.ask('spk: the worker on P4 asks: may it commit unsigned?', { options: ['Go ahead', 'Wait for me', 'Let baton decide'], header: 'spk' })
        await rec($, 'asked', { a })
      } catch (err) {
        await rec($, 'ask-failed', { err: String(err) })
      }
    })
    return {}
  })

  on('command.run', { command: 'spk-list' }, async ($, e) => {
    const list = await $.agent.list()
    await rec($, 'list-cmd', { list })
    return { text: 'spk: ' + JSON.stringify(list.map((a) => [a.name, a.status])) }
  })

  on('turn.start', async ($, e, next) => {
    await rec($, 'turn.start', { agentId: e.agentId, turnId: e.turnId })
    return next(e)
  }).catch(caught)

  on('turn.complete', async ($, e, next) => {
    await rec($, 'turn.complete', { agentId: e.agentId, reason: e.reason, isAborted: e.isAborted, ms: e.durationMs, answer: cut(e.answer, 200) })
    return next(e)
  }).catch(caught)

  on('session.compact', async ($, e, next) => {
    await rec($, 'session.compact', { phase: 'before', trigger: e.trigger, agentId: e.agentId })
    const r = await next(e)
    await rec($, 'session.compact', { phase: 'after', trigger: e.trigger, agentId: e.agentId, skip: r && r.skip })
    return r
  }).catch(caught)

  on('classic.*', async ($, e, next) => {
    const ev = e.hook_event_name || '(envelope)'
    if (['PreCompact', 'PostCompact', 'SessionStart', 'UserPromptSubmit', 'Stop'].includes(ev)) {
      await rec($, 'classic', { ev, agent_id: e.agent_id, source: e.source, trigger: e.trigger, prompt: cut(e.prompt, 120) })
    }
    return next(e)
  }).catch(caught)

  on('prompt.submit', async ($, e, next) => {
    const o = e.origin || {}
    const sender = o.senderTaskId || ''
    const named = Object.values(agents).some((id) => (e.text || '').includes(id))
    const back = handedBack(e.text)
    await rec($, 'prompt.submit', { origin: JSON.parse(JSON.stringify(o, (k, v) => (k === 'body' ? cut(v, 120) : v))), turnId: e.turnId, wait: e.wait, ours: ours(sender), named, back, text: cut(e.text, 300) })
    if (flags.DROP_NOTES && named && o.kind === 'task-notification') return { drop: 'spk: a phase agent reported to the mod, not to Claude' }
    if (flags.DROP_PEER && o.kind === 'peer' && (ours(sender) || back)) {
      await rec($, 'dropped', { back, sender })
      return { drop: 'spk: a phase agent handed its report back to the mod, not to Claude' }
    }
    return next(e)
  }).catch(caught)

  on('session.receive', async ($, e, next) => {
    await rec($, 'session.receive', { origin: e.origin, text: cut(e.text) })
    return next(e)
  }).catch(caught)

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    if (!last) return next(e)
    const { Text } = $.ui.resolve(e)
    return Text({ children: ['◆ spk band: ' + last] })
  })
}
