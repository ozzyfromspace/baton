#!/usr/bin/env python3
"""Spike 34: can baton's mod settle a question at its deadline with no key typed (Claude Code 2.1.296, Haiku)?

Usage: run.py <scenario> <workroot>. Spike 32's harness (claude in a PTY in a fresh git repo, a settings hook on every
event logging to hooks.jsonl, the PTY's raw bytes kept so a screen renders as drawn) with this spike's mod (mod/), which
asks with $.ui.ask from a timer or from inside the band's render hook, and races the dialog in its own tool.call hook
against the question's deadline. A: from a timer. B: from the render, the deadline passing. C: from the render,
answered in time. D: the model's own question. E: from the render, while a permission prompt is open.
"""
import os, sys, time
HERE = os.path.dirname(os.path.abspath(__file__))
os.environ['SPK_MOD_SRC'] = os.path.join(HERE, 'mod')
sys.path.insert(0, os.path.join(HERE, '..', '32-phase-agents'))
from run import Run  # noqa: E402

ENTER = b'\r'


def asks(r):
    return r.ev('PreToolUse', lambda x: x['input'].get('tool_name') == 'AskUserQuestion')


def shown(txt, q):
    return q in txt


def A(root):
    """From a timer: does the mod's own tool.call hook see its ask?"""
    r = Run(root, 'A')
    r.start()
    r.cmd('/spk-ask timer 6000')
    r.wait(lambda: asks(r), 30, 'the ask')
    r.dump('A-open', 2)
    time.sleep(9)
    txt = r.dump('A-after-deadline', 1)
    print('A: dialog still shown after the deadline:', shown(txt, 'spk 1:'))
    r.key(ENTER, 'enter')
    r.wait(lambda: r.modk('asked') or r.modk('ask-failed'), 20, 'the answer')
    r.d.wait_quiet(2, 10)
    r.finish()
    r.summary({'seen': r.modk('tool.call-seen'), 'asked': r.modk('asked'), 'deadline': r.modk('deadline')})


def B(root):
    """From the render hook, the deadline passing: is the dialog closed, what does the ask resolve to, and do the
    settings hooks see the call end?"""
    r = Run(root, 'B')
    r.start()
    r.cmd('/spk-ask render 6000')
    r.wait(lambda: r.modk('ask-start'), 30, 'the ask')
    txt = r.dump('B-open', 2)
    print('B: shown at 2 s:', shown(txt, 'spk 1:'))
    r.wait(lambda: r.modk('asked') or r.modk('ask-failed'), 20, 'settled')
    txt = r.dump('B-after-deadline', 2)
    print('B: shown after the deadline:', shown(txt, 'spk 1:'))
    if not r.modk('asked') and not r.modk('ask-failed'):
        r.key(b'\x1b', 'esc: dismiss the question')  # never Enter: it would pick an option
        time.sleep(2)
    r.ask('Reply with only the word next.')
    r.dump('B-prompt-after', 1)
    r.finish()
    r.summary({'seen': r.modk('tool.call-seen'), 'asked': r.modk('asked'), 'failed': r.modk('ask-failed'), 'deadline': r.modk('deadline'),
               'beneath': r.modk('beneath-after'), 'post': [x['input'] for x in r.ev('PostToolUse') + r.ev('PostToolUseFailure') if x['input'].get('tool_name') == 'AskUserQuestion']})


def C(root):
    """From the render hook, answered in time: the answer stands, and no deadline fires."""
    r = Run(root, 'C')
    r.start()
    r.cmd('/spk-ask render 20000')
    r.wait(lambda: r.modk('ask-start'), 30, 'the ask')
    r.dump('C-open', 2)
    r.key(ENTER, 'enter: Go ahead')
    r.wait(lambda: r.modk('asked') or r.modk('ask-failed'), 20, 'settled')
    time.sleep(22)
    r.dump('C-after', 1)
    r.finish()
    r.summary({'seen': r.modk('tool.call-seen'), 'asked': r.modk('asked'), 'in_time': r.modk('in-time'), 'deadline': r.modk('deadline')})


def D(root):
    """The model's own AskUserQuestion with a deadline (the lead's own question): closed, and what the model gets."""
    r = Run(root, 'D')
    r.start()
    r.cmd('/spk-deadline spk: proceed? 8000')
    n = r.stops()
    r.cmd('Use the AskUserQuestion tool once, to ask me exactly this question: spk: proceed? with the two options Yes and No. Then reply with only the answer you got.')
    r.wait(lambda: asks(r), 60, 'the model asks')
    txt = r.dump('D-open', 2)
    print('D: shown:', shown(txt, 'spk: proceed?'))
    r.wait(lambda: r.modk('deadline') or r.modk('in-time'), 20, 'the deadline')
    txt = r.dump('D-after-deadline', 2)
    print('D: shown after the deadline:', shown(txt, 'spk: proceed?'))
    r.wait(lambda: r.stops() > n, 60, 'the lead stops')
    r.d.wait_quiet(2, 10)
    r.dump('D-end', 1)
    r.finish()
    r.summary({'seen': r.modk('tool.call-seen'), 'deadline': r.modk('deadline'), 'beneath': r.modk('beneath-after'),
               'post': [x['input'] for x in r.ev('PostToolUse') + r.ev('PostToolUseFailure') if x['input'].get('tool_name') == 'AskUserQuestion'],
               'stop': [x['input'].get('last_assistant_message') for x in r.ev('Stop')]})


def E(root):
    """From the render hook while a permission prompt is open: does the ask wait until the prompt is answered?"""
    r = Run(root, 'E')
    r.start()
    r.cmd('/spk-ask render 6000 8000')
    n = r.stops()
    r.cmd("Run this exact Bash command: python3 -c 'print(6*7)'. Then reply with only what it printed.")
    r.wait(lambda: r.ev('PermissionRequest', lambda x: x['input'].get('tool_name') == 'Bash'), 60, 'permission prompt')
    r.mark('permission prompt up')
    time.sleep(16)
    txt = r.dump('E-prompt', 1)
    print('E: asked while the prompt was up:', bool(r.modk('ask-start')), '; question shown:', shown(txt, 'spk 1:'))
    r.key(ENTER, 'enter: Yes to the permission prompt')
    r.wait(lambda: r.modk('ask-start'), 30, 'the ask after the prompt')
    r.dump('E-ask', 2)
    r.wait(lambda: r.modk('asked') or r.modk('ask-failed'), 20, 'settled')
    r.wait(lambda: r.stops() > n, 60, 'the lead stops')
    r.d.wait_quiet(2, 10)
    r.dump('E-end', 1)
    r.finish()
    r.summary({'queued': r.modk('queued'), 'render': r.modk('render-ask'), 'start': r.modk('ask-start'), 'asked': r.modk('asked'), 'deadline': r.modk('deadline'),
               'permission': [x['t'] for x in r.ev('PermissionRequest')], 'bash_done': [x['t'] for x in r.ev('PostToolUse') if x['input'].get('tool_name') == 'Bash']})


if __name__ == '__main__':
    globals()[sys.argv[1]](sys.argv[2])
