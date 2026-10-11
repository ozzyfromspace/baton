//go:build !darwin && !linux

package machine

import "errors"

// read knows nothing here but the CPU count, which Read sets.
func read(*Reading) error {
	return errors.New("machine: no live readings on this platform")
}
