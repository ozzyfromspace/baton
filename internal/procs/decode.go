package procs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// What the kernels give, decoded here rather than in the files for each system, so that recorded bytes
// and /proc fixtures are tested on every platform.

// procargsEnv reads macOS's kern.procargs2: argc as an int32, the executable's path, NUL padding, argc
// arguments, then the environment, each NUL-terminated, up to an empty string. macOS runs only on
// little-endian CPUs.
func procargsEnv(b []byte) ([]string, error) {
	if len(b) < 4 {
		return nil, fmt.Errorf("kern.procargs2: %d bytes", len(b))
	}
	argc := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	i := bytes.IndexByte(b, 0) // the end of the executable's path
	if i < 0 {
		return nil, errors.New("kern.procargs2: no executable path")
	}
	b = b[i:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	var env []string
	for n := 0; len(b) > 0; n++ {
		i := bytes.IndexByte(b, 0)
		if i == 0 && n >= argc {
			break // the empty string after the environment (an argument may be empty)
		}
		if i < 0 {
			i = len(b)
		}
		if n >= argc {
			env = append(env, string(b[:i]))
		}
		b = b[min(i+1, len(b)):]
	}
	return env, nil
}

// fromProc reads the processes of user uid from fsys, rooted at / (Linux's /proc), other than self and
// zombies.
func fromProc(fsys fs.FS, uid, self int) ([]Proc, error) {
	entries, err := fs.ReadDir(fsys, "proc")
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		dir := "proc/" + e.Name()
		if owner, err := statusUID(fsys, dir); err != nil || owner != uid {
			continue
		}
		b, err := fs.ReadFile(fsys, dir+"/stat")
		if err != nil {
			continue // it ended meanwhile
		}
		p, state, err := parseStat(b)
		if err != nil || state == 'Z' || state == 'X' {
			continue
		}
		p.PID = pid
		if b, err := fs.ReadFile(fsys, dir+"/environ"); err == nil {
			p.Env = batonEnv(strings.Split(strings.TrimRight(string(b), "\x00"), "\x00"))
		}
		if cwd, err := fs.ReadLink(fsys, dir+"/cwd"); err == nil {
			p.Cwd = strings.TrimSuffix(cwd, " (deleted)")
		}
		out = append(out, p)
	}
	return out, nil
}

// parentFromProc reads process pid of fsys, rooted at / (Linux's /proc): its parent and its name.
func parentFromProc(fsys fs.FS, pid int) (int, string, error) {
	b, err := fs.ReadFile(fsys, "proc/"+strconv.Itoa(pid)+"/stat")
	if err != nil {
		return 0, "", err
	}
	p, _, err := parseStat(b)
	if err != nil {
		return 0, "", err
	}
	return p.PPID, p.Comm, nil
}

// parseStat reads /proc/<pid>/stat: "pid (comm) state ppid pgrp session …". The command name may hold
// spaces and parentheses, so the fields after it are found from the last ')'.
func parseStat(b []byte) (Proc, byte, error) {
	open, end := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return Proc{}, 0, errors.New("stat: no command name")
	}
	f := strings.Fields(string(b[end+1:]))
	if len(f) < 3 || len(f[0]) != 1 {
		return Proc{}, 0, errors.New("stat: too short")
	}
	ppid, err1 := strconv.Atoi(f[1])
	pgid, err2 := strconv.Atoi(f[2])
	if err := errors.Join(err1, err2); err != nil {
		return Proc{}, 0, fmt.Errorf("stat: %w", err)
	}
	return Proc{PPID: ppid, PGID: pgid, Comm: string(b[open+1 : end])}, f[0][0], nil
}

// statusUID reads the effective uid from /proc/<pid>/status ("Uid: real effective saved fs").
func statusUID(fsys fs.FS, dir string) (int, error) {
	b, err := fs.ReadFile(fsys, dir+"/status")
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(string(b)) {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			if f := strings.Fields(rest); len(f) >= 2 {
				return strconv.Atoi(f[1])
			}
		}
	}
	return 0, errors.New("status: no Uid")
}

// rowsFromProc reads every process of fsys, rooted at / (Linux's /proc), for finding a tree: its parent,
// its group and the CPU time it has used (utime and stime, in clock ticks), other than zombies. One that
// ends as it is read is left out.
func rowsFromProc(fsys fs.FS) ([]row, error) {
	entries, err := fs.ReadDir(fsys, "proc")
	if err != nil {
		return nil, err
	}
	var out []row
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		b, err := fs.ReadFile(fsys, "proc/"+e.Name()+"/stat")
		if err != nil {
			continue // it ended meanwhile
		}
		p, state, err := parseStat(b)
		if err != nil || state == 'Z' || state == 'X' {
			continue
		}
		cpu, err := statCPU(b)
		if err != nil {
			return nil, fmt.Errorf("proc/%d/stat: %w", pid, err)
		}
		out = append(out, row{pid: pid, ppid: p.PPID, pgid: p.PGID, cpu: cpu})
	}
	return out, nil
}

// statCPU reads the CPU time a process has used from /proc/<pid>/stat: utime and stime, its 14th and
// 15th fields, the 12th and 13th after the command name.
func statCPU(b []byte) (uint64, error) {
	end := bytes.LastIndexByte(b, ')')
	if end < 0 {
		return 0, errors.New("no command name")
	}
	f := strings.Fields(string(b[end+1:]))
	if len(f) < 13 {
		return 0, errors.New("no CPU times")
	}
	utime, err1 := strconv.ParseUint(f[11], 10, 64)
	stime, err2 := strconv.ParseUint(f[12], 10, 64)
	if err := errors.Join(err1, err2); err != nil {
		return 0, err
	}
	return utime + stime, nil
}
