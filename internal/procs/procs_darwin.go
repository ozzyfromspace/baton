package procs

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// zombie is a kinfo_proc's p_stat for a process that ended and was not reaped yet (SZOMB).
const zombie = 5

// list reads the table through sysctl, which x/sys calls through libSystem: no cgo.
func list() ([]Proc, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil, fmt.Errorf("kern.proc.uid: %w", err)
	}
	self := os.Getpid()
	out := make([]Proc, 0, len(ks))
	for _, k := range ks {
		pid := int(k.Proc.P_pid)
		if pid <= 0 || pid == self || k.Proc.P_stat == zombie {
			continue
		}
		p := Proc{PID: pid, PPID: int(k.Eproc.Ppid), PGID: int(k.Eproc.Pgid), Comm: cstr(k.Proc.P_comm[:])}
		if b, err := unix.SysctlRaw("kern.procargs2", pid); err == nil {
			if env, err := procargsEnv(b); err == nil && len(env) > 0 { // empty: a platform binary's, hidden
				p.Env = batonEnv(env)
			}
		}
		p.Cwd, _ = cwd(pid)
		out = append(out, p)
	}
	return out, nil
}

// cwd asks proc_info for PROC_PIDVNODEPATHINFO, as lsof does: a struct vnode_info (152 bytes) then the
// working directory's path (MAXPATHLEN), and the same again for the root directory.
func cwd(pid int) (string, error) {
	const (
		callPIDInfo     = 2 // PROC_INFO_CALL_PIDINFO
		flavorVnodePath = 9 // PROC_PIDVNODEPATHINFO
		vnodeInfoSize   = 152
		maxPath         = 1024
		size            = 2 * (vnodeInfoSize + maxPath)
	)
	buf := make([]byte, size)
	n, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, callPIDInfo, uintptr(pid), flavorVnodePath, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return "", errno
	}
	if int(n) != size {
		return "", fmt.Errorf("proc_info: %d bytes, want %d", n, size)
	}
	return cstr(buf[vnodeInfoSize : vnodeInfoSize+maxPath]), nil
}

// parent reads pid's kinfo_proc alone (kern.proc.pid).
func parent(pid int) (int, string, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, "", fmt.Errorf("kern.proc.pid %d: %w", pid, err)
	}
	if int(k.Proc.P_pid) != pid {
		return 0, "", fmt.Errorf("kern.proc.pid %d: no such process", pid)
	}
	return int(k.Eproc.Ppid), cstr(k.Proc.P_comm[:]), nil
}
