// Spike: is $.session.compact accepted from the lead's turn.complete hook, and what does a call from the
// lead's Stop (as baton's host asks at the Stop hook) get? Logs every step to SPK_LOG.
let LOG = ''
let lines = []
let tried = { stop: false, before: false, after: false, timer: false }
let t0 = 0
let refused = false
let want = false
let polling = null

async function log($, msg) {
  if (!LOG) LOG = (await $.env.get('SPK_LOG')) || ''
  const now = await $.clock.now()
  if (!t0) t0 = now
  lines.push(String(now - t0).padStart(6) + ' ' + msg)
  if (LOG) await $.fs.write(LOG, lines.join('\n') + '\n')
}

// attempt calls compact and logs how it went: refused at once, or not within 400 ms, then how it ended.
async function attempt($, where) {
  await log($, where + ': calling compact')
  const p = Promise.resolve()
    .then(() => $.session.compact({}))
    .then(
      (r) => ({ ok: r }),
      (err) => ({ err: String(err) }),
    )
  p.then((o) => log($, where + ': settled ' + JSON.stringify(o).slice(0, 300)))
  const early = await Promise.race([p, $.clock.sleep(400).then(() => null)])
  await log($, where + ': at 400ms ' + (early ? JSON.stringify(early).slice(0, 300) : 'pending'))
  return early && 'err' in early ? false : true
}

export function register(on) {
  on('session.start', async ($, e, next) => {
    const r = await next(e)
    if (!polling) {
      polling = $.clock.every(100, async () => {
        if (!want) return
        want = false
        refused = !(await attempt($, 'poll'))
      })
    }
    return r
  })
  on('turn.start', async ($, e, next) => {
    if (!e.agentId) await log($, 'turn.start ' + e.turnId)
    return next(e)
  })
  on('classic.Stop', async ($, e, next) => {
    if (!e.agent_id && (await $.env.get('SPK_MODE')) === 'poll' && !tried.early) {
      tried.early = true
      want = true // the poll timer, set at session.start, calls it
      await $.clock.sleep(1500)
    }
    if (!e.agent_id && (await $.env.get('SPK_MODE')) === 'held' && !tried.early) {
      tried.early = true
      // as baton's poll would, while the Stop hooks still run (a slow settings hook)
      $.clock.after(0, async () => {
        refused = !(await attempt($, 'stop-early'))
      })
      await $.clock.sleep(1500)
    }
    const r = await next(e)
    await log($, 'classic.Stop agent=' + (e.agent_id || ''))
    const mode = (await $.env.get('SPK_MODE')) || 'before'
    if (!e.agent_id && !tried.stop && mode === 'stop') {
      tried.stop = true
      // not awaited: the Stop hook returns, as baton's would
      attempt($, 'stop')
    }
    if (!e.agent_id && !tried.stop && mode === 'stoptimer') {
      tried.stop = true
      $.clock.after(0, () => attempt($, 'stop-timer-0'))
    }
    return r
  })
  on('turn.complete', async ($, e, next) => {
    if (e.agentId) return next(e)
    await log($, 'turn.complete ' + e.turnId + ' reason=' + e.reason)
    const mode = (await $.env.get('SPK_MODE')) || 'before'
    if (mode === 'before' && !tried.before) {
      tried.before = true
      await attempt($, 'tc-before-next')
    }
    const r = await next(e)
    if ((mode === 'held' || mode === 'poll') && refused && !tried.held) {
      tried.held = true
      await attempt($, 'tc-held')
    }
    if (mode === 'after' && !tried.after) {
      tried.after = true
      await attempt($, 'tc-after-next')
    }
    if (mode === 'timer' && !tried.timer) {
      tried.timer = true
      $.clock.after(0, () => attempt($, 'tc-timer-0'))
    }
    await log($, 'turn.complete returns')
    return r
  })
  on('session.compact', async ($, e, next) => {
    await log($, 'session.compact hook trigger=' + e.trigger)
    return next(e)
  })
}
