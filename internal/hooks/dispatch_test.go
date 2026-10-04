package hooks

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// events lists every hook event baton subscribes to; each one must fail open.
var events = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest",
	"PermissionDenied", "Notification", "Stop", "StopFailure", "SubagentStart", "SubagentStop",
	"PreCompact", "PostCompact",
}

func hosted(k string) string {
	if k == "BATON_HOST" {
		return "1"
	}
	return ""
}

func plain(string) string { return "" }

func now() time.Time { return time.Unix(0, 0) }

func run(t *testing.T, event, input string, env func(string) string, h Handler) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Dispatch(event, strings.NewReader(input), &out, &errb, env, now, map[string]Handler{event: h})
	return code, out.String(), errb.String()
}

func TestPanicFailsOpenForEveryEvent(t *testing.T) {
	for _, ev := range events {
		code, out, errs := run(t, ev, `{"hook_event_name":"`+ev+`"}`, hosted, func(Context) (Result, error) { panic("boom") })
		if code != 0 {
			t.Errorf("%s: panic exited %d, want 0", ev, code)
		}
		if out != "" {
			t.Errorf("%s: panic wrote stdout %q", ev, out)
		}
		if !strings.Contains(errs, "failed open") {
			t.Errorf("%s: stderr %q does not explain the failure", ev, errs)
		}
	}
}

func TestHandlerErrorFailsOpen(t *testing.T) {
	code, out, _ := run(t, "Stop", `{}`, hosted, func(Context) (Result, error) {
		return Result{Output: map[string]any{"decision": "block"}}, errors.New("state locked")
	})
	if code != 0 || out != "" {
		t.Fatalf("got code %d stdout %q; an erroring handler must not block", code, out)
	}
}

func TestInvalidJSONFailsOpen(t *testing.T) {
	called := false
	code, _, _ := run(t, "Stop", `{not json`, hosted, func(Context) (Result, error) { called = true; return Result{}, nil })
	if code != 0 || called {
		t.Fatalf("got code %d called %v", code, called)
	}
}

func TestDormantUnlessHosted(t *testing.T) {
	called := false
	code, out, _ := run(t, "Stop", `{}`, plain, func(Context) (Result, error) {
		called = true
		return Result{Output: map[string]any{"decision": "block"}}, nil
	})
	if code != 0 || out != "" || called {
		t.Fatalf("plain session: code %d stdout %q called %v", code, out, called)
	}
}

func TestUnknownEventIsNoop(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Dispatch("Bogus", strings.NewReader(`{}`), &out, &errb, hosted, now, nil); code != 0 || out.Len() > 0 {
		t.Fatalf("code %d stdout %q", code, out.String())
	}
}

func TestOutputIsJSON(t *testing.T) {
	code, out, _ := run(t, "Stop", `{"stop_hook_active":false}`, hosted, func(c Context) (Result, error) {
		if c.Input["stop_hook_active"] != false {
			t.Errorf("input not decoded: %v", c.Input)
		}
		return Result{Output: map[string]any{"decision": "block", "reason": "keep going"}}, nil
	})
	if code != 0 || out != `{"decision":"block","reason":"keep going"}` {
		t.Fatalf("code %d stdout %q", code, out)
	}
}

func TestOnlyRewakeExitsTwo(t *testing.T) {
	code, _, errs := run(t, "PostCompact", `{}`, hosted, func(Context) (Result, error) {
		return Result{Rewake: "baton: compacted; begin P7"}, nil
	})
	if code != 2 || strings.TrimSpace(errs) != "baton: compacted; begin P7" {
		t.Fatalf("code %d stderr %q", code, errs)
	}
}
