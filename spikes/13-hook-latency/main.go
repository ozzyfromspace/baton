package main

import (
	"encoding/json"
	"os"
)

// A stand-in for `baton hook <event>` on the dormant path: read stdin JSON, check one env var, exit 0.
func main() {
	var in map[string]any
	_ = json.NewDecoder(os.Stdin).Decode(&in)
	if os.Getenv("BATON_HOST") == "" {
		os.Exit(0)
	}
	os.Exit(0)
}
