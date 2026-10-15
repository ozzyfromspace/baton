package procs

import "os"

func list() ([]Proc, error) { return fromProc(os.DirFS("/"), os.Getuid(), os.Getpid()) }

func parent(pid int) (int, string, error) { return parentFromProc(os.DirFS("/"), pid) }
