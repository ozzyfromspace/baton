#!/usr/bin/env python3
"""Tabulate runs/*/result.json: per (channel, primer, text, model) the count of exact / reworded / no-ask,
and the model's words on each run without an exact ask."""
import collections, glob, json, os

HERE = os.path.dirname(os.path.abspath(__file__))
rows = collections.OrderedDict()
misses = []
for p in sorted(glob.glob(os.path.join(HERE, 'runs', '*', 'result.json')), key=os.path.getmtime):
    r = json.load(open(p))
    k = (r['channel'], r['primer'], r['text'], r['model'])
    c = rows.setdefault(k, collections.Counter())
    c['runs'] += 1
    c[r['class']] += 1
    if r['class'] != 'exact':
        said = [w for w in r['words'] if not w.startswith('<tool_use')]
        misses.append((r['tag'], r['class'], said[0] if said else '(nothing said)'))
print(f'{"channel":8} {"primer":6} {"text":5} {"model":7} runs exact reworded no-ask')
for (ch, pr, tx, m), c in rows.items():
    print(f'{ch:8} {pr:6} {tx:5} {m:7} {c["runs"]:4} {c["exact"]:5} {c["reworded"]:8} {c["no-ask"]:6}')
print()
for tag, cls, w in misses:
    print(f'{tag} [{cls}]: {w[:400]!r}')
