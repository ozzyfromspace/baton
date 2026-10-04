"""Condense a stream-json run into a readable timeline."""
import json, sys
for line in open(sys.argv[1]):
    try:
        e = json.loads(line)
    except Exception:
        continue
    t, st = e.get('type'), e.get('subtype')
    if t == 'system':
        extra = {k: e.get(k) for k in ('compact_metadata', 'model', 'permissionMode') if e.get(k)}
        if st == 'hook_response' or st == 'hook_started':
            extra = {k: (str(e.get(k))[:160]) for k in ('hook_event', 'hook_name', 'output', 'stdout', 'outcome', 'exit_code') if e.get(k) is not None}
        print(f'SYSTEM {st} {json.dumps(extra)[:300]}')
    elif t == 'assistant':
        m = e.get('message', {})
        u = m.get('usage', {}) or {}
        ctxn = u.get('input_tokens', 0) + u.get('cache_creation_input_tokens', 0) + u.get('cache_read_input_tokens', 0)
        for b in m.get('content', []):
            if b.get('type') == 'text':
                print(f'ASSIST text (ctx={ctxn}): {b["text"][:200]!r}')
            elif b.get('type') == 'tool_use':
                print(f'ASSIST tool_use (ctx={ctxn}): {b["name"]} {json.dumps(b.get("input"))[:150]}')
    elif t == 'user':
        m = e.get('message', {})
        c = m.get('content')
        if isinstance(c, str):
            print(f'USER text: {c[:300]!r}')
        else:
            for b in c or []:
                if b.get('type') == 'tool_result':
                    cc = b.get('content')
                    print(f'USER tool_result: {str(cc)[:120]!r}')
                elif b.get('type') == 'text':
                    print(f'USER text: {b["text"][:300]!r}')
    elif t == 'result':
        print(f'RESULT {st} turns={e.get("num_turns")} cost={e.get("total_cost_usd")} dur={e.get("duration_ms")} err={e.get("is_error")} : {str(e.get("result"))[:200]!r}')
    else:
        print(f'{t} {st}')
