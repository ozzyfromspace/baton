// Spike 31: can a mod start and steer phase agents with no model in the loop?
// Every event it sees and every call it makes is logged to $SPK_LOG as JSON lines.

let log = []
let logPath = null
let last = ''
const agents = {} // name -> agentId

async function rec($, kind, data) {
  log.push({ t: await $.clock.now(), kind, ...data })
  if (logPath) await $.fs.write(logPath, log.map((x) => JSON.stringify(x)).join('\n') + '\n')
}

function cut(s, n = 300) {
  s = String(s ?? '')
  return s.length > n ? s.slice(0, n) + '…' : s
}

const PROMPTS = {
  P1: "You are working on phase P1 of a plan. Run the Bash command `pwd`. Then use the Write tool to create the file p1.txt in the current directory containing exactly the word one. Then reply with the codeword you were given, if any, and the word DONE.",
  P2: "You are working on phase P2 of a plan. Run this exact Bash command: sh slow.sh. Then use the Write tool to create the file p2.txt in the current directory containing exactly the word two. Then reply DONE.",
  P3: "You are working on phase P3 of a plan. Run this exact Bash command: date. Then use the Write tool to create the file p3.txt in the current directory containing its output. Then reply DONE.",
  P4: "You are working on phase P4 of a plan. Run this exact Bash command: python3 -c 'print(6*7)'. Then use the Write tool to create the file p4.txt in the current directory containing its output. Then reply DONE.",
  P5: "You are working on phase P5 of a plan. Use the Read tool to read big1.txt, then big2.txt, then big3.txt, then big4.txt, all in the current directory, one at a time and in full. Then use the Write tool to create the file p5.txt in the current directory containing the codeword you were given at the start, or UNKNOWN if you no longer know it. Then reply with that codeword and DONE.",
}
let dropNotes = false

export function register(on) {
  on('session.start', async ($, e, next) => {
    logPath = await $.env.get('SPK_LOG')
    dropNotes = (await $.env.get('SPK_DROP_NOTES')) === '1'
    await rec($, 'session.start', { e: cut(JSON.stringify(e)) })
    const cmds = [
      ['spk-go', 'spawn the phase agents'],
      ['spk-go2', 'spawn the phase agents through the Agent tool, in worktrees'],
      ['spk-go3', 'spawn the phase agents as spk:phase, an agent type with isolation worktree'],
      ['spk-list', 'list agents'],
      ['spk-send', 'send <name> <text>'],
      ['spk-compact', 'compact the lead'],
      ['spk-submit', 'submit a prompt to the lead'],
      ['spk-wf', 'start a saved workflow by name'],
      ['spk-run', 'run a slash command as if typed'],
      ['spk-ask', 'ask the human a question from the mod'],
    ]
    for (const [name, description] of cmds) {
      try {
        await $.command.register({ name, description, immediate: true })
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
          $.ui.status('spk: ' + s)
          $.ui.invalidate('ui.render')
        }
      } catch (err) {
        await rec($, 'list-failed', { err: String(err) })
      }
    })
    return next(e)
  })

  on('command.run', { command: 'spk-go' }, async ($, e) => {
    const only = (e.args || '').trim()
    for (const name of ['P1', 'P2']) {
      if (only && only !== name) continue
      const cwd = name === 'P1' ? await $.env.get('SPK_WT1') : await $.env.get('SPK_WT2')
      try {
        const r = await $.agent.spawn({ prompt: PROMPTS[name], description: name + ' — spike phase', name, cwd })
        agents[name] = r.agentId
        await rec($, 'spawned', { name, cwd, r })
      } catch (err) {
        await rec($, 'spawn-failed', { name, err: String(err) })
      }
    }
    return { text: 'spk: spawned ' + JSON.stringify(agents) }
  })

  on('command.run', { command: 'spk-go2' }, async ($, e) => {
    const only = (e.args || '').trim()
    for (const name of ['P1', 'P2']) {
      if (only && only !== name) continue
      // Not awaited: a plugin's Agent call resolves once the agent settles.
      $.tool.call({ tool: 'Agent', description: name + ' — spike phase', prompt: PROMPTS[name], name, isolation: 'worktree', run_in_background: true }).then(
        (r) => rec($, 'agent-called', { name, r: cut(JSON.stringify(r), 1500) }),
        (err) => rec($, 'agent-call-failed', { name, err: String(err) }),
      )
    }
    return { text: 'spk: Agent calls made' }
  })

  on('command.run', { command: 'spk-go3' }, async ($, e) => {
    const only = (e.args || '').trim()
    for (const name of Object.keys(PROMPTS)) {
      if (only ? only !== name : !['P1', 'P2'].includes(name)) continue
      try {
        const r = await $.agent.spawn({ subagentType: 'spk:phase', prompt: PROMPTS[name], description: name + ' — spike phase', name })
        agents[name] = r.agentId
        await rec($, 'spawned', { name, r })
      } catch (err) {
        await rec($, 'spawn-failed', { name, err: String(err) })
      }
    }
    return { text: 'spk: spawned ' + JSON.stringify(agents) }
  })

  on('command.run', { command: 'spk-list' }, async ($, e) => {
    const list = await $.agent.list()
    await rec($, 'list-cmd', { list })
    return { text: 'spk: ' + JSON.stringify(list) }
  })

  on('command.run', { command: 'spk-send' }, async ($, e) => {
    const [name, ...rest] = (e.args || '').trim().split(' ')
    const agentId = agents[name] || name
    const r = await $.session.send({ to: { agentId }, text: rest.join(' ') })
    await rec($, 'sent', { name, agentId, r })
    return { text: 'spk: send ' + JSON.stringify(r) }
  })

  on('command.run', { command: 'spk-compact' }, async ($, e) => {
    // From a timer, outside the command's own event: a compaction from inside command.run is refused.
    $.clock.after(500, async () => {
      try {
        const r = await $.session.compact({ instructions: 'Keep it to one sentence.' })
        await rec($, 'compacted', { r })
      } catch (err) {
        await rec($, 'compact-failed', { err: String(err) })
      }
    })
    return {}
  })

  on('command.run', { command: 'spk-submit' }, async ($, e) => {
    // From a timer, outside the command's own event; it resolves when the turn starts, once the session is idle.
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

  on('command.run', { command: 'spk-wf' }, async ($, e) => {
    const name = (e.args || 'spk-plan').trim()
    $.tool.call({ tool: 'Workflow', name }).then(
      (r) => rec($, 'workflow-called', { r: cut(JSON.stringify(r), 2000) }),
      (err) => rec($, 'workflow-failed', { err: String(err) }),
    )
    return { text: 'spk: workflow ' + name + ' requested' }
  })

  on('command.run', { command: 'spk-ask' }, async ($, e) => {
    $.clock.after(500, async () => {
      try {
        const a = await $.ui.ask('spk: the worker on P4 asks: may it commit unsigned?', { options: ['Go ahead', 'Wait for me'], header: 'spk' })
        await rec($, 'asked', { a })
      } catch (err) {
        await rec($, 'ask-failed', { err: String(err) })
      }
    })
    return {}
  })

  on('command.run', { command: 'spk-run' }, async ($, e) => {
    $.command.run({ command: (e.args || '').trim() }).then(
      (r) => rec($, 'command-ran', { r: cut(JSON.stringify(r), 1000) }),
      (err) => rec($, 'command-failed', { err: String(err) }),
    )
    return {}
  })

  on('agent.spawn', async ($, e, next) => {
    const r = await next(e)
    await rec($, 'agent.spawn', { name: e.name, cwd: e.cwd, background: e.background, type: e.subagentType, origin: next.origin, r })
    return r
  })

  on('turn.complete', async ($, e, next) => {
    await rec($, 'turn.complete', { agentId: e.agentId, reason: e.reason, answer: cut(e.answer) })
    return next(e)
  })

  on('tool.call', async ($, e, next) => {
    await rec($, 'tool.call', { agentId: e.agentId, tool: e.tool, command: e.command, file_path: e.file_path })
    return next(e)
  })

  on('session.compact', async ($, e, next) => {
    await rec($, 'session.compact', { e: cut(JSON.stringify(e)) })
    return next(e)
  })

  on('prompt.submit', async ($, e, next) => {
    const ours = Object.values(agents).some((id) => (e.text || '').includes(id))
    await rec($, 'prompt.submit', { origin: e.origin, ours, text: cut(e.text, 200) })
    if (dropNotes && ours && e.origin && e.origin.kind === 'task-notification') {
      return { drop: 'spk: a phase agent reported to the mod, not to Claude' }
    }
    return next(e)
  })

  on('session.receive', async ($, e, next) => {
    await rec($, 'session.receive', { origin: e.origin, text: cut(e.text) })
    return next(e)
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    if (!last) return next(e)
    const { Text } = $.ui.resolve(e)
    return Text({ children: ['◆ spk band: ' + last] })
  })
}
