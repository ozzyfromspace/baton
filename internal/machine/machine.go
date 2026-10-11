// Package machine reads how big this machine is and how busy it is, for sizing the fleet of workers
// (`workers: auto`). It needs no cgo: macOS through sysctl, Linux through /proc and the cgroup v2 files a
// container runs under, and elsewhere only the CPU count. What each reading means, and why, is in
// docs/research/workers.md, "Machine readings and leftovers" (spike 27):
//
//   - CPUs is the smaller of runtime.NumCPU and runtime.GOMAXPROCS(0): only GOMAXPROCS follows a container's
//     cpu.max.
//   - On macOS, kern.memorystatus_level counts what macOS could still compress or swap out as free, and macOS
//     holds it near 36% by swapping, so it is only shown; the kernel's pressure level and how fast swap grows
//     tell a tight machine from a busy one.
//   - On Linux, MemAvailable is the kernel's estimate of what can be used without swapping. In a container,
//     /proc shows the whole VM, so the cgroup's memory.max and memory.current cap it.
package machine

import "runtime"

// Pressure is macOS's own verdict on memory (kern.memorystatus_vm_pressure_level), the values of the
// dispatch memory-pressure source. 0 means the platform does not report one.
type Pressure int

const (
	PressureNormal   Pressure = 1
	PressureWarn     Pressure = 2
	PressureCritical Pressure = 4
)

func (p Pressure) String() string {
	switch p {
	case PressureNormal:
		return "normal"
	case PressureWarn:
		return "warn"
	case PressureCritical:
		return "critical"
	}
	return ""
}

// Reading is one look at the machine. CPUs is always set; MemTotal is 0 when the platform does not report
// it. The live readings (Load1, MemAvailablePct, Pressure, SwapUsed) mean something only when Known.
type Reading struct {
	CPUs     int
	MemTotal uint64 // bytes; on Linux capped by the cgroup's memory.max
	// MemAvailablePct is how much of MemTotal is free, 0 to 100. On Linux it is MemAvailable (or the
	// cgroup's room, when smaller) and a floor on it makes sense. On macOS it is kern.memorystatus_level,
	// which macOS defends by swapping: shown, never gated on (Pressure and SwapUsed are).
	MemAvailablePct int
	Pressure        Pressure // macOS only
	SwapUsed        uint64   // macOS only, bytes (vm.swapusage)
	Load1           float64  // the one-minute load average
	Known           bool     // the live readings were read
}

// MemGiB is MemTotal in GiB, to the nearest: Linux's MemTotal leaves out what the kernel keeps for itself,
// so an 8 GB machine reports about 7.6 GiB and still counts as 8.
func (r Reading) MemGiB() int {
	return int((r.MemTotal + 1<<29) >> 30)
}

// Read looks at the machine now.
func Read() Reading {
	r := Reading{CPUs: cpus()}
	r.Known = read(&r) == nil
	return r
}

func cpus() int {
	return min(runtime.NumCPU(), runtime.GOMAXPROCS(0))
}
