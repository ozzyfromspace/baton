// baton's mod (plugin/hooks/baton.js) under Claude Code's own test kit: `make mod-test` copies the plugin
// and this file to a scratch folder and runs `claude plugin test` there, so the plugin baton ships holds
// no tests. Beneath the mod, the world answers as a session baton hosts would: the environment the host
// sets, `baton mod report` (the host's side of the bridge, kept here in memory), the bridge's files, and
// the agents, tools and dialog the mod calls.
import { test, expect, mock } from 'claude-code/testing'

const DIR = '/proj/.baton/runs/sess/mod'
const HOSTED = { BATON_HOST: '1', BATON_BIN: '/opt/baton', BATON_INSTANCE: 'inst' }
const AGENT = { name: 'phase', description: 'A phase of a plan baton runs.', prompt: 'You work on one phase.' }
const BOARD = { description: "baton's board", title: 'baton' }

type Opts = {
  env?: Record<string, string>
  store?: Record<string, unknown>
  files?: Record<string, string>
  // reply answers a report in the host's place (undefined: as the host would)
  reply?: (rep: any) => any
  // board answers `baton board` with its arguments in the host's place (null: it fails)
  board?: (args: string[]) => string | null
  // placed: whether the board's pane is drawn once opened
  placed?: boolean
  // compact and submit answer the mod's own $.session.compact and $.prompt.submit in Claude Code's place
  // (undefined: as Claude Code would, between turns)
  compact?: ($: any, e: any) => any
  submit?: ($: any, e: any) => any
}

const SUMMARY = [{ role: 'user', text: 'The summary.', toolUses: [] }]

// later is a promise the test settles when it likes: a call Claude Code takes its time over.
function later() {
  let settle: (v: any) => void = () => {}
  const p = new Promise((res) => (settle = res))
  return { p, settle }
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
    hang: false, // the human never answers the dialog
    asking: [] as any[], // the askings the mod sent
    clock: null as any,
    beneath: [] as string[], // the events that reached the bottom
    boards: [] as string[][], // the arguments of every `baton board` the mod ran
    boardEnv: [] as any[], // and what it set in its environment
    panes: [] as any[], // the panes open
    commands: [] as string[], // the slash commands the mod registered
    submitted: [] as any[], // the prompts the mod submitted itself
  }
  mock.env(on, o.env ?? HOSTED)
  mock.store(on, o.store ?? {})
  w.clock = mock.clock(on, { now: 1_000_000 })
  on('session.id', async () => ({ value: 'sess' }))
  on('session.start', async ($: any, e: any) => ({ cwd: e.cwd }))
  on('process.run', async ($: any, e: any) => {
    if (e.argv[0] === '/opt/baton' && e.argv[1] === 'board') {
      const args = e.argv.slice(2)
      w.boards.push(args)
      w.boardEnv.push(e.init?.env ?? {})
      const out = o.board ? o.board(args) : args.includes('--band') ? '◆ baton · step 1/2 · P1 ▶ 1m · lead: working on P1' : 'step 1  ▶ P1  The mod\nlead: working on P1'
      return { value: { exitCode: out === null ? 1 : 0, stdout: out === null ? '' : out + '\n', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
    }
    const rep = JSON.parse(e.init.stdin)
    w.runs.push(rep.kind)
    let reply = o.reply ? o.reply(rep) : undefined
    if (rep.kind === 'asking') w.asking.push(rep)
    if (reply === undefined) {
      if (rep.kind === 'hello') reply = { ok: true, run: 'sess', dir: DIR, seen: w.seen, agent: AGENT, board: BOARD }
      else if (rep.kind === 'asking') reply = { ok: true }
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
      if (w.hang) return new Promise(() => {})
      return { result: { questions: e.questions, answers: { [e.questions[0].question]: w.answer } } }
    }
    w.calls.push(`${e.tool} ${e.to ?? e.task_id}${e.message ? ' ' + e.message : ''}`)
    if (e.tool === 'SendMessage' && e.to === 'stopped') return { result: { success: false, message: 'was stopped by the user' } }
    if (e.tool === 'TaskStop' && e.task_id === 'gone') return { result: 'No task found with ID: gone', isError: true }
    return { result: { success: true, message: e.tool === 'TaskStop' ? 'Successfully stopped task' : 'Message queued for delivery' } }
  })
  on('prompt.submit', async ($: any, e: any) => {
    w.beneath.push('prompt ' + e.origin.kind)
    if (e.origin.kind === 'plugin') {
      w.submitted.push(e)
      if (o.submit) return o.submit($, e)
    }
    return { text: e.text, origin: e.origin }
  })
  on('turn.complete', async ($: any, e: any) => {
    w.beneath.push('turn ' + (e.agentId ?? 'lead'))
    return { text: e.answer }
  })
  on('session.compact', async ($: any, e: any) => {
    w.beneath.push(`compact ${e.agentId ?? 'lead'} after ${w.runs.filter((k) => k === 'compacting').length} compacting reports`)
    if (o.compact) return o.compact($, e)
    return { messages: e.messages?.length ? e.messages : SUMMARY, tokensBefore: 2, tokensAfter: 1 }
  })
  on('agent.offer', async ($: any, e: any) => ({ isOffered: true }))
  on('command.register', async ($: any, e: any) => {
    w.commands.push(`/${e.name}${e.immediate ? ' immediate' : ''}: ${e.description}`)
    return { value: { command: e.name } }
  })
  on('command.run', async ($: any, e: any) => {
    w.beneath.push(`command ${e.command} ${e.args}`)
    return { text: 'the skill ran' }
  })
  on('ui.open', async ($: any, e: any) => {
    w.panes = w.panes.filter((p: any) => p.id !== e.id).concat([{ id: e.id, title: e.title }])
    return { value: o.placed === false ? { isPlaced: false, reason: 'too narrow' } : { isPlaced: true } }
  })
  on('ui.panes', async () => ({ value: w.panes.map((p: any) => ({ ...p, isShown: true, isFocused: false, isPlaced: o.placed !== false })) }))
  on('ui.render', async ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: ["Claude Code's own"] })
  })
  return w
}

// start starts the session and lets the mod say hello and poll a few times.
async function start($: any, w: any, ms = 3000) {
  await $.session.start({ cwd: '/proj', surface: 'terminal', isInteractive: true })
  await w.clock.advance(ms)
}

// steps advances the clock ms in small steps, so that a call Claude Code answers at once is answered within
// the mod's look at it (AT_ONCE_MS).
async function steps(w: any, ms: number) {
  for (let t = 0; t < ms; t += 50) await w.clock.advance(50)
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
      ...command({ id: 7, kind: 'stop', agent: 'gone' }),
      ...command({ id: 5, kind: 'ask', question: 'May P3 commit unsigned?', options: ['Go ahead', 'Wait'] }),
      ...command({ id: 6, kind: 'ask', question: "Another host's?", instance: 'inst-gone' }),
      [DIR + '/out/.000008.json.tmp-1']: '{"id":8',
    },
  })
  await start($, w)
  expect(w.calls).toEqual([
    'register phase worktree You work on one phase.',
    'spawn baton:phase P1 [P1 — one] Begin P1.',
    'SendMessage a1 The gate passed.',
    'TaskStop a1',
    'SendMessage stopped Go on.',
    'TaskStop gone',
  ])
  expect(kinds(w)).toEqual(['ack 1', 'ack 2', 'ack 3', 'ack 4', 'ack 5', 'ack 7'])
  expect(w.reports[5]).toEqual(expect.objectContaining({ ok: false, error: 'No task found with ID: gone' }))
  expect(w.reports[0]).toEqual(expect.objectContaining({ ok: false, error: 'no agent started' }))
  expect(w.reports.map((r: any) => r.seq)).toEqual([1, 2, 3, 4, 5, 6])
  expect(w.reports[3]).toEqual(expect.objectContaining({ ok: false, result: 'was stopped by the user' }))
  expect(w.reports[4]).toEqual(expect.objectContaining({ ok: true, result: 'queued' }))
  expect(Object.keys(w.files)).toEqual([cmdPath(6), DIR + '/out/.000008.json.tmp-1'])
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

test('the phase agent type stays offered, so that a message resumes its agents', async ($, on) => {
  world(on)
  const r = await $.agent.offer({ agent: 'baton:phase', description: 'x', source: 'plugin', provider: { plugin: 'baton', tier: 'user' } } as any)
  expect((r as any).isOffered).toBe(true)
})

test('the band is the line baton words, read every second and fitted to the columns it is drawn in', async ($, on) => {
  let band = '◆ baton · step 1/2 · P1 ▶ 1m · lead: working on P1'
  const w = world(on, { board: (args) => (args.includes('--band') ? band : 'the board') })
  await start($, w)
  const ui = await $.ui.mount({ plugin: 'baton', surface: 'terminal', component: 'AbovePrompt', props: { hasSurvey: false, isWorking: true, maxRows: 10, bodyColumns: 64 } as any })
  expect((await ui.find({ type: 'Text' }))?.text).toBe(band)
  band = '◆ baton · step 2/2 · P2 ⚙ 1m · lead: idle'
  await w.clock.advance(1000)
  expect((await ui.find({ type: 'Text' }))?.text).toBe(band)
  expect(w.boards[w.boards.length - 1]).toEqual(['--band', '--width', '64'])
  expect(w.boardEnv[0]).toEqual({ CLAUDE_CODE_SESSION_ID: 'sess' })
  expect(w.boards.filter((a) => !a.includes('--band'))).toEqual([]) // no pane open: the board is not read
})

const PROMPT = { hasSurvey: false, isWorking: false, maxRows: 10, bodyColumns: 64 } as any

test('outside a session baton hosts there is no band, and no baton command is run for one', async ($, on) => {
  const w = world(on, { env: { BATON_BIN: '/opt/baton', BATON_INSTANCE: 'inst' } })
  await start($, w)
  const ui = await $.ui.mount({ plugin: 'baton', surface: 'terminal', component: 'AbovePrompt', props: PROMPT })
  expect((await ui.find({ type: 'Text' }))?.text).toBe("Claude Code's own")
  expect(w.boards).toEqual([])
  expect(w.commands).toEqual([])
})

test('with no plan, or a survey up, the band is not baton\'s', async ($, on) => {
  let band = ''
  const w = world(on, { board: (args) => (args.includes('--band') ? band : 'the board') })
  await start($, w)
  expect(w.boards.length).toBeGreaterThan(0)
  const ui = await $.ui.mount({ plugin: 'baton', surface: 'terminal', component: 'AbovePrompt', props: PROMPT })
  expect((await ui.find({ type: 'Text' }))?.text).toBe("Claude Code's own")
  band = '◆ baton · step 1/2 · lead: idle'
  await w.clock.advance(1000)
  const survey = await $.ui.mount({ plugin: 'baton', surface: 'terminal', component: 'AbovePrompt', props: { ...PROMPT, hasSurvey: true } })
  expect((await survey.find({ type: 'Text' }))?.text).toBe("Claude Code's own")
})

test('/baton board opens the pane with the board in it, and never reaches the skill or the model', async ($, on) => {
  const w = world(on)
  await start($, w)
  expect(w.commands).toEqual(["/baton-board immediate: baton's board"])
  const r = await $.command.run({ command: 'baton:baton', args: 'board' })
  expect(r).toEqual({})
  expect(w.panes).toEqual([{ id: 'baton-board', title: 'baton' }])
  expect(w.beneath).toEqual([])
  const ui = await $.ui.mount({ plugin: 'baton', surface: 'terminal', component: 'Pane', requestId: 'baton-board', props: { bodyColumns: 50 } as any })
  expect((await ui.findAll({ type: 'Text' })).map((t) => t.text)).toEqual(['step 1  ▶ P1  The mod', 'lead: working on P1'])
  await w.clock.advance(1000)
  expect(w.boards.filter((a) => !a.includes('--band')).pop()).toEqual(['--width', '50']) // read again while open
  expect(await $.command.run({ command: 'baton-board', args: '' })).toEqual({})
  expect(await $.command.run({ command: 'baton:baton', args: 'status' })).toEqual(expect.objectContaining({ text: 'the skill ran' }))
  expect(w.beneath).toEqual(['command baton:baton status'])
})

test('a pane not drawn yet leaves the board as the command\'s output', async ($, on) => {
  const w = world(on, { placed: false })
  await start($, w)
  expect(await $.command.run({ command: 'baton:baton', args: ' Board ' })).toEqual(expect.objectContaining({ text: 'step 1  ▶ P1  The mod\nlead: working on P1' }))
})

test('where baton board fails, or outside a hosted session, /baton board is the skill\'s', async ($, on) => {
  const w = world(on, { board: () => null })
  await start($, w)
  expect(await $.command.run({ command: 'baton:baton', args: 'board' })).toEqual(expect.objectContaining({ text: 'the skill ran' }))
  expect(w.panes).toEqual([])
})

test("baton's questions are asked from the band, one at a time, and each answer reported", async ($, on) => {
  const w = world(on, {
    files: {
      ...command({ id: 1, kind: 'ask', question: 'baton: the agent on P3 is blocked: no key. Only P3 waits for you.', options: ['Continue', 'Pause baton', 'Wait for me'] }),
      ...command({ id: 2, kind: 'ask', question: 'baton: the agent on P4 is blocked: no disk. Only P4 waits for you.', options: ['Continue', 'Pause baton', 'Wait for me'] }),
    },
  })
  await start($, w)
  expect(w.calls.filter((c) => c.startsWith('ask'))).toEqual([]) // acked, and waiting for the band
  expect(kinds(w)).toEqual(['ack 1', 'ack 2'])
  const ui = await $.ui.mount({ plugin: 'baton', surface: 'terminal', component: 'AbovePrompt', props: PROMPT })
  await ui.find({ type: 'Text' })
  await w.clock.advance(1000)
  await ui.find({ type: 'Text' })
  await w.clock.advance(1000)
  expect(w.calls.filter((c) => c.startsWith('ask'))).toEqual([
    'ask baton: the agent on P3 is blocked: no key. Only P3 waits for you.',
    'ask baton: the agent on P4 is blocked: no disk. Only P4 waits for you.',
  ])
  expect(kinds(w)).toEqual(['ack 1', 'ack 2', 'answered 1', 'answered 2'])
  expect(w.reports[2]).toEqual(expect.objectContaining({ question: 'baton: the agent on P3 is blocked: no key. Only P3 waits for you.', answer: 'Go ahead', deadline: false }))
})

test("a question of baton's nobody answers is answered by the mod at the host's deadline", async ($, on) => {
  const q = 'baton: the agent on P3: no signing key. Unless you answer by 15:04, I will commit unsigned.'
  let due = false
  const w = world(on, {
    files: command({ id: 1, kind: 'ask', question: q, options: ['Wait for me', 'Pause baton', 'Go ahead'] }),
    reply: (rep) => (rep.kind === 'asking' ? (due ? { ok: true, answer: 'Go ahead' } : { ok: true, wait_ms: 60_000 }) : undefined),
  })
  w.hang = true
  await start($, w)
  const ui = await $.ui.mount({ plugin: 'baton', surface: 'terminal', component: 'AbovePrompt', props: PROMPT })
  await ui.find({ type: 'Text' })
  await w.clock.advance(30_000)
  expect(w.asking.length).toBe(1)
  expect(kinds(w)).toEqual(['ack 1'])
  due = true
  await w.clock.advance(30_000)
  await w.clock.advance(1000)
  expect(w.asking.map((a: any) => a.question)).toEqual([q, q])
  expect(w.asking[1].elapsed).toBeGreaterThanOrEqual(60_000)
  expect(kinds(w)).toEqual(['ack 1', 'deadline', 'answered 1'])
  expect(w.reports[1]).toEqual(expect.objectContaining({ question: q, answer: 'Go ahead' }))
  expect(w.reports[2]).toEqual(expect.objectContaining({ question: q, answer: 'Go ahead', deadline: true }))
})

test("the lead model's own question of baton's is bounded too; the human's answer in time stands", async ($, on) => {
  const q = 'baton: the tests need a database. Unless you answer by 15:04, I will use sqlite.'
  const w = world(on, { reply: (rep) => (rep.kind === 'asking' ? { ok: true, wait_ms: 60_000 } : undefined) })
  await start($, w)
  const r = await $.tool.call({ tool: 'AskUserQuestion', questions: [{ question: q, header: 'baton', options: [{ label: 'Go ahead', description: '' }], multiSelect: false }] } as any)
  expect((r as any).result.answers).toEqual({ [q]: 'Go ahead' })
  expect(w.asking.map((a: any) => a.question)).toEqual([q])
  expect(kinds(w)).toEqual([])
})

test("a question baton never answers waits for the human, and one not baton's is not asked about", async ($, on) => {
  const w = world(on)
  w.hang = true
  await start($, w)
  let answered = false
  $.tool.call({ tool: 'AskUserQuestion', questions: [{ question: 'baton: P2 is blocked: no key.', header: 'baton', options: [], multiSelect: false }] } as any).then(() => (answered = true))
  $.tool.call({ tool: 'AskUserQuestion', questions: [{ question: 'Which colour?', header: 'q', options: [], multiSelect: false }] } as any).then(() => (answered = true))
  await w.clock.advance(120_000)
  expect(answered).toBe(false)
  expect(w.asking.map((a: any) => a.question)).toEqual(['baton: P2 is blocked: no key.'])
  expect(kinds(w)).toEqual([])
})

test("baton's compaction and lines run without the poll waiting on them, and how each went follows its ack", async ($, on) => {
  const compacted = later()
  const w = world(on, {
    files: {
      ...command({ id: 1, kind: 'compact', epoch: 3 }),
      ...command({ id: 2, kind: 'submit', text: '[baton] Job j1 (P1) finished; its result is attached.' }),
    },
    compact: () => compacted.p, // a compaction takes a while
  })
  await start($, w, 0)
  await steps(w, 3000)
  expect(w.beneath).toEqual(['compact lead after 0 compacting reports', 'prompt plugin']) // the line went while it compacted
  expect(w.submitted.map((e: any) => e.text)).toEqual(['[baton] Job j1 (P1) finished; its result is attached.'])
  expect(w.submitted[0].origin).toEqual(expect.objectContaining({ kind: 'plugin', asUser: true }))
  expect(kinds(w)).toEqual(['ack 1', 'ack 2'])
  expect(w.reports[0]).toEqual(expect.objectContaining({ ok: true, result: 'compacting' }))
  expect(w.reports[1]).toEqual(expect.objectContaining({ ok: true, result: 'submitted' }))
  compacted.settle({ messages: SUMMARY, tokensBefore: 9, tokensAfter: 1 })
  await w.clock.advance(1000)
  expect(kinds(w)).toEqual(['ack 1', 'ack 2', 'compacted 1'])
  expect(w.reports[2]).toEqual(expect.objectContaining({ epoch: 3, skipped: false }))
  expect(w.runs.filter((k) => k === 'compacting')).toEqual([]) // its own call never reaches its own hook
})

test('a compaction Claude Code refuses at once is acked as refused; one vetoed, or failing later, is reported so', async ($, on) => {
  let n = 0
  const failing = later()
  const w = world(on, {
    files: {
      ...command({ id: 1, kind: 'compact', epoch: 1 }),
      ...command({ id: 2, kind: 'compact', epoch: 1 }),
      ...command({ id: 3, kind: 'compact', epoch: 2 }),
    },
    compact: async () => {
      switch (++n) {
        case 1:
          throw new Error('a turn is running (t1); the conversation compacts between turns')
        case 2:
          return { skip: 'another plugin keeps this conversation whole' }
        default:
          await failing.p
          throw new Error('the summary could not be made')
      }
    },
  })
  await start($, w, 0)
  await steps(w, 3000)
  failing.settle(null)
  await w.clock.advance(1000)
  expect(kinds(w)).toEqual(['ack 1', 'ack 2', 'compacted 2', 'ack 3', 'compacted 3'])
  // A hook that fails is skipped, and the call fails with Claude Code's own error (the kit's here): what the
  // host reads of it is in its words (loop.turnRunning).
  expect(w.reports[0]).toEqual(expect.objectContaining({ ok: false }))
  expect(w.reports[0].error).toContain('HooksError')
  expect(w.reports[1]).toEqual(expect.objectContaining({ ok: true }))
  expect(w.reports[2]).toEqual(expect.objectContaining({ command: 2, epoch: 1, skipped: true, result: 'another plugin keeps this conversation whole' }))
  expect(w.reports[3]).toEqual(expect.objectContaining({ ok: true }))
  expect(w.reports[4]).toEqual(expect.objectContaining({ command: 3, epoch: 2 }))
  expect(w.reports[4].error).toContain('HooksError')
})

test('a line Claude Code fails at once is acked as failed, and one failing later is reported', async ($, on) => {
  let n = 0
  const turn = later() // a turn runs: the line waits for its end
  const w = world(on, {
    files: {
      ...command({ id: 1, kind: 'submit', text: 'one' }),
      ...command({ id: 2, kind: 'submit', text: 'two' }),
    },
    submit: async () => {
      if (++n === 1) throw new Error('no session to submit to')
      await turn.p
      throw new Error('the session ended')
    },
  })
  await start($, w, 0)
  await steps(w, 3000)
  expect(kinds(w)).toEqual(['ack 1', 'ack 2'])
  turn.settle(null)
  await w.clock.advance(1000)
  expect(kinds(w)).toEqual(['ack 1', 'ack 2', 'failed 2'])
  expect(w.reports[0]).toEqual(expect.objectContaining({ ok: false }))
  expect(w.reports[0].error).toContain('HooksError')
  expect(w.reports[1]).toEqual(expect.objectContaining({ ok: true, result: 'submitted' }))
  expect(w.reports[2].error).toContain('HooksError')
})
