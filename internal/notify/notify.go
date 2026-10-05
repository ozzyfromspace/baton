// Package notify tells the human, outside the session, that baton needs them or has finished. It is
// sent by the host, never by the model, so it goes out even when the model is stuck.
//
// Channels: a native desktop notification (macOS osascript, Linux notify-send) and, if configured, an
// ntfy push (https://ntfy.sh), which reaches phones and watches regardless of what the computer is
// doing. Pushes are content-free by default (project name and what kind of attention is needed): they
// leave the machine through a third-party relay, and ntfy topics are public by name.
package notify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/ozzyfromspace/baton/internal/config"
)

// Notifier sends one message to the human.
type Notifier interface {
	Notify(project, kind, detail string) error
}

// New returns a notifier for the configured channels.
func New(c config.Config) Notifier { return &multi{cfg: c} }

type multi struct{ cfg config.Config }

// headline is the content-free text for each kind of notice.
var headline = map[string]string{
	"blocked":           "baton needs you: blocked",
	"stalled":           "baton needs you: the run stalled",
	"compaction_failed": "baton needs you: a compaction did not happen",
	"draft":             "baton is waiting for your draft in the input box",
	"plan_complete":     "plan complete",
	"session_ended":     "the session ended mid-plan",
	"rate_limit":        "paused by a usage limit",
}

func (m *multi) Notify(project, kind, detail string) error {
	title := "baton — " + project
	body := headline[kind]
	if body == "" {
		body = "baton needs you"
	}
	if m.cfg.Details && detail != "" {
		body += ": " + detail
	}
	var errs []string
	if m.cfg.DesktopOn() {
		if err := desktop(title, body); err != nil {
			errs = append(errs, "desktop: "+err.Error())
		}
	}
	if m.cfg.NtfyTopic != "" {
		if err := ntfy(m.cfg.NtfyServer, m.cfg.NtfyTopic, title, body, kind); err != nil {
			errs = append(errs, "ntfy: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func desktop(title, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %s with title %s sound name \"Ping\"", appleString(body), appleString(title))
		return exec.CommandContext(ctx, "osascript", "-e", script).Run()
	case "linux":
		if _, err := exec.LookPath("notify-send"); err != nil {
			return nil // no notification daemon tool: the push channel still works
		}
		return exec.CommandContext(ctx, "notify-send", title, body).Run()
	default:
		return nil
	}
}

func appleString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func ntfy(server, topic, title, body, kind string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+"/"+topic, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	tag := "warning"
	if kind == "plan_complete" {
		tag = "white_check_mark"
	}
	req.Header.Set("Tags", tag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %s", resp.Status)
	}
	return nil
}
