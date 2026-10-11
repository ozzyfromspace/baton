#!/bin/sh
# How do kern.memorystatus_level and the pressure level move while a process holds GB gigabytes?
# usage: ./memory.sh LABEL GB HOLD [random]   (e.g. ./memory.sh r8 8 40s random)
# The probe samples every 2s from 20s before the hog until 30s after it; a watchdog ends the hog at once
# if macOS reports critical pressure (4).
set -u
cd "$(dirname "$0")"
label=$1 gb=$2 hold=$3 kind=${4:-}
mkdir -p out
out=out/mem-$label.jsonl
: > "$out"
./work/probe watch 2s 600s >> "$out" &
watch=$!
sleep 20
echo "$(date +%T) hog $gb GiB $kind start" >> "$out"
HOG_STEP=${HOG_STEP:-} ./work/probe hog "$gb" "$hold" $kind 2>> out/mem-$label.hog.log &
hog=$!
while kill -0 $hog 2>/dev/null; do
	if [ "$(sysctl -n kern.memorystatus_vm_pressure_level)" = 4 ]; then
		echo "$(date +%T) critical: hog stopped" >> "$out"
		kill $hog
		break
	fi
	memory_pressure -Q | sed -n 's/.*percentage: /mp /p' | sed "s/^/$(date +%T) /" >> out/mem-$label.mp.log
	sleep 1
done
wait $hog 2>/dev/null
echo "$(date +%T) hog end" >> "$out"
sleep 30
kill $watch
