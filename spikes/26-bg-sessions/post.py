#!/usr/bin/env python3
"""Post one message into a Claude Code session's inbox socket, as a host process would.
usage: post.py SOCKET TEXT [--token T] [--priority now|next|later] [--from F] [--raw JSONLINE]
Prints the exact bytes sent and anything the socket writes back within 1.5 s."""
import json, socket, sys, time
a = sys.argv[1:]; sock, text = a[0], a[1]; opt = {}
i = 2
while i < len(a): opt[a[i]] = a[i+1]; i += 2
lines = []
if '--token' in opt: lines.append(json.dumps({'type': 'auth', 'token': opt['--token']}))
if '--raw' in opt: lines.append(opt['--raw'])
else:
    m = {'type': 'user', 'message': {'role': 'user', 'content': text}}
    if '--priority' in opt: m['priority'] = opt['--priority']
    if '--from' in opt: m['from'] = opt['--from']
    lines.append(json.dumps(m))
payload = ''.join(l + '\n' for l in lines).encode()
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); s.connect(sock)
t0 = time.time(); s.sendall(payload)
print('SENT', repr(payload))
s.settimeout(1.5); got = b''
try:
    while True:
        b = s.recv(4096)
        if not b: print('server closed after', round(time.time()-t0, 2), 's'); break
        got += b
except socket.timeout:
    pass
print('RECV', repr(got))
s.close()
