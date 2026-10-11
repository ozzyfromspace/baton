package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// platform reads /proc, and the cgroup v2 limits a container runs under.
func platform(r *Reading) error {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return err
	}
	r.LoadRaw = strings.TrimSpace(string(b))
	if _, err := fmt.Sscan(r.LoadRaw, &r.Load1, &r.Load5, &r.Load15); err != nil {
		return fmt.Errorf("/proc/loadavg: %w", err)
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return err
	}
	defer f.Close()
	var total, avail uint64
	for s := bufio.NewScanner(f); s.Scan(); {
		var kb uint64
		switch {
		case strings.HasPrefix(s.Text(), "MemTotal:"):
			fmt.Sscan(strings.TrimPrefix(s.Text(), "MemTotal:"), &kb)
			total = kb << 10
		case strings.HasPrefix(s.Text(), "MemAvailable:"):
			fmt.Sscan(strings.TrimPrefix(s.Text(), "MemAvailable:"), &kb)
			avail = kb << 10
		}
	}
	r.MemTotal = total
	if total > 0 {
		r.AvailPct = int(avail * 100 / total)
	}
	if b, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		r.CgroupMem = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		r.CgroupCPU = strings.TrimSpace(string(b))
	}
	return nil
}
