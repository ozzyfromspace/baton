#!/usr/bin/env python3
"""Spike 43, second run: a lead lowered from one session to the next, and the model of a resumed agent.

Usage: run2.py <workroot>. run.py's harness. Two sessions of one conversation:

  D  `--model haiku --effort max`. A lead turn; A6 of the type with effort low, spawned with model sonnet; A7 of the
     type with model sonnet and effort medium, spawned with no model. Then that type registered again with model
     haiku, and A6 and A7 each resumed by a message: does a resumed agent keep the model it was spawned with (A6),
     and does it re-read its type's model (A7), as it re-reads its type's effort (run 1)?
  E  `--resume <id> --model haiku --effort low`: a lead turn, a step down from D's max.
"""
import glob, json, os, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from run import Run, assistant_entries, runs, user_tier


def main(root):
    r = Run(root)
    print('claude', r.version, 'work', r.work, flush=True)

    r.open('D', ['--model', 'haiku', '--effort', 'max'])
    r.start()
    r.wait(lambda: len(r.modk('registered')) + len(r.modk('register-failed')) >= 4, 30, 'types registered')
    r.ask('Reply with only the word READY.')
    r.cmd('/spk-go A6:t-low:sonnet A7:t-sonnet')
    for name in ('A6', 'A7'):
        r.wait(lambda: r.turns(name), 240, f'{name} done')
    time.sleep(3)
    r.cmd('/spk-re t-sonnet medium haiku')
    r.wait(lambda: r.modk('registered', lambda x: x['why'] == 'again'), 20, 't-sonnet again')
    r.cmd('/spk-msg A6 Run this exact Bash command: pwd. Then reply with only the word AGAIN.')
    r.wait(lambda: len(r.turns('A6')) >= 2, 120, 'A6 resumed and done')
    r.cmd('/spk-msg A7 Run this exact Bash command: pwd. Then reply with only the word AGAIN.')
    r.wait(lambda: len(r.turns('A7')) >= 2, 120, 'A7 resumed and done')
    r.notes['dialog_D'] = r.dialog()
    r.close()

    starts = r.ev('SessionStart')
    sid = starts[0]['input'].get('session_id')
    tp = starts[0]['input'].get('transcript_path')

    r.open('E', ['--resume', sid, '--model', 'haiku', '--effort', 'low'])
    r.start(trust=False)
    time.sleep(4)
    r.notes['dialog_E_at_resume'] = r.dialog()
    if not r.notes['dialog_E_at_resume']:
        r.ask('Reply with only the word OK5.')
    r.close()
    r.notes['user_settings_after'] = user_tier()

    out = {'claude': r.version, 'session': sid, 'notes': r.notes, 'lead': runs(assistant_entries(tp))}
    ids = {s['r']['agentId']: s['name'] for s in r.modk('spawned') if s['r'].get('agentId')}
    out['agents'] = {}
    for p in sorted(glob.glob(os.path.join(os.path.dirname(tp), sid, 'subagents', 'agent-*.jsonl'))):
        aid = os.path.basename(p)[len('agent-'):-len('.jsonl')]
        if aid in ids:
            out['agents'][ids[aid]] = runs(assistant_entries(p))
    out['spawned'] = [{'name': s['name'], 'type': s['type'], 'model_asked': s['model'], 'r': s['r']} for s in r.modk('spawned')]
    out['registered'] = [{'name': x['name'], 'def': x['def'], 'why': x['why']} for x in r.modk('registered')]
    out['sent'] = [x.get('r', x.get('err', ''))[:160] for x in r.modk('sent') + r.modk('send-failed')]
    path = os.path.join(r.work, 'results2.json')
    json.dump(out, open(path, 'w'), indent=1)
    print(json.dumps(out, indent=1))
    print('results:', path)


if __name__ == '__main__':
    main(sys.argv[1])
