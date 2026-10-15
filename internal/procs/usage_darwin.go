package procs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// treeUsage finds the tree in the whole process table (a process of the tree may run as another user,
// whose time cannot be read: then there is no reading), and reads each one's time from proc_info.
func treeUsage(pgid int) (Usage, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("kern.proc.all: %w", err)
	}
	rows := make([]row, 0, len(ks))
	for _, k := range ks {
		if pid := int(k.Proc.P_pid); pid > 0 && k.Proc.P_stat != zombie {
			rows = append(rows, row{pid: pid, ppid: int(k.Eproc.Ppid), pgid: int(k.Eproc.Pgid)})
		}
	}
	u := Usage{}
	for _, r := range tree(rows, pgid) {
		cpu, err := taskTime(r.pid)
		switch {
		case errors.Is(err, unix.ESRCH):
			continue // it ended meanwhile
		case err != nil:
			return nil, fmt.Errorf("the CPU time of pid %d: %w", r.pid, err)
		}
		u[r.pid] = cpu
	}
	return u, nil
}

// taskTime asks proc_info for PROC_PIDTASKINFO, a struct proc_taskinfo (96 bytes): its third and fourth
// fields are the task's user and system time, of its live threads and its ended ones, in Mach absolute
// time units.
func taskTime(pid int) (uint64, error) {
	const (
		callPIDInfo    = 2 // PROC_INFO_CALL_PIDINFO
		flavorTaskInfo = 4 // PROC_PIDTASKINFO
		size           = 96
	)
	var buf [size]byte
	n, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, callPIDInfo, uintptr(pid), flavorTaskInfo, 0,
		uintptr(unsafe.Pointer(&buf[0])), size)
	if errno != 0 {
		return 0, errno
	}
	if int(n) != size {
		return 0, fmt.Errorf("proc_info: %d bytes, want %d", n, size)
	}
	return binary.LittleEndian.Uint64(buf[16:]) + binary.LittleEndian.Uint64(buf[24:]), nil
}
