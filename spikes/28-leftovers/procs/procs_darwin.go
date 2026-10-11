package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// table reads the process from kern.proc.pid: parent, group, owner, command name.
func table(p *Proc) error {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", p.PID)
	if err != nil {
		return err
	}
	if k.Proc.P_pid != int32(p.PID) {
		return nil // no such process: the kernel returns an empty record
	}
	p.Alive = true
	p.PPID, p.PGID = int(k.Eproc.Ppid), int(k.Eproc.Pgid)
	p.UID = int(k.Eproc.Ucred.Uid)
	p.Comm = cstr(k.Proc.P_comm[:])
	if sid, err := unix.Getsid(p.PID); err == nil {
		p.SID = sid
	}
	return nil
}

// all lists this user's processes.
func all() ([]int, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(ks))
	for _, k := range ks {
		pids = append(pids, int(k.Proc.P_pid))
	}
	return pids, nil
}

// environ reads kern.procargs2: argc, the executable's path, padding, argc arguments, then the environment,
// each NUL-terminated, up to an empty string.
func environ(pid int) ([]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, fmt.Errorf("%d bytes", len(b))
	}
	argc := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	i := bytes.IndexByte(b, 0) // the executable's path
	if i < 0 {
		return nil, fmt.Errorf("no executable path")
	}
	b = b[i:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	var env []string
	for n := 0; len(b) > 0; n++ {
		i := bytes.IndexByte(b, 0)
		if i <= 0 {
			break
		}
		if n >= argc {
			env = append(env, string(b[:i]))
		}
		b = b[i+1:]
	}
	return env, nil
}

// cwd asks proc_info for PROC_PIDVNODEPATHINFO, as lsof does; x/sys sends the call through libSystem.
func cwd(pid int) (string, error) {
	const (
		callPIDInfo       = 2
		flavorVnodePath   = 9
		vnodeInfoSize     = 152 // struct vnode_info
		maxPath           = 1024
		vnodePathInfoSize = 2 * (vnodeInfoSize + maxPath) // cdir, then rdir
	)
	buf := make([]byte, vnodePathInfoSize)
	n, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, callPIDInfo, uintptr(pid), flavorVnodePath, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return "", errno
	}
	if int(n) != len(buf) {
		return "", fmt.Errorf("proc_info returned %d bytes, want %d", n, len(buf))
	}
	return cstr(buf[vnodeInfoSize : vnodeInfoSize+maxPath]), nil
}

// cstr is the string up to the first NUL.
func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}
