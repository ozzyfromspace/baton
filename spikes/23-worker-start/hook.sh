#!/bin/sh
# Spike hook: append {t, ev, pwd, cpd, input} as one JSON line to LOG.
# usage: hook.sh EVENT LOG
# env:   CODEWORD=x   SessionStart with source "startup" returns additionalContext "Code word: x"
#        HOOK_SLEEP=n SessionStart sleeps n seconds before answering (logs "SessionStart-done" after)
ev="$1"; log="$2"
in=$(cat | tr -d '\n')
now() { perl -MTime::HiRes=time -e 'printf("%.3f", time)'; }
printf '{"t":%s,"ev":"%s","pwd":"%s","cpd":"%s","input":%s}\n' "$(now)" "$ev" "$PWD" "$CLAUDE_PROJECT_DIR" "${in:-null}" >> "$log"
if [ "$ev" = SessionStart ]; then
  if [ -n "$HOOK_SLEEP" ]; then
    sleep "$HOOK_SLEEP"
    printf '{"t":%s,"ev":"SessionStart-done","input":null}\n' "$(now)" >> "$log"
  fi
  case "$in" in
    *'"source":"startup"'*|*'"source": "startup"'*)
      if [ -n "$CODEWORD" ]; then
        printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"Code word: %s"}}\n' "$CODEWORD"
      fi ;;
  esac
fi
exit 0
