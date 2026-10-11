package main

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

// platform reads macOS through sysctl, which x/sys calls through libSystem: no cgo.
func platform(r *Reading) error {
	// struct loadavg { fixpt_t ldavg[3]; long fscale; }: three uint32, padding, then an int64 at offset 16.
	b, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return fmt.Errorf("vm.loadavg: %w", err)
	}
	r.LoadRaw = fmt.Sprintf("%x", b)
	if len(b) < 24 {
		return fmt.Errorf("vm.loadavg: %d bytes", len(b))
	}
	scale := float64(binary.LittleEndian.Uint64(b[16:24]))
	r.Load1 = float64(binary.LittleEndian.Uint32(b[0:4])) / scale
	r.Load5 = float64(binary.LittleEndian.Uint32(b[4:8])) / scale
	r.Load15 = float64(binary.LittleEndian.Uint32(b[8:12])) / scale

	if r.MemTotal, err = unix.SysctlUint64("hw.memsize"); err != nil {
		return fmt.Errorf("hw.memsize: %w", err)
	}
	level, err := unix.SysctlUint32("kern.memorystatus_level")
	if err != nil {
		return fmt.Errorf("kern.memorystatus_level: %w", err)
	}
	r.AvailPct = int(level)
	pressure, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level")
	if err != nil {
		return fmt.Errorf("kern.memorystatus_vm_pressure_level: %w", err)
	}
	r.Pressure = int(pressure)
	// struct xsw_usage { u_int64_t xsu_total, xsu_avail, xsu_used; u_int32_t xsu_pagesize; boolean_t xsu_encrypted; }
	if s, err := unix.SysctlRaw("vm.swapusage"); err == nil && len(s) >= 24 {
		r.SwapUsed = binary.LittleEndian.Uint64(s[16:24])
	}
	return nil
}
