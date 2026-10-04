package cli

import (
	"fmt"
	"strings"
)

// parsed holds positional arguments and --flags, which may appear in any order.
type parsed struct {
	pos   []string
	vals  map[string]string
	bools map[string]bool
}

// parseArgs accepts flags anywhere (the model writes `baton done P1 --notes "…"` as often as
// `baton done --notes "…" P1`). valueFlags take an argument (--x v or --x=v); boolFlags do not.
func parseArgs(args []string, valueFlags, boolFlags []string) (parsed, error) {
	p := parsed{vals: map[string]string{}, bools: map[string]bool{}}
	isValue := map[string]bool{}
	for _, f := range valueFlags {
		isValue[f] = true
	}
	isBool := map[string]bool{}
	for _, f := range boolFlags {
		isBool[f] = true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.pos = append(p.pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "--") || a == "-" {
			p.pos = append(p.pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		switch {
		case isBool[name]:
			if hasVal {
				return p, fmt.Errorf("--%s takes no value", name)
			}
			p.bools[name] = true
		case isValue[name]:
			if !hasVal {
				if i+1 >= len(args) {
					return p, fmt.Errorf("--%s needs a value", name)
				}
				i++
				val = args[i]
			}
			p.vals[name] = val
		default:
			return p, fmt.Errorf("unknown flag --%s", name)
		}
	}
	return p, nil
}
