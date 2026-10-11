#!/bin/sh
python3 -c "import time,sys;print(round(time.time(),3), sys.argv[1])" "$1" >> "$(dirname "$0")/hooks-$BATON_SPIKE.log"
