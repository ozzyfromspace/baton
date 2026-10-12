// Spike 34: can baton's mod settle a question at its deadline with no key typed (Claude Code 2.1.296)?
// The question is asked with $.ui.ask from a timer (as a command poll would) or from inside the band's
// render hook, and the mod's own tool.call hook for AskUserQuestion races the dialog beneath it against
// the question's deadline: a hook that returns while its next is pending aborts what runs beneath. The
// same hook can bound the model's own AskUserQuestion. Every event and call is logged to $SPK_LOG.<inst>.

const L = { log: [], path: null, inst: null, dirty: false, flushing: false }
const deadlines = {} // question -> ms; set by /spk-ask and /spk-deadline
const pending = [] // questions for the next render (render)
let n = 0

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
        await $.fs.write(`${L.path}.${L.inst}.000`, L.log.map((x) => JSON.stringify(x)).join('\n') + '\n')
      }
    } finally {
      L.flushing = false
    }
  } catch (err) {
    // the log is the spike's only output
  }
}

function cut(s, k = 400) {
  s = String(s ?? '')
  return s.length > k ? s.slice(0, k) + '…' : s
}

async function caught($, e, next) {
  const err = next.error || {}
  await rec($, 'caught', { kind: err.kind, cause: err.cause, message: cut(err.message, 200), tool: e && e.tool })
  return next(e)
}

// doAsk calls $.ui.ask before it awaits anything: from a hook, the call must be made while the hook's frame
// is still the one running (a first try awaited a log line first, and the render hook had returned).
async function doAsk($, q, how) {
  const asking = $.ui.ask(q, { options: ['Go ahead', 'Wait for me', 'Let baton decide'], header: 'spk' })
  const t0 = await $.clock.now()
  await rec($, 'ask-start', { how, question: q })
  try {
    const a = await asking
    await rec($, 'asked', { how, question: q, answer: a, ms: (await $.clock.now()) - t0 })
  } catch (err) {
    await rec($, 'ask-failed', { how, question: q, err: String(err), ms: (await $.clock.now()) - t0 })
  }
}

async function start($, e, next) {
  for (const name of ['spk-ask', 'spk-deadline']) {
    try {
      await $.command.register({ name, description: 'spike: ' + name, immediate: true })
    } catch (err) {
      await rec($, 'register-failed', { name, err: String(err) })
    }
  }
  await rec($, 'session.start', { source: e && e.source })
  return next(e)
}

// /spk-ask <timer|render> <deadline ms> [delay ms]: asks a question of its own after delay.
async function askCommand($, e) {
  const [how, ms, delay] = String(e.args || '').trim().split(/\s+/)
  n++
  const q = `spk ${n}: may the agent on P4 commit unsigned?`
  deadlines[q] = Number(ms) || 0
  $.clock.after(Number(delay) || 300, async () => {
    if (how === 'render') {
      pending.push(q)
      await rec($, 'queued', { question: q })
      $.ui.invalidate('ui.render')
    } else {
      void doAsk($, q, 'timer')
    }
  })
  return {}
}

// /spk-deadline <question> <ms>: a deadline on a question the model asks.
async function deadlineCommand($, e) {
  const args = String(e.args || '').trim()
  const i = args.lastIndexOf(' ')
  deadlines[args.slice(0, i)] = Number(args.slice(i + 1))
  await rec($, 'deadline-set', { question: args.slice(0, i), ms: Number(args.slice(i + 1)) })
  return {}
}

async function band($, e, next) {
  while (pending.length) {
    const q = pending.shift()
    void doAsk($, q, 'render')
    void rec($, 'render-ask', { question: q })
  }
  return next(e)
}

// answerFor is what the deadline answers: the default of baton's question, the first option of the model's.
function answerFor(question) {
  return question.startsWith('spk ') ? 'Let baton decide' : 'Yes'
}

async function bounded($, e, next) {
  const question = e.questions && e.questions[0] && e.questions[0].question
  await rec($, 'tool.call-seen', { question, id: e.tool_use_id, agentId: e.agentId, origin: next.origin })
  const ms = deadlines[question]
  if (!ms) return next(e)
  const beneath = next(e).then((v) => ({ v }), (err) => ({ err: String(err) }))
  const timer = $.clock.sleep(ms, { signal: next.signal }).then(() => null, () => ({ err: 'aborted' }))
  const r = await Promise.race([beneath, timer])
  if (r) {
    await rec($, 'in-time', { question, r: cut(JSON.stringify(r)) })
    return r.v || next(e)
  }
  await rec($, 'deadline', { question })
  void beneath.then((x) => rec($, 'beneath-after', { question, x: cut(JSON.stringify(x)) }))
  // AskUserQuestion's output: the questions asked, and the answers by question (its declared shape).
  return { result: { questions: e.questions, answers: { [question]: answerFor(question) } } }
}

export function register(on) {
  on('session.start', start).catch(caught)
  on('command.run', { command: 'spk-ask' }, askCommand).catch(caught)
  on('command.run', { command: 'spk-deadline' }, deadlineCommand).catch(caught)
  on('ui.render', { component: 'AbovePrompt' }, band).catch(caught)
  on('tool.call', { tool: 'AskUserQuestion' }, bounded).catch(caught)
}
