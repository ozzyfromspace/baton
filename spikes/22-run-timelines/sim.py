"""Replay observed phase segments under a dependency graph and resource limits.
A phase is a list of segments [type, kind, secs]: W = a session working, V = waiting on a gate.
Resources: N concurrently-working sessions; L verification lanes for stateful gates (stack/backend/other);
static gates (lint/types/unit) need no lane. hold=True: a phase keeps its session while its gates run
(today's behaviour). pipelined=True: a dependent may start once its dependencies' last W segment is done
(their trailing verification still runs). gate deps (verification needs another phase's instruments) block
the dependent's first V segment, not its start."""
import heapq, math
INF = math.inf
STATEFUL = {'stack', 'backend', 'other'}

def simulate(segs, hard, gate=None, N=1, L=1, hold=True, pipelined=False):
    gate = gate or {}
    ph = sorted(segs, key=lambda p: int(p[1:]))
    rem = {p: sum(s[2] for s in segs[p]) for p in ph}
    succ = {p: [q for q in ph if p in hard.get(q, ())] for p in ph}
    bl = {}
    def bottom(p):
        if p not in bl: bl[p] = rem[p] + max([bottom(q) for q in succ[p]] or [0])
        return bl[p]
    for p in ph: bottom(p)
    t = 0.0; seg_i = {p: 0 for p in ph}; done_t = {}; lastw_t = {}
    started = set(); holding = set(); running = {}  # p -> (end_time, kind)
    agents = N; lanes = L
    # last W index per phase
    lastw = {p: max([i for i, s in enumerate(segs[p]) if s[0] == 'W'] or [-1]) for p in ph}
    events = []
    def deps_ok(p):
        for q in hard.get(p, ()):
            if pipelined:
                if q not in lastw_t and q not in done_t: return False
            elif q not in done_t: return False
        return True
    def gate_ok(p): return all(q in done_t for q in gate.get(p, ()))
    def finish_seg(p, now):
        nonlocal agents, lanes
        typ, kind, _ = segs[p][seg_i[p]]
        if typ == 'W' and not hold: agents += 1
        if typ == 'V' and kind in STATEFUL: lanes += 1
        if seg_i[p] == lastw[p]: lastw_t[p] = now
        seg_i[p] += 1
        if seg_i[p] >= len(segs[p]):
            done_t[p] = now; lastw_t.setdefault(p, now)
            if hold: agents += 1; holding.discard(p)
    while len(done_t) < len(ph):
        # dispatch in priority order (longest remaining path first)
        progressed = True
        while progressed:
            progressed = False
            for p in sorted(ph, key=lambda p: -bl[p]):
                if p in done_t or p in running: continue
                if p not in started:
                    if not deps_ok(p): continue
                    if hold:
                        if agents <= 0: continue
                        agents -= 1; holding.add(p)
                    started.add(p)
                typ, kind, secs = segs[p][seg_i[p]]
                if typ == 'W':
                    if not hold:
                        if agents <= 0: continue
                        agents -= 1
                    running[p] = t + secs; progressed = True
                else:
                    if seg_i[p] == min([i for i, s in enumerate(segs[p]) if s[0] == 'V'] or [0]) and not gate_ok(p): continue
                    if kind in STATEFUL:
                        if lanes <= 0: continue
                        lanes -= 1
                    running[p] = t + secs; progressed = True
        if not running: raise RuntimeError(f'deadlock at t={t}: done={sorted(done_t)}')
        p = min(running, key=running.get); t = running.pop(p)
        finish_seg(p, t)
    return t, done_t

def critical_path(segs, hard):
    ph = sorted(segs, key=lambda p: int(p[1:])); f = {}
    for p in ph:
        f[p] = max([f[q] for q in hard.get(p, ())] or [0]) + sum(s[2] for s in segs[p])
    return max(f.values())
