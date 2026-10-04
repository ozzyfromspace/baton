PS1='spike-zsh%# '
# The spike directory is this file's parent's parent (ZDOTDIR=<spike>/zdot).
SPIKE_DIR="${ZDOTDIR:h}"
export ELEV_DIR="$SPIKE_DIR/.elev"
baton_precmd() {
  local f="$ELEV_DIR/${TTY##*/}.json"
  [[ -f $f ]] || return
  local sid=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["session_id"])' "$f")
  local pend=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["pending_file"])' "$f")
  mv "$f" "$f.consumed"
  print -r -- "[precmd] baton: relaunching session ${sid[1,8]} under baton"
  BATON_PENDING_FILE="$pend" python3 "$SPIKE_DIR/wrapper.py" --resume "$sid" --model haiku
}
precmd_functions+=(baton_precmd)
