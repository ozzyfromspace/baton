#!/usr/bin/env python3
"""Render a raw terminal byte file onto a blank screen: render.py FILE [rows cols] -> prints the screen."""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from vt import Screen
data = open(sys.argv[1], 'rb').read()
rows, cols = (int(sys.argv[2]), int(sys.argv[3])) if len(sys.argv) > 3 else (50, 200)
s = Screen(rows, cols); s.feed(data)
txt = s.text().rstrip('\n')
print('\n'.join(l for l in txt.split('\n')))
if s.events: print('-- events:', sorted(set(s.events)), file=sys.stderr)
