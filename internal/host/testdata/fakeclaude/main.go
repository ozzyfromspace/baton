// fakeclaude stands in for claude in the host's integration tests. It records its arguments, its
// BATON_* environment and every byte it receives into $FAKE_OUT, and exits with $FAKE_EXIT when it
// reads a 'Q'.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"
)

func main() {
	out := os.Getenv("FAKE_OUT")
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "BATON_") {
			k, v, _ := strings.Cut(kv, "=")
			env[k] = v
		}
	}
	b, _ := json.Marshal(map[string]any{"args": os.Args[1:], "env": env})
	os.WriteFile(filepath.Join(out, "start.json"), b, 0o644)

	if old, err := term.MakeRaw(0); err == nil {
		defer term.Restore(0, old)
	}
	fmt.Print("fake-claude ready\r\n")
	in, _ := os.OpenFile(filepath.Join(out, "input.bin"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	buf := make([]byte, 1024)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			in.Write(buf[:n])
			fmt.Printf("got:%q\r\n", buf[:n])
			if strings.ContainsRune(string(buf[:n]), 'Q') {
				code, _ := strconv.Atoi(os.Getenv("FAKE_EXIT"))
				in.Close()
				os.Exit(code)
			}
		}
		if err != nil {
			return
		}
	}
}
