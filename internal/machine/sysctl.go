package machine

import (
	"encoding/binary"
	"fmt"
)

// The structs macOS's sysctls return, decoded here rather than in machine_darwin.go so they are tested on
// every platform. macOS runs only on little-endian CPUs (arm64, amd64), and both lay these out alike.

// decodeLoadavg reads vm.loadavg, a struct loadavg { fixpt_t ldavg[3]; long fscale; }: three uint32
// fixed-point values, 4 bytes of padding, then fscale as an int64 at offset 16. It returns the one-minute
// value.
func decodeLoadavg(b []byte) (float64, error) {
	if len(b) < 24 {
		return 0, fmt.Errorf("vm.loadavg: %d bytes", len(b))
	}
	scale := binary.LittleEndian.Uint64(b[16:24])
	if scale == 0 {
		return 0, fmt.Errorf("vm.loadavg: fscale 0")
	}
	return float64(binary.LittleEndian.Uint32(b[0:4])) / float64(scale), nil
}

// decodeSwapUsed reads vm.swapusage, a struct xsw_usage { u_int64_t xsu_total, xsu_avail, xsu_used; … }: the
// bytes of swap in use are at offset 16.
func decodeSwapUsed(b []byte) (uint64, error) {
	if len(b) < 24 {
		return 0, fmt.Errorf("vm.swapusage: %d bytes", len(b))
	}
	return binary.LittleEndian.Uint64(b[16:24]), nil
}
