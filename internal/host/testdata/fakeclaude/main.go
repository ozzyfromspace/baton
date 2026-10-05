// fakeclaude stands in for claude in the host's integration tests. It records its arguments, its
// BATON_* environment and every byte it receives into $FAKE_OUT, and exits with $FAKE_EXIT when it
// reads a 'Q'.
//
// With FAKE_HUP set it floods the screen instead, the way a busy claude does. FAKE_HUP=loop notices
// SIGHUP only between writes, as claude does (its handler runs on the thread that writes), so it
// exits 129 only if someone keeps reading its output; FAKE_HUP=ignore never exits on SIGHUP.
// FAKE_TERM=ignore ignores SIGTERM too.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

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
	if os.Getenv("FAKE_TERM") == "ignore" {
		signal.Ignore(syscall.SIGTERM)
	}
	switch os.Getenv("FAKE_HUP") {
	case "loop":
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		flood(func() bool {
			select {
			case <-hup:
				os.Exit(129)
			default:
			}
			return true
		})
	case "ignore":
		signal.Ignore(syscall.SIGHUP)
		flood(func() bool { return true })
	}
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

// flood writes to the screen for as long as more says so.
func flood(more func() bool) {
	line := strings.Repeat("busy ", 30) + "\r\n"
	for more() {
		os.Stdout.WriteString(line)
	}
}
