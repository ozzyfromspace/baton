package procs

import "os"

func list() ([]Proc, error) { return fromProc(os.DirFS("/"), os.Getuid(), os.Getpid()) }
