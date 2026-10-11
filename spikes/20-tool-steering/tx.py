import json,sys,glob,os
sid=sys.argv[1]
f=glob.glob(os.path.expanduser('~/.claude/projects/*/')+sid+'.jsonl')[0]
for l in open(f):
    d=json.loads(l); m=d.get('message')
    if d.get('type')=='attachment':
        a=d.get('attachment'); 
        if a.get('type') not in ('total_tokens_reminder','deferred_tools_record'): print('ATT', json.dumps(a)[:400])
        continue
    if not isinstance(m,dict): continue
    c=m.get('content')
    if isinstance(c,list):
        for x in c:
            if x.get('type')=='tool_use': print(d.get('timestamp'),'TOOL_USE', json.dumps(x['input'])[:400])
            if x.get('type')=='tool_result': print(d.get('timestamp'),'TOOL_RESULT', json.dumps(x.get('content'))[:500])
            if x.get('type')=='text': print(d.get('timestamp'),d['type'].upper(),'TEXT', x['text'][:300].replace('\n',' '))
    elif isinstance(c,str): print(d.get('timestamp'),d['type'].upper(),'STR', c[:300].replace('\n',' '))
    if 'toolUseResult' in d: print('   toolUseResult', json.dumps(d['toolUseResult'])[:300])
