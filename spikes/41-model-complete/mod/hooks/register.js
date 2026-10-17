// Spike 41: $.model.complete from the mod's poll, as baton's bridge commands run. The harness writes
// numbered command files to SPK_DIR/cmd/<n>.json; a poll timer set at session.start runs each new one once,
// in order, and does not wait for the call (as baton's mod must not: a model call takes seconds). Every
// step goes to SPK_LOG, with the lead's turn as the mod sees it, and every row the session appends.
let LOG = ''
let DIR = ''
let lines = []
let t0 = 0
let last = 0
let polling = null
let busy = false
let turn = '' // the lead's turn running ('' for none)
let turns = 0 // the lead's turns begun

async function log($, msg) {
  if (!LOG) LOG = (await $.env.get('SPK_LOG')) || ''
  const now = await $.clock.now()
  if (!t0) t0 = now
  lines.push(String(now - t0).padStart(7) + ' ' + msg)
  if (LOG) await $.fs.write(LOG, lines.join('\n') + '\n')
}

function show(v) {
  const s = JSON.stringify(v)
  return s.length > 600 ? s.slice(0, 600) + '…' : s
}

// call runs command c's completion, unawaited by the poll, and logs how it ended and how long it took.
function call($, c) {
  const req = { model: c.model, prompt: c.prompt, maxTokens: c.maxTokens ?? 400, timeoutMs: c.timeoutMs ?? 60000 }
  if (c.system !== undefined) req.system = c.system
  const at = Date.now()
  const during = turn ? 'turn ' + turn : 'idle'
  log($, `#${c.id} ${c.label}: calling (${during}, ${turns} lead turns so far) ${show({ ...req, prompt: typeof req.prompt === 'string' ? req.prompt.slice(0, 80) : req.prompt })}`)
  let p
  try {
    p = $.model.complete(req)
  } catch (err) {
    p = Promise.reject(err)
  }
  Promise.resolve(p).then(
    (r) => log($, `#${c.id} ${c.label}: resolved after ${Date.now() - at} ms (${turn ? 'turn ' + turn : 'idle'}, ${turns} lead turns so far): ${show(r)}`),
    (err) => log($, `#${c.id} ${c.label}: rejected after ${Date.now() - at} ms: ${String(err)}`),
  )
}

async function poll($) {
  if (busy || !DIR) return
  busy = true
  try {
    let entries = []
    try {
      entries = await $.fs.list(DIR + '/cmd')
    } catch (err) {
      return
    }
    const ids = entries.map((e) => /^([0-9]+)\.json$/.exec(e.name)).filter(Boolean).map((m) => Number(m[1])).sort((a, b) => a - b)
    for (const id of ids) {
      if (id <= last) continue
      let c
      try {
        c = JSON.parse(await $.fs.read(DIR + '/cmd/' + id + '.json'))
      } catch (err) {
        continue // not written whole yet
      }
      last = id
      call($, { ...c, id })
    }
  } finally {
    busy = false
  }
}

export function register(on) {
  on('session.start', async ($, e, next) => {
    const r = await next(e)
    DIR = (await $.env.get('SPK_DIR')) || ''
    await log($, 'session.start ' + (await $.session.id()))
    if (!polling) polling = $.clock.every(500, () => poll($))
    return r
  })
  on('turn.start', async ($, e, next) => {
    if (!e.agentId) {
      turn = String(e.turnId || '?')
      turns++
      await log($, 'turn.start ' + turn)
    } else await log($, 'turn.start agent ' + e.agentId)
    return next(e)
  })
  on('turn.complete', async ($, e, next) => {
    if (!e.agentId) {
      await log($, `turn.complete ${e.turnId} reason=${e.reason} usage=${show(e.usage)}`)
      turn = ''
    }
    return next(e)
  })
  on('session.append', async ($, e, next) => {
    const m = e.message || {}
    let text = ''
    for (const b of Array.isArray(m.content) ? m.content : []) if (b && b.type === 'text') text += b.text
    if (typeof m.content === 'string') text = m.content
    await log($, `append door=${e.door} origin=${(e.origin && e.origin.kind) || ''} type=${m.type} role=${m.role || ''} agent=${e.agentId || ''} text=${JSON.stringify(text.slice(0, 80))}`)
    return next(e)
  })
}
