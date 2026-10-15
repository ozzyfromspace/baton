package procs

// Usage is the CPU time each process of a tree has used so far, by pid, in the system's own units
// (nanoseconds or Mach ticks on macOS, clock ticks on Linux): a reading compares only with another
// reading of the same machine. A tree that ran at all between two readings reads differently in the
// second: a process used more CPU, started, or ended.
type Usage map[int]uint64

// TreeUsage reads the CPU time used so far by every process of process group pgid, and by every process
// descended from one of them (a test harness may start a group of its own), as they are now. An empty
// Usage means none of them is running. A process of the tree whose time cannot be read makes it an
// error, never a reading that leaves it out. ErrUnsupported where baton reads no process table.
func TreeUsage(pgid int) (Usage, error) { return treeUsage(pgid) }

// row is a process as the table gives it, for finding a tree: cpu is filled in where the table carries
// it (Linux), and read for the tree's processes alone elsewhere (macOS).
type row struct {
	pid, ppid, pgid int
	cpu             uint64
}

// tree picks from all the processes of group pgid, and those descended from one of them, in table order.
func tree(all []row, pgid int) []row {
	if pgid <= 1 {
		return nil // 0 and 1 are no job's group: every process descends from init
	}
	in := map[int]bool{}
	children := map[int][]int{}
	var queue []int
	for _, r := range all {
		children[r.ppid] = append(children[r.ppid], r.pid)
		if r.pgid == pgid {
			in[r.pid] = true
			queue = append(queue, r.pid)
		}
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range children[pid] {
			if !in[c] {
				in[c] = true
				queue = append(queue, c)
			}
		}
	}
	var out []row
	for _, r := range all {
		if in[r.pid] {
			out = append(out, r)
		}
	}
	return out
}
