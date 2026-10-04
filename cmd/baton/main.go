// Command baton runs multi-phase Claude Code plans unattended, compacting at every phase boundary in
// one live terminal session. See https://github.com/ozzyfromspace/baton.
package main

import (
	"os"

	"github.com/ozzyfromspace/baton/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.StdIO()))
}
