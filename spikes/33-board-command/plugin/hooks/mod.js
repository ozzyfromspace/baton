async function rec($, kind, data) {
  const path = await $.env.get('PROBE_LOG')
  let old = ''
  try { old = await $.fs.read(path) } catch (err) { old = '' }
  await $.fs.write(path, old + JSON.stringify({ kind, ...data }) + '\n')
}
function caught($, e, next) { return next(e) }
async function start($, e, next) {
  const r = await next(e)
  const mode = await $.env.get('PROBE_MODE')
  try {
    if (mode === 'same') await rec($, 'register', { r: await $.command.register({ name: 'bt', description: 'probe: mod bt', immediate: true }) })
    if (mode === 'other') await rec($, 'register', { r: await $.command.register({ name: 'bt-board', description: 'probe: mod bt-board', immediate: true }) })
  } catch (err) {
    await rec($, 'register-failed', { err: String(err) })
  }
  try {
    const list = await $.command.list()
    await rec($, 'list', { bt: list.filter((c) => JSON.stringify(c).includes('bt')) })
  } catch (err) {
    await rec($, 'list-failed', { err: String(err) })
  }
  return r
}
async function ran($, e, next) {
  await rec($, 'command.run', { command: e.command, args: e.args, origin: e.origin })
  if (e.args && e.args.trim() === 'board') return { text: 'MOD-ANSWERED board' }
  const r = await next(e)
  await rec($, 'command.run-next', { r: JSON.stringify(r).slice(0, 500) })
  return r
}
async function submitted($, e, next) {
  await rec($, 'prompt.submit', { text: String(e.text).slice(0, 200), origin: e.origin })
  return next(e)
}
export function register(on) {
  on('session.start', start).catch(caught)
  on('command.run', ran).catch(caught)
  on('prompt.submit', submitted).catch(caught)
}
