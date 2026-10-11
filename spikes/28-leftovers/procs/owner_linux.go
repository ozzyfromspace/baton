package main

import (
	"io/fs"
	"syscall"
)

func owner(fi fs.FileInfo) int { return int(fi.Sys().(*syscall.Stat_t).Uid) }
