# 27: reading the machine (macOS 26.3 arm64; Linux in a container; Go 1.27, 2026-10-11)

Question: what can a static Go binary read about how big the machine is and how busy it is? The constraints
are no cgo and no new module (golang.org/x/sys only). This is for `workers: auto`, which sizes the fleet by the
machine and holds new workers while the machine is busy. Is "at least 15% of memory free" a sensible floor?

Harness:

- **The probe.** `main.go`, `read_darwin.go` and `read_linux.go` make a probe in a module of its own:
  - `probe read` prints one reading as JSON;
  - `probe watch EVERY FOR` samples;
  - `probe hog GB FOR [random]` holds memory with every page touched. With `random` the memory is
    incompressible; `HOG_STEP=4s` adds a GiB per step.
- **`memory.sh LABEL GB HOLD [random]`** samples every 2 s, from 20 s before a hog until 30 s after it. A
  watchdog ends the hog at once if macOS reports critical pressure; it never fired.
- **The build.** `GOFLAGS=-mod=mod CGO_ENABLED=0 go build -o work/probe .`, plus
  `GOOS=linux GOARCH=arm64` for `work/probe-linux`.
- **The Linux runs** used `python:3.13-alpine` in Docker's Linux VM: arm64, 14 CPUs, 23.4 GiB.
- **The Mac** has 14 CPUs (10 performance and 4 efficiency cores) and 48 GiB. A lead and a worker session ran
  throughout, and the Linux VM used about 3 CPUs; the load was 5.3 to 7.1.

## Q1: load and size on macOS

- **`vm.loadavg`.** `unix.SysctlRaw` returns 24 bytes: three `uint32` fixed-point values, 4 bytes of padding,
  then an `int64` `fscale` at offset 16 (2048 here). The probe decoded `5.81 5.93 5.92`, and
  `sysctl vm.loadavg` printed `{ 5.81 5.93 5.92 }`.
- **Size.** `unix.SysctlUint64("hw.memsize")` gave 51539607552 (48 GiB), and `runtime.NumCPU()` gave 14, the
  same as `hw.ncpu`.
- macOS's load counts runnable threads; unlike Linux's, it does not count threads waiting on disk.

## Q2: memory on macOS

- **`kern.memorystatus_level`** (`unix.SysctlUint32`) equalled `memory_pressure`'s "System-wide memory free
  percentage" in every sample.
  - It is not free memory. It counts the pages macOS could still compress or swap out as available, active
    pages included. It read 44–47% while 1.4 M pages (21 GiB) sat in the compressor.
- **`kern.memorystatus_vm_pressure_level`** is macOS's own verdict: 1 normal, 2 warn, 4 critical, the values
  of the dispatch memory-pressure source.

A process holding incompressible memory:

| hog | level before → during → after | pressure before → during → after | swap used |
|---|---|---|---|
| 4 GiB at once | 45 → 36–37 → 46–47 | warn → warn → warn | 2.45 → 4.23 GiB |
| 8 GiB at once | 47–48 → 36–38 → 55–57 | warn → warn → **normal** | 4.23 → 8.60 GiB |
| 12 GiB, 1 GiB every 4 s | 67 → 52 (6 GiB) → 43 (7 GiB) → 36–37 (8–12 GiB) → 62–66 | normal → normal → **warn** → warn → normal | 8.52 → 14.81 GiB |

- **macOS defends the level by swapping.** It swaps idle memory out to make room, so the level falls much
  less than what the process holds. 8 GiB took it no lower than 4 GiB did, about 10 points. Afterwards it
  came back higher than before (55–57%, then 66%), and pressure went to normal, because the pages swapped out
  stay out.
- **In the stepped run,** pressure turned to warn between 52% and 43%. From 8 GiB on, the level stopped
  falling at 36–37%, and swap took everything more: 6.3 GiB in 50 seconds. Critical never came.
  `memory_pressure`, sampled every second alongside the probe, gave the same percentages.
- **15% was never near.** Warn held from 36% to 48%, and normal came back at 55%. Before each test, with the
  machine at warn and nothing growing, swap stayed level through the 20 s of baseline samples.
- So neither the level nor the pressure level tells a machine that is a little tight from one swapping
  gigabytes a minute. Swap growing (`vm.swapusage`, `xsu_used` at offset 16) does.

## Q3: Linux

- **`/proc/loadavg`** read `2.74 2.71 2.90 3/784 10`. **`/proc/meminfo`'s `MemAvailable`** follows what a
  process holds: a 4 GiB hog took it from 60% to 46% of 23.4 GiB, and back to 59%. It is the kernel's
  estimate of what can be used without swapping, so a 15% floor makes sense on Linux.
- **A container sees the VM's numbers.** In a container (cgroup v2), `/proc` shows the VM's total memory and
  load:

| `docker run` | `NumCPU()` | `GOMAXPROCS(0)` | `/proc` MemTotal | `memory.max` | `cpu.max` |
|---|---|---|---|---|---|
| no limits | 14 | 14 | 23.4 GiB | `max` | `max 100000` |
| `--cpus=2 --memory=2g` | **14** | 2 | **23.4 GiB** | 2147483648 | `200000 100000` |
| `--cpuset-cpus=0-3 --memory=4g` | 4 | 4 | **23.4 GiB** | 4294967296 | `max 100000` |

- `GOMAXPROCS(0)` follows `cpu.max` (Go 1.25 and later); `NumCPU()` follows only the CPU set.
- With 1 GiB held in a 2 GiB container, `memory.current` read 1.08 GB, while `MemAvailable` still said 56% of
  the VM was free.

## Verdict: what `workers: auto` reads

- **CPUs:** `min(runtime.NumCPU(), runtime.GOMAXPROCS(0))` on every platform.
- **Memory size:**
  - macOS: `hw.memsize`;
  - Linux: `MemTotal`, capped by the cgroup's `memory.max` when one is set.
- **Room for another worker:**
  - **load:** `Load1 < CPUs`;
  - **macOS:** two tests.
    - Pressure is below critical (`kern.memorystatus_vm_pressure_level` under 4).
    - While pressure is warn, swap has not grown by more than 256 MiB since the reading a minute earlier.
      Swap stood still while the machine idled at warn, and grew by gigabytes a minute while something
      held memory.

    The level is only shown, since a 15% floor on it would never trip. Holding at every warn would hold
    workers whenever this machine had two sessions and a VM open, with no memory actually short.
  - **Linux:** `MemAvailable` at least 15% of the total. With a cgroup limit, the available memory is the
    smaller of `MemAvailable` and `memory.max − memory.current`.
- **Elsewhere (Windows):** only the CPU count is known, and there is no live check.
