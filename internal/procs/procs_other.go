//go:build !darwin && !linux

package procs

func list() ([]Proc, error) { return nil, ErrUnsupported }
