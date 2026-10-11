package machine

import (
	"encoding/hex"
	"fmt"
	"runtime"
	"testing"
	"testing/fstest"
)

const gib = 1 << 30

// Recorded on an arm64 Mac (macOS 26.3), next to what sysctl printed for the same sample.
const (
	loadavgBlob = "bd40000099370000b9340000000000000008000000000000"                 // vm.loadavg: { 8.09 6.95 6.59 }
	swapBlob    = "00000000040000000000cf4f00000000000031b0030000000040000001000000" // used = 15107.06M  (encrypted)
)

func blob(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeLoadavg(t *testing.T) {
	b := blob(t, loadavgBlob)
	zeroScale := append([]byte{}, b[:16]...)
	zeroScale = append(zeroScale, make([]byte, 8)...)
	for _, c := range []struct {
		name string
		b    []byte
		want string // as sysctl prints it
		err  bool
	}{
		{"recorded", b, "8.09", false},
		{"short", b[:23], "", true},
		{"no scale", zeroScale, "", true},
	} {
		got, err := decodeLoadavg(c.b)
		if (err != nil) != c.err || (err == nil && fmt.Sprintf("%.2f", got) != c.want) {
			t.Errorf("%s: %v, %v; want %s (error %v)", c.name, got, err, c.want, c.err)
		}
	}
}

func TestDecodeSwapUsed(t *testing.T) {
	b := blob(t, swapBlob)
	if got, err := decodeSwapUsed(b); err != nil || fmt.Sprintf("%.2fM", float64(got)/(1<<20)) != "15107.06M" {
		t.Errorf("recorded: %d, %v; want 15107.06M", got, err)
	}
	if _, err := decodeSwapUsed(b[:23]); err == nil {
		t.Error("short: no error")
	}
}

func TestFromProc(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	meminfo := func(totalKB, availKB uint64) *fstest.MapFile {
		return file(fmt.Sprintf("MemTotal:       %d kB\nMemFree:         1048576 kB\nMemAvailable:   %d kB\nBuffers:          123456 kB\n", totalKB, availKB))
	}
	loadavg := file("2.74 2.71 2.90 3/784 10\n")
	// A 23.4 GiB VM, 56% of it available, as a container saw it in spike 27.
	vm := fstest.MapFS{"proc/meminfo": meminfo(24534300, 13739208), "proc/loadavg": loadavg}
	with := func(base fstest.MapFS, extra map[string]string) fstest.MapFS {
		m := fstest.MapFS{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = file(v)
		}
		return m
	}
	for _, c := range []struct {
		name  string
		fsys  fstest.MapFS
		total uint64
		pct   int
		err   bool
	}{
		{"a machine, no limit on its user's slice", with(vm, map[string]string{
			"proc/self/cgroup":                    "0::/user.slice/user-1000.slice/session-2.scope\n",
			"sys/fs/cgroup/user.slice/memory.max": "max\n",
		}), 24534300 << 10, 56, false},
		{"no cgroup files at all", vm, 24534300 << 10, 56, false},
		{"a container without limits", with(vm, map[string]string{
			"proc/self/cgroup": "0::/\n", "sys/fs/cgroup/memory.max": "max\n", "sys/fs/cgroup/memory.current": "123456789\n",
		}), 24534300 << 10, 56, false},
		// docker run --memory=2g, with 1.08 GB in use: /proc still says 56% of the VM is free.
		{"a container limited to 2 GiB", with(vm, map[string]string{
			"proc/self/cgroup": "0::/\n", "sys/fs/cgroup/memory.max": "2147483648\n", "sys/fs/cgroup/memory.current": "1080000000\n",
		}), 2 * gib, 49, false},
		{"a limit on a parent cgroup", with(vm, map[string]string{
			"proc/self/cgroup":           "0::/a/b\n",
			"sys/fs/cgroup/a/memory.max": "4294967296", "sys/fs/cgroup/a/memory.current": "3221225472",
			"sys/fs/cgroup/a/b/memory.max": "max", "sys/fs/cgroup/a/b/memory.current": "1073741824",
		}), 4 * gib, 25, false},
		// The parent's limit is looser, but it has less room left: each is taken from where it is tightest.
		{"the tightest of two limits", with(vm, map[string]string{
			"proc/self/cgroup":           "0::/a/b\n",
			"sys/fs/cgroup/a/memory.max": "8589934592", "sys/fs/cgroup/a/memory.current": "7516192768",
			"sys/fs/cgroup/a/b/memory.max": "4294967296", "sys/fs/cgroup/a/b/memory.current": "1073741824",
		}), 4 * gib, 25, false},
		{"more in use than the limit", with(vm, map[string]string{
			"proc/self/cgroup": "0::/\n", "sys/fs/cgroup/memory.max": "1073741824", "sys/fs/cgroup/memory.current": "1181116006",
		}), gib, 0, false},
		// A process outside its cgroup namespace's root sees a path that climbs out: only the root is read.
		{"a cgroup path that climbs out", with(vm, map[string]string{
			"proc/self/cgroup": "0::/../..\n", "sys/fs/cgroup/memory.max": "2147483648", "sys/fs/cgroup/memory.current": "1073741824",
		}), 2 * gib, 50, false},
		{"cgroup v1 only", with(vm, map[string]string{
			"proc/self/cgroup": "12:memory:/docker/abc\n1:name=systemd:/docker/abc\n",
		}), 24534300 << 10, 56, false},
		{"before Linux 3.14: no MemAvailable", fstest.MapFS{
			"proc/meminfo": file("MemTotal:       8000000 kB\nMemFree:  10000 kB\n"), "proc/loadavg": loadavg,
		}, 8000000 << 10, 0, true},
		{"no load average", fstest.MapFS{"proc/meminfo": meminfo(8000000, 4000000)}, 8000000 << 10, 0, true},
		{"a garbled load average", fstest.MapFS{"proc/meminfo": meminfo(8000000, 4000000), "proc/loadavg": file("x 1 2\n")}, 8000000 << 10, 0, true},
		{"no /proc", fstest.MapFS{}, 0, 0, true},
	} {
		var r Reading
		err := fromProc(c.fsys, &r)
		if (err != nil) != c.err || r.MemTotal != c.total || r.MemAvailablePct != c.pct {
			t.Errorf("%s: total %d, %d%% free, error %v; want %d, %d%%, error %v", c.name, r.MemTotal, r.MemAvailablePct, err, c.total, c.pct, c.err)
		}
		if !c.err && r.Load1 != 2.74 {
			t.Errorf("%s: load %v, want 2.74", c.name, r.Load1)
		}
	}
}

func TestMemGiB(t *testing.T) {
	for _, c := range []struct {
		total uint64
		want  int
	}{
		{48 * gib, 48},     // this Mac's hw.memsize
		{8000000 << 10, 8}, // an 8 GB Linux machine: MemTotal leaves out what the kernel keeps
		{24534300 << 10, 23},
		{gib / 2, 1},
		{gib/2 - 1, 0},
		{0, 0},
	} {
		if got := (Reading{MemTotal: c.total}).MemGiB(); got != c.want {
			t.Errorf("%d bytes: %d GiB, want %d", c.total, got, c.want)
		}
	}
}

func TestPressureString(t *testing.T) {
	for p, want := range map[Pressure]string{0: "", PressureNormal: "normal", PressureWarn: "warn", PressureCritical: "critical", 3: ""} {
		if got := p.String(); got != want {
			t.Errorf("%d: %q, want %q", p, got, want)
		}
	}
}

// TestRead reads the machine the test runs on, so it checks only that the values are sane.
func TestRead(t *testing.T) {
	r := Read()
	if r.CPUs < 1 || r.CPUs > runtime.NumCPU() {
		t.Errorf("CPUs %d (NumCPU %d)", r.CPUs, runtime.NumCPU())
	}
	switch runtime.GOOS {
	case "darwin", "linux":
		if !r.Known {
			t.Fatalf("not known: %+v", r)
		}
		if r.MemTotal < gib/2 || r.MemAvailablePct < 0 || r.MemAvailablePct > 100 || r.Load1 < 0 {
			t.Errorf("not sane: %+v", r)
		}
		if runtime.GOOS == "darwin" && r.Pressure.String() == "" {
			t.Errorf("pressure %d", r.Pressure)
		}
		if runtime.GOOS == "linux" && (r.Pressure != 0 || r.SwapUsed != 0) {
			t.Errorf("macOS-only readings on Linux: %+v", r)
		}
	default:
		if r.Known || r.MemTotal != 0 {
			t.Errorf("live readings where there are none: %+v", r)
		}
	}
	t.Logf("%+v (%d GiB, pressure %s)", r, r.MemGiB(), r.Pressure)
}
