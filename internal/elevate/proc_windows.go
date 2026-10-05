//go:build windows

package elevate

import (
	"errors"
	"os/exec"
	"time"
)

// Supported reports whether elevation works on this platform. Windows support is planned.
const Supported = false

func ProcTTY(string) string        { return "" }
func ProcArgs(string) []string     { return nil }
func Detach(*exec.Cmd) error       { return errors.New("not supported on Windows") }
func Stop(int, time.Duration) bool { return false }
