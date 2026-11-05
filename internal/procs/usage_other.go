//go:build !darwin && !linux

package procs

import "time"

func treeUsage(int) (Usage, error) { return nil, ErrUnsupported }

func cpuTime(uint64) time.Duration { return 0 }
