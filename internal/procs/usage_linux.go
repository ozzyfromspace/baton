package procs

import (
	"os"
	"time"
)

func treeUsage(pgid int) (Usage, error) {
	rows, err := rowsFromProc(os.DirFS("/"))
	if err != nil {
		return nil, err
	}
	u := Usage{}
	for _, r := range tree(rows, pgid) {
		u[r.pid] = r.cpu
	}
	return u, nil
}

// cpuTime converts clock ticks, /proc's unit for a process's time: USER_HZ, 100 a second on every Linux
// baton runs on.
func cpuTime(n uint64) time.Duration { return time.Duration(n) * 10 * time.Millisecond }
