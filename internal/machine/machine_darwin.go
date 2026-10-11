package machine

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// read fills r through sysctl, which x/sys calls through libSystem: no cgo.
func read(r *Reading) error {
	var err error
	if r.MemTotal, err = unix.SysctlUint64("hw.memsize"); err != nil {
		return fmt.Errorf("hw.memsize: %w", err)
	}
	b, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return fmt.Errorf("vm.loadavg: %w", err)
	}
	if r.Load1, err = decodeLoadavg(b); err != nil {
		return err
	}
	level, err := unix.SysctlUint32("kern.memorystatus_level")
	if err != nil {
		return fmt.Errorf("kern.memorystatus_level: %w", err)
	}
	r.MemAvailablePct = int(level)
	pressure, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level")
	if err != nil {
		return fmt.Errorf("kern.memorystatus_vm_pressure_level: %w", err)
	}
	r.Pressure = Pressure(pressure)
	// Without swap the sysctl still answers, with zeros; if it fails, no growth is seen.
	if b, err := unix.SysctlRaw("vm.swapusage"); err == nil {
		r.SwapUsed, _ = decodeSwapUsed(b)
	}
	return nil
}
