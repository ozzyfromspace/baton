package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// table reads /proc/<pid>/stat (after the command name, which may hold spaces) and /proc/<pid>/status.
func table(p *Proc) error {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.PID))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	p.Alive = true
	open, close := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	p.Comm = string(b[open+1 : close])
	// state ppid pgrp session ...
	f := strings.Fields(string(b[close+2:]))
	p.PPID, _ = strconv.Atoi(f[1])
	p.PGID, _ = strconv.Atoi(f[2])
	p.SID, _ = strconv.Atoi(f[3])
	if st, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", p.PID)); err == nil {
		for _, l := range strings.Split(string(st), "\n") {
			if strings.HasPrefix(l, "Uid:") {
				p.UID, _ = strconv.Atoi(strings.Fields(l)[1])
			}
		}
	}
	return nil
}

func all() ([]int, error) {
	ds, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, d := range ds {
		if pid, err := strconv.Atoi(d.Name()); err == nil {
			if fi, err := d.Info(); err == nil && fi.Sys() != nil && owner(fi) == os.Getuid() {
				pids = append(pids, pid)
			}
		}
	}
	return pids, nil
}

func environ(pid int) ([]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00"), nil
}

func cwd(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}
