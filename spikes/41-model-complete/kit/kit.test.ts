// Spike 41's mod under Claude Code's test kit: what the test's on('model.complete') stub gets and gives.
import { test, expect, mock } from 'claude-code/testing'

const USAGE = { input_tokens: 36, output_tokens: 7, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 }

function world(on: any, stub?: ($: any, e: any) => any) {
  const files: Record<string, string> = {}
  const seen: any[] = []
  mock.env(on, { SPK_DIR: '/w', SPK_LOG: '/w/mod.log' })
  const clock = mock.clock(on, { now: 1_000_000 })
  on('session.id', async () => ({ value: 'sess' }))
  on('session.start', async ($: any, e: any) => ({ cwd: e.cwd }))
  on('fs.write', async ($: any, e: any) => {
    files[e.path] = e.content ?? e.data ?? e.text
    return { value: undefined }
  })
  on('fs.read', async ($: any, e: any) => (e.path in files ? { value: files[e.path] } : { deny: 'ENOENT: ' + e.path }))
  on('fs.list', async ($: any, e: any) => {
    const pre = e.path + '/'
    const names = Object.keys(files)
      .filter((p) => p.startsWith(pre))
      .map((p) => p.slice(pre.length))
    return { value: names.map((name) => ({ name, kind: 'file', size: 1, mtimeMs: 0, isLink: false })) }
  })
  if (stub)
    on('model.complete', async ($: any, e: any) => {
      seen.push(e)
      return stub($, e)
    })
  return { files, seen, clock }
}

async function run($: any, w: any, c: any) {
  await $.session.start({ cwd: '/w', surface: 'terminal', isInteractive: true })
  w.files['/w/cmd/1.json'] = JSON.stringify({ label: 'kit', model: 'haiku', system: 'One line.', prompt: 'Say PONG.', maxTokens: 400, timeoutMs: 60000, ...c })
  await w.clock.advance(1000)
  return w.files['/w/mod.log'] || ''
}

test('the stub answers as the session would, and reads the request', async ($, on) => {
  const w = world(on, () => ({ value: { isAnswered: true, text: 'PONG', usage: USAGE } }))
  const log = await run($, w, {})
  expect(log).toContain('"isAnswered":true,"text":"PONG"')
  expect(w.seen[0]).toEqual(expect.objectContaining({ model: 'haiku', system: 'One line.', prompt: 'Say PONG.', maxTokens: 400, timeoutMs: 60000 }))
})

test('an api-error is a value too', async ($, on) => {
  const w = world(on, () => ({ value: { isAnswered: false, reason: 'api-error', status: 529, error: 'overloaded', usage: USAGE } }))
  const log = await run($, w, {})
  expect(log).toContain('"reason":"api-error","status":529,"error":"overloaded"')
})

test('a deny rejects the call', async ($, on) => {
  const w = world(on, () => ({ deny: 'blocked model' }))
  const log = await run($, w, {})
  expect(log).toContain('HooksError: spkm: $.model.complete: blocked model')
})

test('with no stub, the call rejects', async ($, on) => {
  const w = world(on)
  const log = await run($, w, {})
  expect(log).toContain('HooksError: no implementation for model.complete')
})
