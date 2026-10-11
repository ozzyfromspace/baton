// Spike 28: can a static Go binary (no cgo, no ps or lsof) find the processes a hosted session left, and
// read the marker in their environment and their working directory?
//
//	procs show PID...   what can be read of each process, as JSON
//	procs scan [DIR]    every process of this user whose environment has BATON_INSTANCE (and BATON_DIR=DIR)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Proc is one process as the reader saw it; an error names what could not be read.
type Proc struct {
	PID   int               `json:"pid"`
	Alive bool              `json:"alive"`
	PPID  int               `json:"ppid,omitempty"`
	PGID  int               `json:"pgid,omitempty"`
	SID   int               `json:"sid,omitempty"`
	UID   int               `json:"uid"`
	Comm  string            `json:"comm,omitempty"`
	Cwd   string            `json:"cwd,omitempty"`
	Env   map[string]string `json:"baton_env,omitempty"` // only BATON_* variables
	Errs  []string          `json:"errs,omitempty"`
}

func batonEnv(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "BATON_") {
			m[k] = v
		}
	}
	return m
}

func show(pid int) Proc {
	p := Proc{PID: pid, UID: -1}
	if err := table(&p); err != nil {
		p.Errs = append(p.Errs, "table: "+err.Error())
	}
	if !p.Alive {
		return p
	}
	if env, err := environ(pid); err != nil {
		p.Errs = append(p.Errs, "env: "+err.Error())
	} else {
		p.Env = batonEnv(env)
	}
	if cwd, err := cwd(pid); err != nil {
		p.Errs = append(p.Errs, "cwd: "+err.Error())
	} else {
		p.Cwd = cwd
	}
	return p
}

func main() {
	out := json.NewEncoder(os.Stdout)
	switch os.Args[1] {
	case "show":
		for _, a := range os.Args[2:] {
			pid, _ := strconv.Atoi(a)
			out.Encode(show(pid))
		}
	case "count": // how many arguments and environment strings kern.procargs2 gives for each pid
		for _, a := range os.Args[2:] {
			pid, _ := strconv.Atoi(a)
			env, err := environ(pid)
			fmt.Printf("%d: %d environment strings, err %v\n", pid, len(env), err)
		}
	case "scan":
		pids, err := all()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		unreadable := 0
		for _, pid := range pids {
			env, err := environ(pid)
			if err != nil {
				unreadable++
				p := Proc{PID: pid}
				table(&p)
				fmt.Fprintf(os.Stderr, "scan: pid %d (%s): %v\n", pid, p.Comm, err)
				continue
			}
			if m := batonEnv(env); m["BATON_INSTANCE"] != "" && (len(os.Args) < 3 || m["BATON_DIR"] == os.Args[2]) {
				out.Encode(show(pid))
			}
		}
		fmt.Fprintf(os.Stderr, "scan: %d processes of uid %d, %d environments unreadable\n", len(pids), os.Getuid(), unreadable)
	}
}
