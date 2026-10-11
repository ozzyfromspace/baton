#!/bin/sh
# A stand-in .baton-lane. It records each call (its subcommand) in $LANE_CALLS, and what it saw of the
# environment in $LANE_CALLS.env. LANE_SLOTS is its answer to slots; up fails its first LANE_UP_FAILS
# times.
calls=${LANE_CALLS:?}
echo "$1" >>"$calls"
case $1 in
slots)
	echo "${LANE_SLOTS:-1}"
	;;
up)
	n=$(($(cat "$calls.up" 2>/dev/null || echo 0) + 1))
	echo "$n" >"$calls.up"
	if [ "$n" -le "${LANE_UP_FAILS:-0}" ]; then
		echo "up failed"
		exit 1
	fi
	echo "up in $2"
	;;
run)
	env | grep -E '^(BATON|GIT|CLAUDE)' | sort >"$calls.env"
	cd "$2" || exit 125
	sh -c "$3"
	;;
down)
	echo "down in $2"
	;;
esac
