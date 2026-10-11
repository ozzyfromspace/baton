package machine

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"strconv"
	"strings"
)

// Linux's readings, from fsys rooted at / (os.DirFS("/") on Linux), here rather than in machine_linux.go so
// they are tested on every platform.

// fromProc fills r from /proc/meminfo and /proc/loadavg, capped by the cgroup v2 memory limits the process
// runs under.
func fromProc(fsys fs.FS, r *Reading) error {
	total, avail, memErr := meminfo(fsys)
	if limit, room, ok := cgroupMemory(fsys); ok {
		total, avail = min(total, limit), min(avail, room)
	}
	r.MemTotal = total
	if memErr != nil {
		return memErr
	}
	if total == 0 {
		return errors.New("machine: no memory to go on")
	}
	b, err := fs.ReadFile(fsys, "proc/loadavg")
	if err != nil {
		return err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return errors.New("/proc/loadavg: empty")
	}
	if r.Load1, err = strconv.ParseFloat(f[0], 64); err != nil {
		return fmt.Errorf("/proc/loadavg: %w", err)
	}
	r.MemAvailablePct = int(avail * 100 / total)
	return nil
}

// meminfo returns MemTotal and MemAvailable in bytes. MemTotal is returned even when MemAvailable is
// missing (kernels before 3.14).
func meminfo(fsys fs.FS) (total, avail uint64, err error) {
	b, err := fs.ReadFile(fsys, "proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	haveAvail := false
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line) // "MemTotal:       24534300 kB"
		if len(f) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = kb << 10
		case "MemAvailable:":
			avail, haveAvail = kb<<10, true
		}
	}
	switch {
	case total == 0:
		return 0, 0, errors.New("/proc/meminfo: no MemTotal")
	case !haveAvail:
		return total, 0, errors.New("/proc/meminfo: no MemAvailable")
	}
	return total, avail, nil
}

// cgroupMemory returns the tightest cgroup v2 memory limit on the process (memory.max) and the least room
// left under any of them (memory.max − memory.current), walking up from the process's own cgroup to the
// root of the hierarchy; ok is false when no level sets a limit. Inside a container with a cgroup
// namespace, the process's cgroup is the root.
func cgroupMemory(fsys fs.FS) (limit, room uint64, ok bool) {
	const root = "sys/fs/cgroup"
	dir := root
	if b, err := fs.ReadFile(fsys, "proc/self/cgroup"); err == nil {
		for line := range strings.Lines(string(b)) {
			if p, found := strings.CutPrefix(strings.TrimSpace(line), "0::"); found {
				if d := path.Join(root, p); strings.HasPrefix(d+"/", root+"/") {
					dir = d
				}
			}
		}
	}
	limit, room = math.MaxUint64, math.MaxUint64
	for {
		if most, err := readUint(fsys, dir+"/memory.max"); err == nil { // "max" means no limit
			used, _ := readUint(fsys, dir+"/memory.current")
			limit, room, ok = min(limit, most), min(room, most-min(used, most)), true
		}
		if dir == root {
			return limit, room, ok
		}
		dir = path.Dir(dir)
	}
}

func readUint(fsys fs.FS, name string) (uint64, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}
