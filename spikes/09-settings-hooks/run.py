#!/usr/bin/env python3
"""Spike 09: hooks passed via --settings — do they merge with project hooks, does the compact brief land
before an un-delayed PostCompact rewake, and what do SessionEnd / Notification inputs look like?"""
import json, os, shlex, shutil, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, '..', 'common'))
from scenario import Driver, hooklog_settings

work = os.path.join(HERE, 'work'); shutil.rmtree(work, ignore_errors=True); os.makedirs(os.path.join(work, '.claude'))
HL = os.path.join(HERE, '..', 'common', 'hooklog.py')
log = os.path.join(work, 'hooks.log')
# Project-level hook (tag "project") — to see whether --settings hooks merge with it or replace it.
json.dump({'hooks': {'SessionStart': [{'hooks': [{'type': 'command', 'command': f'python3 "{HL}" SessionStart --tag project --log "{log}"'}]}]}},
          open(os.path.join(work, '.claude', 'settings.json'), 'w'))

BRIEF = ('BATON BRIEF (after compaction): Phase 1 is complete; you are on Phase 2. Run `echo PHASE2_DONE KIWI` with Bash, '
         'then end your turn with one line naming the codeword in this brief.')
def h(ev, *extra, asyncrw=False):
    e = {'type': 'command', 'command': f'python3 "{HL}" {ev} --tag flag --log "{log}" ' + ' '.join(shlex.quote(x) for x in extra)}
    if asyncrw:
        e['asyncRewake'] = True; e['timeout'] = 60
    return [{'hooks': [e]}]
settings = {'hooks': {
    'SessionStart': h('SessionStart', '--brief', BRIEF),
    'PostToolUse': h('PostToolUse', '--mark', 'PHASE1_DONE'),
    'Stop': h('Stop', '--request-compact'),
    'PreCompact': h('PreCompact'),
    'PostCompact': h('PostCompact', '--rewake', 'baton: compacted; begin Phase 2 from the brief.', asyncrw=True),
    'UserPromptSubmit': h('UserPromptSubmit'),
    'Notification': h('Notification'),
    'SessionEnd': h('SessionEnd'),
}}
prompt = ('Phase 1 of a test. Using the Bash tool, run `echo PHASE1_DONE`. Then end your turn with the single line '
          '"phase 1 complete". After that, follow any further instructions you receive.')
d = Driver(work, ['claude', '--model', 'haiku', '--settings', json.dumps(settings), prompt])
d.trust()
req = os.path.join(work, 'compact.request')
if d.wait(lambda: os.path.exists(req), 90, 'compact request from Stop hook'):
    d.wait_quiet(1.5, 20); d.note('typing /compact'); d.type('/compact'); d.enter()
d.wait_log(log, lambda r: 'PHASE2_DONE' in json.dumps(r.get('input', {}).get('tool_input', {})), 150, 'phase 2 done')
d.wait_log(log, lambda r: r['ev'] == 'Stop' and r['t'] > '0' and 'PHASE2' in open(log).read(), 30, 'stop after phase 2')
time.sleep(3); d.wait_quiet(2, 20); d.note('typing /exit'); d.type('/exit'); d.enter()
d.wait_log(log, lambda r: r['ev'] == 'SessionEnd', 20, 'SessionEnd')
d.close()
print('done; see', log)
