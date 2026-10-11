// Spike 27: what can a static Go binary (no cgo) read about the machine's size and how busy it is?
//
//	probe read                 one reading, as JSON
//	probe watch EVERY FOR      a reading every EVERY for FOR (e.g. 2s 60s)
//	probe hog GB FOR [random]  hold GB gigabytes, every page touched, for FOR (HOG_STEP=4s: one GiB per step)
package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"
)

// Reading is what the probe saw; fields a platform can't read stay zero.
type Reading struct {
	T         string  `json:"t"`
	CPUs      int     `json:"cpus"`       // runtime.NumCPU
	MaxProcs  int     `json:"gomaxprocs"` // runtime.GOMAXPROCS(0): cgroup-aware since Go 1.25
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`
	MemTotal  uint64  `json:"mem_total"`
	AvailPct  int     `json:"avail_pct"`          // darwin: kern.memorystatus_level; linux: MemAvailable/MemTotal
	Pressure  int     `json:"pressure,omitempty"` // darwin: kern.memorystatus_vm_pressure_level (1 normal, 2 warn, 4 critical)
	SwapUsed  uint64  `json:"swap_used,omitempty"`
	CgroupMem string  `json:"cgroup_mem,omitempty"` // linux: memory.max
	CgroupCPU string  `json:"cgroup_cpu,omitempty"` // linux: cpu.max
	LoadRaw   string  `json:"loadavg_raw,omitempty"`
	Err       string  `json:"err,omitempty"`
}

func read() Reading {
	r := Reading{T: time.Now().Format("15:04:05"), CPUs: runtime.NumCPU(), MaxProcs: runtime.GOMAXPROCS(0)}
	if err := platform(&r); err != nil {
		r.Err = err.Error()
	}
	return r
}

func emit(r Reading) {
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe read | watch EVERY FOR | hog GB FOR [random]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "read":
		emit(read())
	case "watch":
		every, _ := time.ParseDuration(os.Args[2])
		total, _ := time.ParseDuration(os.Args[3])
		for end := time.Now().Add(total); time.Now().Before(end); time.Sleep(every) {
			emit(read())
		}
	case "hog":
		var gb int
		fmt.Sscan(os.Args[2], &gb)
		hold, _ := time.ParseDuration(os.Args[3])
		random := len(os.Args) > 4 && os.Args[4] == "random"
		step, _ := time.ParseDuration(os.Getenv("HOG_STEP"))
		chunks := make([][]byte, 0, gb)
		for i := 0; i < gb; i++ {
			c := make([]byte, 1<<30)
			if random {
				rand.Read(c) // incompressible, so the compressor can't hide it
			} else {
				for j := 0; j < len(c); j += 4096 {
					c[j] = byte(j>>12) | 1
				}
			}
			chunks = append(chunks, c)
			fmt.Fprintf(os.Stderr, "%s hog: %d GiB\n", time.Now().Format("15:04:05"), i+1)
			time.Sleep(step)
		}
		time.Sleep(hold)
		runtime.KeepAlive(chunks)
	}
}
