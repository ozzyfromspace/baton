// Command baton runs multi-phase Claude Code plans unattended, compacting at every phase boundary in
// one live terminal session. See https://github.com/ozzyfromspace/baton.
package main

import (
	"os"

	"github.com/ozzyfromspace/baton/internal/cli"
	"github.com/ozzyfromspace/baton/internal/upgrade"
)

func main() {
	if bin := cli.HostBinary(os.Getenv); bin != "" {
		upgrade.Exec(bin, append([]string{bin}, os.Args[1:]...), os.Environ()) // returns only if it failed
	}
	os.Exit(cli.Main(os.Args[1:], cli.StdIO()))
}
