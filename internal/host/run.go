package host

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/term"
)

// Exit is how the lead ended: claude's exit code, and what ended it (Session.EndedBy).
type Exit struct {
	Code int
	By   string
}

// Run starts claude, the lead session, in the human's terminal, and returns how it ended once it has
// exited. The lead alone has the terminal: its raw mode, its keys and its size.
func Run(cfg Config) (Exit, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.TypeDelay == 0 {
		cfg.TypeDelay = defaultTypeDelay
	}
	if cfg.EnterDelay == 0 {
		cfg.EnterDelay = defaultEnterDelay
	}
	if cfg.HangupGrace == 0 {
		cfg.HangupGrace = defaultHangupGrace
	}
	bind(cfg)
	if cfg.Project != nil {
		// What says the session is hosted, and not one that inherited its environment (package hostcheck).
		if err := cfg.Project.SetHostPID(cfg.Instance, os.Getpid()); err != nil {
			cfg.Logf("host: recording its process: %v", err)
		}
	}
	args := append([]string{"--settings", SettingsJSON(cfg.BatonBin)}, cfg.Args...)
	if cfg.Autocompact != "" {
		args = append([]string{"--autocompact", cfg.Autocompact}, args...)
	}
	// Always start from an environment with no BATON_* in it. A session started inside another one
	// inherits them, and hooks that kept them would write the FIRST session's run.
	env := append(withoutBatonVars(cfg.Env), cfg.BatonEnv...)
	env = append(env, "BATON_HOST=1", "BATON_INSTANCE="+cfg.Instance, "BATON_BIN="+cfg.BatonBin, "BATON_VERSION="+cfg.Version)
	if cfg.Project != nil {
		env = append(env, "BATON_DIR="+cfg.Project.Dir)
	}
	cmd := exec.Command(cfg.Claude, args...)
	cmd.Env = env

	rows, cols := uint16(50), uint16(160)
	interactive := term.IsTerminal(int(cfg.Stdin.Fd()))
	if interactive {
		if c, r, err := term.GetSize(int(cfg.Stdout.Fd())); err == nil {
			rows, cols = uint16(r), uint16(c)
		}
	}
	lead, err := startSession(cfg, cmd, rows, cols)
	if err != nil {
		return Exit{Code: 1, By: EndedSelf}, err
	}

	// From here on the user's terminal is raw; restore it on every way out, panics included.
	restore := func() {}
	if interactive {
		if old, err := term.MakeRaw(int(cfg.Stdin.Fd())); err == nil {
			restore = func() { term.Restore(int(cfg.Stdin.Fd()), old) }
			defer restore()
		}
	}
	go pumpInput(cfg, lead, interactive)
	stopSignals := forwardSignals(signalHandlers{
		resize: func() {
			if interactive {
				if c, r, err := term.GetSize(int(cfg.Stdout.Fd())); err == nil {
					lead.Resize(uint16(r), uint16(c))
				}
			}
		},
		hangup:  func() { lead.hangUp("baton got SIGHUP") },
		forward: lead.pass,
	})
	defer stopSignals()

	<-lead.Done()
	restore()
	return Exit{Code: lead.code, By: lead.EndedBy()}, nil
}

// pumpInput passes the human's keys to the lead.
func pumpInput(cfg Config, lead *Session, interactive bool) {
	buf := make([]byte, 4096)
	for {
		n, err := cfg.Stdin.Read(buf)
		if err != nil && interactive {
			// A terminal in raw mode never reads as EOF (Ctrl-D is a byte): the terminal is gone.
			lead.hangUp(fmt.Sprintf("reading from it failed: %v", err))
		}
		if n > 0 {
			if werr := lead.Keys(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// ended reports a session whose claude has exited.
func (s *Session) ended() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}
