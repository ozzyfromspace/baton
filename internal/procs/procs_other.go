//go:build !darwin && !linux

package procs

func list() ([]Proc, error) { return nil, ErrUnsupported }

func parent(int) (int, string, error) { return 0, "", ErrUnsupported }
