// fakeclaude stands in for claude in the host's integration tests. It records its arguments, its
// BATON_* environment (and CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS), its working directory and every byte
// it receives into $FAKE_OUT, and exits with $FAKE_EXIT when it reads a 'Q'.
//
// With FAKE_HUP set it floods the screen instead, the way a busy claude does. FAKE_HUP=loop notices
// SIGHUP only between writes, as claude does (its handler runs on the thread that writes), so it
// exits 129 only if someone keeps reading its output; FAKE_HUP=ignore never exits on SIGHUP.
// FAKE_TERM=ignore ignores SIGTERM too.
//
// It exits 0 on a typed /exit line unless FAKE_SLASH=ignore. FAKE_MODES=1 sets terminal modes at startup,
// FAKE_FLOOD=n writes n numbered lines once it is ready, and FAKE_CHILD=1 starts a child in a process
// session of its own, as the Bash tool does, and records its pid in $FAKE_OUT/child.pid.
//
// Like claude, it repaints its screen when its terminal changes size: it writes "repaint ROWSxCOLS".
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
		if strings.HasPrefix(kv, "BATON_") || strings.HasPrefix(kv, "CLAUDE_CODE_AUTO_BACKGROUND_TIMEOUT_MS=") {
			k, v, _ := strings.Cut(kv, "=")
			env[k] = v
		}
	}
	wd, _ := os.Getwd()
	b, _ := json.Marshal(map[string]any{"args": os.Args[1:], "env": env, "dir": wd})
	os.WriteFile(filepath.Join(out, "start.json"), b, 0o644)

	if old, err := term.MakeRaw(0); err == nil {
		defer term.Restore(0, old)
	}
	if os.Getenv("FAKE_MODES") == "1" {
		fmt.Print("\x1b[?1004h\x1b[?2004;25h\x1b[?2026h\x1b[?25l\x1b[?2026l")
	}
	go repaint(watchSize())
	fmt.Print("fake-claude ready\r\n")
	if n, _ := strconv.Atoi(os.Getenv("FAKE_FLOOD")); n > 0 {
		for i := 1; i <= n; i++ {
			fmt.Printf("line %05d\r\n", i)
		}
		fmt.Print("flood done\r\n")
	}
	if os.Getenv("FAKE_CHILD") == "1" {
		child := exec.Command("sleep", "300")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err == nil {
			os.WriteFile(filepath.Join(out, "child.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o644)
		}
	}
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
	var line []byte
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
			for _, c := range buf[:n] {
				switch c {
				case '\r':
					if string(line) == "/exit" && os.Getenv("FAKE_SLASH") != "ignore" {
						in.Close()
						os.Exit(0)
					}
					line = line[:0]
				case 0x03:
					line = line[:0]
				default:
					line = append(line, c)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// sized is the terminal's size, and its changes from now on.
type sized struct {
	last  string
	winch chan os.Signal
}

// watchSize starts listening for the terminal's size changes, before the fake says it is ready: a resize
// that came before would be lost.
func watchSize() sized {
	s := sized{winch: make(chan os.Signal, 1)}
	signal.Notify(s.winch, syscall.SIGWINCH)
	if c, r, err := term.GetSize(0); err == nil {
		s.last = fmt.Sprintf("%dx%d", r, c)
	}
	return s
}

// repaint writes the terminal's size each time it changes. A resize to the size it has is ignored, as
// claude ignores it.
func repaint(s sized) {
	last, winch := s.last, s.winch
	for range winch {
		if c, r, err := term.GetSize(0); err == nil {
			if size := fmt.Sprintf("%dx%d", r, c); size != last {
				last = size
				fmt.Printf("repaint %s\r\n", size)
			}
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
