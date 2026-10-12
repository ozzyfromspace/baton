#!/usr/bin/env python3
"""Spike 32: a local pass-through to api.anthropic.com that fails the requests of one agent on demand.

Usage: proxy.py <port> <workdir>. Claude Code runs with ANTHROPIC_BASE_URL=http://127.0.0.1:<port>. A request whose
body carries SPKFAIL-MARKER (the ERR agent's prompt) is answered with the error that <workdir>/proxy.mode names,
{"status": 500|400|429|529, "count": N}, N times; every other request is forwarded as it is, streamed back. Each
request is logged to <workdir>/proxy.jsonl.
"""
import http.client, http.server, json, os, socketserver, sys, threading, time

PORT, WORK = int(sys.argv[1]), sys.argv[2]
MODE = os.path.join(WORK, 'proxy.mode')
LOG = os.path.join(WORK, 'proxy.jsonl')
lock = threading.Lock()
HOP = {'connection', 'keep-alive', 'transfer-encoding', 'content-length', 'host', 'accept-encoding', 'proxy-connection', 'upgrade'}

ERRORS = {
    500: ('api_error', 'spk: injected internal error'),
    529: ('overloaded_error', 'spk: injected overload'),
    400: ('invalid_request_error', 'spk: injected bad request'),
    429: ('rate_limit_error', 'spk: injected rate limit'),
}


def log(rec):
    rec['t'] = f'{time.time():.3f}'
    with lock:
        with open(LOG, 'a') as f:
            f.write(json.dumps(rec) + '\n')


def take():
    """The error to inject now, or None."""
    with lock:
        try:
            m = json.load(open(MODE))
        except Exception:
            return None
        if m.get('count', 0) <= 0:
            return None
        m['count'] -= 1
        json.dump(m, open(MODE, 'w'))
        return m


class H(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *a):
        pass

    def do_GET(self):
        self.forward(b'')

    def do_HEAD(self):
        self.forward(b'')

    def do_POST(self):
        n = int(self.headers.get('content-length') or 0)
        self.forward(self.rfile.read(n) if n else b'')

    def forward(self, body):
        marked = b'SPKFAIL-MARKER' in body
        m = take() if marked else None
        if m:
            st = int(m.get('status', 500))
            typ, msg = ERRORS.get(st, ('api_error', 'spk: injected'))
            out = json.dumps({'type': 'error', 'error': {'type': typ, 'message': msg}}).encode()
            self.send_response(st)
            self.send_header('content-type', 'application/json')
            self.send_header('content-length', str(len(out)))
            self.send_header('request-id', 'req_spk_injected')
            if st == 429:
                reset = int(time.time()) + int(m.get('reset_in', 120))
                self.send_header('retry-after', str(m.get('retry_after', 60)))
                self.send_header('anthropic-ratelimit-unified-status', 'rejected')
                self.send_header('anthropic-ratelimit-unified-reset', str(reset))
                self.send_header('anthropic-ratelimit-unified-representative-claim', 'five_hour')
                self.send_header('anthropic-ratelimit-unified-5h-status', 'rejected')
                self.send_header('anthropic-ratelimit-unified-5h-reset', str(reset))
                self.send_header('anthropic-ratelimit-unified-5h-utilization', '1.0')
            self.send_header('connection', 'close')
            self.end_headers()
            self.wfile.write(out)
            self.close_connection = True
            log({'path': self.path, 'marked': True, 'injected': st, 'left': m['count']})
            return
        c = http.client.HTTPSConnection('api.anthropic.com', timeout=600)
        hdrs = {k: v for k, v in self.headers.items() if k.lower() not in HOP}
        try:
            c.request(self.command, self.path, body=body or None, headers=hdrs)
            r = c.getresponse()
        except Exception as err:
            log({'path': self.path, 'marked': marked, 'upstream_error': str(err)})
            self.send_error(502)
            return
        self.send_response(r.status)
        for k, v in r.getheaders():
            if k.lower() not in HOP:
                self.send_header(k, v)
        self.send_header('connection', 'close')
        self.end_headers()
        size = 0
        while True:
            chunk = r.read1(65536) if hasattr(r, 'read1') else r.read(65536)
            if not chunk:
                break
            size += len(chunk)
            try:
                self.wfile.write(chunk)
                self.wfile.flush()
            except OSError:
                break
        self.close_connection = True
        log({'path': self.path, 'marked': marked, 'status': r.status, 'bytes': size})


class S(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True


S(('127.0.0.1', PORT), H).serve_forever()
