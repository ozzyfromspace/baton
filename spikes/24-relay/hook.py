#!/usr/bin/env python3
"""Spike 24 hook: `hook.py <Event> <rundir>`. Logs every event to <rundir>/hooks.jsonl and acts per
<rundir>/cfg.json:
  primer   text for SessionStart(startup) additionalContext
  channel  A  PostToolUse additionalContext on the fire_at-th PostToolUse
           A2 PostToolUse decision=block + reason (hook feedback) on the fire_at-th PostToolUse
           B  Stop decision=block + reason, once
           C  UserPromptSubmit additionalContext, once, on a prompt containing [baton]
           D  PostToolUse additionalContext telling the model to run ./baton-relay (fire_at-th)
           DS Stop decision=block telling the model to run ./baton-relay, once
           E  like A (the variant is in primer/text)
           F  PreToolUse deny (permissionDecisionReason) of the (fire_at+1)-th tool call, once
           G  an asyncRewake hook on Stop: 5 s after the first stop (model idle), exit 2 with the text on stderr
  text     what the channel delivers
AskUserQuestion never opens: PreToolUse records tool_input to asks.jsonl and blocks it (exit 2).
Fails open: any internal error exits 0."""
import json, os, sys, time


def main():
    ev, rundir = sys.argv[1], sys.argv[2]
    raw = sys.stdin.read()
    try:
        inp = json.loads(raw)
    except Exception:
        inp = {'_raw': raw[:500]}
    cfg = json.load(open(os.path.join(rundir, 'cfg.json')))
    st_path = os.path.join(rundir, 'state.json')
    try:
        st = json.load(open(st_path))
    except Exception:
        st = {'post': 0, 'fired': False}
    out, code, err, action = None, 0, '', ''
    ch, text = cfg.get('channel'), cfg.get('text', '')
    tool = inp.get('tool_name')

    if ev == 'StopRewake':  # asyncRewake hook on Stop (channel G): wakes the idle model once by exiting 2
        mark = os.path.join(rundir, 'rewoke')
        if ch != 'G' or os.path.exists(mark):
            sys.exit(0)
        open(mark, 'w').write(str(time.time()))
        time.sleep(5)
        with open(os.path.join(rundir, 'hooks.jsonl'), 'a') as f:
            f.write(json.dumps({'t': time.time(), 'ev': ev, 'tool': None, 'action': 'fired'}) + '\n')
        print(text, file=sys.stderr)
        sys.exit(2)
    if ev == 'SessionStart' and inp.get('source') == 'startup' and cfg.get('primer'):
        out = {'hookSpecificOutput': {'hookEventName': 'SessionStart', 'additionalContext': cfg['primer']}}
        action = 'primer'
    elif ev == 'PreToolUse' and tool == 'AskUserQuestion':
        with open(os.path.join(rundir, 'asks.jsonl'), 'a') as f:
            f.write(json.dumps({'t': time.time(), 'fired': st.get('fired'), 'input': inp.get('tool_input')}) + '\n')
        err, code, action = 'recorded; the human will answer later, carry on', 2, 'blocked-ask'
    elif ev == 'PreToolUse':
        st['pre'] = st.get('pre', 0) + 1
        if ch == 'F' and not st['fired'] and st['pre'] == cfg.get('fire_at', 2) + 1:
            out = {'hookSpecificOutput': {'hookEventName': 'PreToolUse', 'permissionDecision': 'deny',
                                          'permissionDecisionReason': text}}
            st['fired'], action = time.time(), 'fired'
    elif ev == 'PostToolUse' and tool != 'AskUserQuestion':
        st['post'] += 1
        if ch in ('A', 'D', 'E') and not st['fired'] and st['post'] == cfg.get('fire_at', 2):
            out = {'hookSpecificOutput': {'hookEventName': 'PostToolUse', 'additionalContext': text}}
            st['fired'], action = time.time(), 'fired'
        elif ch == 'A2' and not st['fired'] and st['post'] == cfg.get('fire_at', 2):
            out = {'decision': 'block', 'reason': text}
            st['fired'], action = time.time(), 'fired'
    elif ev == 'Stop':
        if ch in ('B', 'DS') and not st['fired'] and not inp.get('stop_hook_active'):
            out = {'decision': 'block', 'reason': text}
            st['fired'], action = time.time(), 'fired'
        else:
            action = 'allow'
    elif ev == 'UserPromptSubmit':
        if ch == 'C' and not st['fired'] and '[baton]' in (inp.get('prompt') or ''):
            out = {'hookSpecificOutput': {'hookEventName': 'UserPromptSubmit', 'additionalContext': text}}
            st['fired'], action = time.time(), 'fired'

    json.dump(st, open(st_path, 'w'))
    with open(os.path.join(rundir, 'hooks.jsonl'), 'a') as f:
        f.write(json.dumps({'t': time.time(), 'ev': ev, 'tool': tool, 'action': action,
                            'source': inp.get('source'), 'prompt': (inp.get('prompt') or '')[:200],
                            'transcript': inp.get('transcript_path'), 'sid': inp.get('session_id'),
                            'stop_hook_active': inp.get('stop_hook_active')}) + '\n')
    if out is not None:
        print(json.dumps(out))
    if err:
        print(err, file=sys.stderr)
    sys.exit(code)


if __name__ == '__main__':
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:  # fail open
        try:
            open(os.path.join(sys.argv[2], 'hook-errors.log'), 'a').write(f'{time.time()} {e!r}\n')
        except Exception:
            pass
        sys.exit(0)
