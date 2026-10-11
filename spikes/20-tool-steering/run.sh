#!/bin/sh
# run.sh <mode> <permission-mode> <tag> <prompt>  — one headless haiku run with the spike hook
D=$(cd "$(dirname "$0")" && pwd); mkdir -p "$D/logs" "$D/proj"
mode=$1; perm=$2; tag=$3; prompt=$4
settings=$(python3 - "$mode" "$D" "$tag" <<'PY'
import json,sys
mode,D,tag=sys.argv[1:4]
cmd=f"MODE={mode} HOOKLOG={D}/logs/{tag}.jsonl python3 {D}/hook.py"
h=[{"matcher":"Bash","hooks":[{"type":"command","command":cmd}]}]
print(json.dumps({"hooks":{"PreToolUse":h,"PostToolUse":h,"PostToolUseFailure":h}}))
PY
)
rm -f $D/logs/$tag.jsonl
cd $D/proj
unset $(env | grep -E '^(CLAUDE|BATON_)' | cut -d= -f1) 2>/dev/null
env $EXTRA claude -p --model ${MODEL:-haiku} --settings "$settings" --permission-mode "$perm" --allowedTools Bash --output-format json "$prompt" < /dev/null > $D/logs/$tag.out.json 2>&1
python3 -c "import json,sys; d=json.loads(open(sys.argv[1]).read().split(chr(10),1)[-1] if open(sys.argv[1]).read().startswith(\"Warning\") else open(sys.argv[1]).read()); print(\"session=\"+d[\"session_id\"], \"result=\"+str(d.get(\"result\"))[:400])" $D/logs/$tag.out.json
