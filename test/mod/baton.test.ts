// baton's mod (plugin/hooks/baton.js) under Claude Code's own test kit: `make mod-test` copies the plugin
// and this file to a scratch folder and runs `claude plugin test` there, so the plugin baton ships holds
// no tests. Beneath the mod, the world answers as a session baton hosts would: the environment the host
// sets, `baton mod report` (the host's side of the bridge, kept here in memory), the bridge's files, and
// the agents, tools and dialog the mod calls.
import { test, expect, mock } from 'claude-code/testing'

const DIR = '/proj/.baton/runs/sess/mod'
const HOSTED = { BATON_HOST: '1', BATON_BIN: '/opt/baton', BATON_INSTANCE: 'inst' }
const AGENT = { name: 'phase', description: 'A phase of a plan baton runs.', prompt: 'You work on one phase.' }

type Opts = {
  env?: Record<string, string>
  store?: Record<string, unknown>
  files?: Record<string, string>
  // reply answers a report in the host's place (undefined: as the host would)
  reply?: (rep: any) => any
}

function cmdPath(id: number) {
  return DIR + '/out/' + String(id).padStart(6, '0') + '.json'
}

function command(c: any) {
  return { [cmdPath(c.id)]: JSON.stringify({ instance: 'inst', ...c }) }
}

function world(on: any, o: Opts = {}) {
  const w = {
    files: { ...(o.files ?? {}) } as Record<string, string>,
    reports: [] as any[], // what the host recorded, in order
    runs: [] as string[], // every `baton mod report` the mod ran
    calls: [] as string[], // what the mod did beneath
    seen: 0,
    answer: 'Go ahead',
    clock: null as any,
    beneath: [] as string[], // the events that reached the bottom
  }
  mock.env(on, o.env ?? HOSTED)
  mock.store(on, o.store ?? {})
  w.clock = mock.clock(on, { now: 1_000_000 })
  on('session.id', async () => ({ value: 'sess' }))
  on('session.start', async ($: any, e: any) => ({ cwd: e.cwd }))
  on('process.run', async ($: any, e: any) => {
    const rep = JSON.parse(e.init.stdin)
    w.runs.push(rep.kind)
    let reply = o.reply ? o.reply(rep) : undefined
    if (reply === undefined) {
      if (rep.kind === 'hello') reply = { ok: true, run: 'sess', dir: DIR, seen: w.seen, agent: AGENT }
      else if (rep.seq <= w.seen) reply = { ok: true, ignored: 'recorded already' }
      else {
        w.seen = rep.seq
        w.reports.push(rep)
        if (rep.kind === 'ack') delete w.files[cmdPath(rep.command)]
        reply = { ok: true }
      }
    }
    const stdout = typeof reply === 'string' ? reply : JSON.stringify(reply)
    return { value: { exitCode: 0, stdout, stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('fs.read', async ($: any, e: any) => (e.path in w.files ? { value: w.files[e.path] } : { deny: 'ENOENT: ' + e.path }))
  on('fs.list', async ($: any, e: any) => {
    const pre = e.path + '/'
    const names = Object.keys(w.files).filter((p) => p.startsWith(pre)).map((p) => p.slice(pre.length))
    return { value: names.map((name) => ({ name, kind: 'file', size: 1, mtimeMs: 0, isLink: false })) }
  })
  on('agent.register', async ($: any, e: any) => {
    w.calls.push(`register ${e.name} ${e.isolation} ${e.prompt}`)
    return { value: { agent: 'baton:' + e.name } }
  })
  on('agent.spawn', async ($: any, e: any) => {
    // The kit raises it with the Agent tool's input, and only Claude Code's core sets an agent's id, so here
    // no agent starts: what the mod does with one is for the end-to-end scenarios.
    w.calls.push(`spawn ${e.subagentType ?? e.subagent_type} ${e.name} [${e.description}] ${e.prompt}`)
    return { model: 'haiku' }
  })
  on('tool.call', async ($: any, e: any) => {
    if (e.tool === 'AskUserQuestion') {
      w.calls.push('ask ' + e.questions[0].question)
      return { result: { questions: e.questions, answers: { [e.questions[0].question]: w.answer } } }
    }
    w.calls.push(`${e.tool} ${e.to ?? e.task_id}${e.message ? ' ' + e.message : ''}`)
    if (e.tool === 'SendMessage' && e.to === 'stopped') return { result: { success: false, message: 'was stopped by the user' } }
    return { result: { success: true, message: e.tool === 'TaskStop' ? 'Successfully stopped task' : 'Message queued for delivery' } }
  })
  on('prompt.submit', async ($: any, e: any) => {
    w.beneath.push('prompt ' + e.origin.kind)
    return { text: e.text, origin: e.origin }
  })
  on('turn.complete', async ($: any, e: any) => {
    w.beneath.push('turn ' + (e.agentId ?? 'lead'))
    return { text: e.answer }
  })
  on('session.compact', async ($: any, e: any) => {
    w.beneath.push(`compact ${e.agentId ?? 'lead'} after ${w.runs.filter((k) => k === 'compacting').length} compacting reports`)
    return { messages: e.messages, tokensBefore: 2, tokensAfter: 1 }
  })
  on('agent.offer', async ($: any, e: any) => ({ isOffered: true }))
  return w
}

// start starts the session and lets the mod say hello and poll a few times.
async function start($: any, w: any, ms = 3000) {
  await $.session.start({ cwd: '/proj', surface: 'terminal', isInteractive: true })
  await w.clock.advance(ms)
}

function kinds(w: any) {
  return w.reports.map((r: any) => r.kind + (r.command ? ' ' + r.command : '') + (r.agent ? ' ' + r.agent : '') + (r.interrupted ? ' interrupted' : ''))
}

test('outside a session baton hosts, the mod does nothing at all', async ($, on) => {
  const env = { BATON_BIN: '/opt/baton', BATON_INSTANCE: 'inst' } // all but BATON_HOST
  const w = world(on, { env, files: { ...command({ id: 1, kind: 'spawn', phase: 'P1', prompt: 'Begin P1.' }) } })
  await start($, w, 10_000)
  const r = await $.prompt.submit({ text: 'a1 finished', origin: { kind: 'task-notification' }, wait: false })
  await $.turn.complete({ agentId: 'a1', reason: 'answer', answer: 'DONE', durationMs: 1, isAborted: false, turnId: 't' })
  expect(w.runs).toEqual([])
  expect(w.calls).toEqual([])
  expect((r as any).text).toBe('a1 finished')
  expect(w.beneath).toEqual(['prompt task-notification', 'turn a1'])
})

test('a baton too old to answer the hello leaves the mod dormant', async ($, on) => {
  const w = world(on, { reply: () => 'baton: "mod" is not a baton command', files: command({ id: 1, kind: 'ask', question: 'Q?' }) })
  await start($, w, 20_000)
  expect(w.runs).toEqual(['hello'])
  expect(w.calls).toEqual([])
})

test('a mod baton does not want says hello once, and does nothing more', async ($, on) => {
  const w = world(on, { reply: (rep) => (rep.kind === 'hello' ? { ok: false, why: "a worker's session" } : undefined), files: command({ id: 1, kind: 'ask', question: 'Q?' }) })
  await start($, w, 20_000)
  expect(w.runs).toEqual(['hello'])
  expect(w.calls).toEqual([])
})

test('a hello baton asks to wait for is tried again until it holds', async ($, on) => {
  let n = 0
  const w = world(on, { reply: (rep) => (rep.kind === 'hello' && ++n < 3 ? { ok: false, retry: true, why: 'no run yet' } : undefined) })
  await start($, w, 5_000)
  expect(w.runs.filter((k) => k === 'hello').length).toBe(3)
  expect(w.calls).toEqual(['register phase worktree You work on one phase.'])
})

test("the host's commands run in order, once each, and only its own", async ($, on) => {
  const w = world(on, {
    files: {
      ...command({ id: 1, kind: 'spawn', phase: 'P1', prompt: 'Begin P1.', description: 'P1 — one', name: 'P1', drop: "P1's report went to baton" }),
      ...command({ id: 2, kind: 'send', agent: 'a1', text: 'The gate passed.' }),
      ...command({ id: 3, kind: 'stop', agent: 'a1' }),
      ...command({ id: 4, kind: 'send', agent: 'stopped', text: 'Go on.' }),
      ...command({ id: 5, kind: 'ask', question: 'May P3 commit unsigned?', options: ['Go ahead', 'Wait'] }),
      ...command({ id: 6, kind: 'ask', question: "Another host's?", instance: 'inst-gone' }),
      [DIR + '/out/.000007.json.tmp-1']: '{"id":7',
    },
  })
  await start($, w)
  expect(w.calls).toEqual([
    'register phase worktree You work on one phase.',
    'spawn baton:phase P1 [P1 — one] Begin P1.',
    'SendMessage a1 The gate passed.',
    'TaskStop a1',
    'SendMessage stopped Go on.',
    'ask May P3 commit unsigned?',
  ])
  expect(kinds(w)).toEqual(['ack 1', 'ack 2', 'ack 3', 'ack 4', 'ack 5', 'answered 5'])
  expect(w.reports[0]).toEqual(expect.objectContaining({ ok: false, error: 'no agent started' }))
  expect(w.reports.map((r: any) => r.seq)).toEqual([1, 2, 3, 4, 5, 6])
  expect(w.reports[3]).toEqual(expect.objectContaining({ ok: false, result: 'was stopped by the user' }))
  expect(w.reports[5]).toEqual(expect.objectContaining({ answer: 'Go ahead' }))
  expect(Object.keys(w.files)).toEqual([cmdPath(6), DIR + '/out/.000007.json.tmp-1'])
})

test('an ack the host has not recorded is reported again, never the command run again', async ($, on) => {
  let down = true
  const w = world(on, {
    files: command({ id: 1, kind: 'send', agent: 'a1', text: 'Hi.' }),
    reply: (rep) => (rep.kind !== 'hello' && down ? 'baton: state is locked' : undefined),
  })
  await start($, w)
  expect(w.calls.filter((c) => c.startsWith('SendMessage'))).toEqual(['SendMessage a1 Hi.'])
  expect(w.reports).toEqual([])
  down = false
  await w.clock.advance(2000)
  expect(w.calls.filter((c) => c.startsWith('SendMessage'))).toEqual(['SendMessage a1 Hi.'])
  expect(kinds(w)).toEqual(['ack 1'])
})

test('reloaded, the mod goes on after the last command it ran, and acks one it was running as interrupted', async ($, on) => {
  const w = world(on, {
    store: { 'baton:inst': { last: 2, running: 3, seq: 4, outbox: [{ kind: 'turn', agent: 'a1', reason: 'answer', seq: 4 }], at: 1_000_000 } },
    files: {
      ...command({ id: 2, kind: 'send', agent: 'a1', text: 'Two.' }),
      ...command({ id: 3, kind: 'send', agent: 'a1', text: 'Three.' }),
      ...command({ id: 4, kind: 'send', agent: 'a1', text: 'Four.' }),
    },
  })
  w.seen = 3
  await start($, w)
  expect(w.calls.filter((c) => c.startsWith('SendMessage'))).toEqual(['SendMessage a1 Four.'])
  expect(kinds(w)).toEqual(['turn a1', 'ack 3 interrupted', 'ack 4'])
  expect(w.reports.map((r: any) => r.seq)).toEqual([4, 5, 6])
})

test("a mod whose store was lost numbers its reports after the host's count", async ($, on) => {
  const w = world(on)
  w.seen = 41
  await start($, w)
  await $.turn.complete({ agentId: 'a1', reason: 'answer', answer: 'DONE', durationMs: 5, isAborted: false, turnId: 't' })
  w.files[DIR + '/agents.json'] = JSON.stringify({ agents: [{ id: 'a1', phase: 'P1', drop: "P1's report went to baton" }] })
  await w.clock.advance(1000)
  await $.turn.complete({ agentId: 'a1', reason: 'answer', answer: 'DONE', durationMs: 5, isAborted: false, turnId: 't' })
  await w.clock.advance(1000)
  expect(w.reports.map((r: any) => r.seq)).toEqual([42])
})

test("baton's agents' reports go to baton; the rewake's brief and the human's prompts reach the model", async ($, on) => {
  const w = world(on, { files: { [DIR + '/agents.json']: JSON.stringify({ agents: [{ id: 'a1', phase: 'P1', drop: "P1's report went to baton" }] }) } })
  await start($, w)
  const dropped = await $.prompt.submit({ text: '<task-notification><task-id>a1</task-id> done</task-notification>', origin: { kind: 'task-notification' }, wait: false })
  const brief = await $.prompt.submit({ text: '[baton] Context compacted. Begin P2.', origin: { kind: 'task-notification' }, wait: false })
  const typed = await $.prompt.submit({ text: 'what is a1 doing?', origin: { kind: 'composer' }, wait: false })
  const other = await $.prompt.submit({ text: '<task-notification><task-id>b7</task-id></task-notification>', origin: { kind: 'task-notification' }, wait: false })
  await w.clock.advance(1000)
  expect((dropped as any).drop).toBe("P1's report went to baton")
  expect((brief as any).text).toBe('[baton] Context compacted. Begin P2.')
  expect((typed as any).text).toBe('what is a1 doing?')
  expect((other as any).text).toContain('b7')
  expect(w.beneath).toEqual(['prompt task-notification', 'prompt composer', 'prompt task-notification'])
  expect(kinds(w)).toEqual(['dropped a1'])
})

test("the turns of baton's agents are reported, how they ended included; no one else's", async ($, on) => {
  const w = world(on, { files: { [DIR + '/agents.json']: JSON.stringify({ agents: [{ id: 'a1', phase: 'P1' }] }) } })
  await start($, w)
  await $.turn.complete({ agentId: 'a1', reason: 'aborted', answer: '', durationMs: 900, isAborted: true, turnId: 't1' })
  await $.turn.complete({ agentId: 'a1', reason: 'refusal', refusal: { category: 'cyber', explanation: null }, answer: '', durationMs: 5, isAborted: false, turnId: 't2' } as any)
  await $.turn.complete({ agentId: 'model-own', reason: 'answer', answer: 'x', durationMs: 5, isAborted: false, turnId: 't3' })
  await $.turn.complete({ reason: 'answer', answer: 'lead', durationMs: 5, isAborted: false, turnId: 't4' })
  await w.clock.advance(1000)
  expect(kinds(w)).toEqual(['turn a1', 'turn a1'])
  expect(w.reports[0]).toEqual(expect.objectContaining({ reason: 'aborted', ms: 900 }))
  expect(w.reports[1]).toEqual(expect.objectContaining({ reason: 'refusal', refusal: 'cyber' }))
  expect(w.beneath).toEqual(['turn a1', 'turn a1', 'turn model-own', 'turn lead'])
})

test("the lead's compaction is reported before it runs and after; a subagent's is not", async ($, on) => {
  const w = world(on)
  await start($, w)
  const messages = [{ role: 'user', text: 'Begin P1.', toolUses: [] }]
  await $.session.compact({ trigger: 'auto', messages } as any)
  await $.session.compact({ trigger: 'auto', agentId: 'a1', messages } as any)
  expect(w.beneath).toEqual(['compact lead after 1 compacting reports', 'compact a1 after 1 compacting reports'])
  expect(w.reports.map((r: any) => r.kind + ' ' + r.trigger)).toEqual(['compacting auto', 'compacted auto'])
})

test('a stale reply makes the mod say hello again, then send what it kept meanwhile', async ($, on) => {
  let stale = true
  const w = world(on, {
    files: { [DIR + '/agents.json']: JSON.stringify({ agents: [{ id: 'a1', phase: 'P1' }] }) },
    reply: (rep) => (rep.kind !== 'hello' && stale ? { ok: false, stale: true } : undefined),
  })
  await start($, w)
  await $.turn.complete({ agentId: 'a1', reason: 'answer', answer: '', durationMs: 1, isAborted: false, turnId: 't' })
  await w.clock.advance(500)
  await $.turn.complete({ agentId: 'a1', reason: 'aborted', answer: '', durationMs: 1, isAborted: true, turnId: 't2' }) // while it says hello again
  stale = false
  await w.clock.advance(3000)
  expect(w.runs.filter((k) => k === 'hello').length).toBe(2)
  expect(kinds(w)).toEqual(['turn a1', 'turn a1'])
})

test('the model is never offered the phase agent type', async ($, on) => {
  world(on)
  const r = await $.agent.offer({ agent: 'baton:phase', description: 'x', source: 'plugin', provider: { plugin: 'baton', tier: 'user' } } as any)
  const other = await $.agent.offer({ agent: 'Explore', description: 'x', source: 'built-in', provider: { plugin: 'engine', tier: 'core' } } as any)
  expect((r as any).isOffered).toBe(false)
  expect((other as any).isOffered).toBe(true)
})
