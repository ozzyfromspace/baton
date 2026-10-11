"""Shared helpers for spike 26 (Claude Code background sessions)."""
import json, os, subprocess, time, uuid
HERE = os.path.dirname(os.path.abspath(__file__))
PROJ = os.path.join(HERE, 'proj')
CLEAN = {k: os.environ[k] for k in ('HOME', 'USER', 'LOGNAME', 'PATH', 'TMPDIR', 'SHELL') if k in os.environ}
CLEAN.update(LANG=os.environ.get('LANG', 'en_US.UTF-8'), TERM='xterm-256color')

def note(msg, fn='timeline.log'):
    line = f'[{time.strftime("%H:%M:%S")}] {msg}'
    print(line, flush=True)
    with open(os.path.join(HERE, fn), 'a') as f: f.write(line + '\n')

def cc(*args, cwd=PROJ, timeout=60, inp=None):
    """Run a claude subcommand with a clean env, no TTY. Returns (rc, combined output)."""
    t0 = time.time()
    r = subprocess.run(['claude', *args], cwd=cwd, env=CLEAN, capture_output=True, text=True, timeout=timeout, input=inp)
    return r.returncode, (r.stdout + r.stderr), time.time() - t0

def agents(all_=True):
    rc, out, _ = cc('agents', '--json', *(['--all'] if all_ else []))
    try: return json.loads(out[out.index('['):])
    except Exception: return out

def mine(lst):
    return [a for a in lst if isinstance(a, dict) and HERE in (a.get('cwd') or '')] if isinstance(lst, list) else lst

def hooks(log):
    try: return [json.loads(l) for l in open(log) if l.strip()]
    except FileNotFoundError: return []

def count(log, ev, pred=lambda r: True):
    return sum(1 for r in hooks(log) if r['ev'] == ev and pred(r))

def wait(cond, timeout, what, step=0.5):
    end = time.time() + timeout
    while time.time() < end:
        if cond(): return True
        time.sleep(step)
    note(f'TIMEOUT waiting for {what}')
    return False

def ps():
    out = subprocess.run(['ps', '-axo', 'pid=,ppid=,pgid=,stat=,rss=,%cpu=,etime=,command='], capture_output=True, text=True).stdout
    rows = []
    for l in out.splitlines():
        p = l.split(None, 7)
        if len(p) == 8: rows.append(dict(pid=int(p[0]), ppid=int(p[1]), pgid=int(p[2]), stat=p[3], rss=int(p[4]), cpu=p[5], etime=p[6], cmd=p[7]))
    return rows

def tree(root):
    rows = ps(); kids = {}
    for r in rows: kids.setdefault(r['ppid'], []).append(r)
    out = []; stack = [(r, 0) for r in rows if r['pid'] == root]
    while stack:
        r, d = stack.pop(); out.append((d, r))
        for k in kids.get(r['pid'], []): stack.append((k, d + 1))
    return out

def daemon_pid():
    try: return json.load(open(os.path.expanduser('~/.claude/daemon.lock')))['pid']
    except Exception: return None

def fmt_tree(root):
    return '\n'.join(f"{'  '*d}{r['pid']} ppid={r['ppid']} pgid={r['pgid']} {r['stat']} rss={r['rss']//1024}MB cpu={r['cpu']} {r['cmd'][:150]}" for d, r in tree(root))

def settings(log, extra=None):
    return subprocess.run(['python3', os.path.join(HERE, 'mksettings.py'), log] + ([json.dumps(extra)] if extra else []),
                          capture_output=True, text=True).stdout.strip()

def dump(name, obj):
    with open(os.path.join(HERE, 'out', name), 'w') as f:
        f.write(obj if isinstance(obj, str) else json.dumps(obj, indent=1))
os.makedirs(os.path.join(HERE, 'out'), exist_ok=True)
