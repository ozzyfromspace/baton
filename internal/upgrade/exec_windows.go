package upgrade

import "errors"

// CanRestart is false on Windows, which has no exec: a session there keeps its version until restarted.
const CanRestart = false

const exeSuffix = true

// Exec is not available on Windows.
func Exec(path string, argv, env []string) error {
	return errors.New("restarting in place is not supported on Windows")
}
