//go:build !darwin && !linux

package procs

func treeUsage(int) (Usage, error) { return nil, ErrUnsupported }
