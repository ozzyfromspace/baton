package cli

import (
	"fmt"

	"github.com/ozzyfromspace/baton/internal/skill"
)

func init() {
	register("skill", "(internal) print the /baton skill's instructions (the plugin's skill loads them from here)", func(args []string, io IO) int {
		if len(args) != 0 {
			return fail(io, "usage: baton skill")
		}
		fmt.Fprint(io.Out, skill.Text)
		return 0
	})
}
