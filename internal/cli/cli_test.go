package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ozzyfromspace/baton/internal/hooks"
)

func testIO(stdin string, env map[string]string) (IO, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return IO{
		In:  strings.NewReader(stdin),
		Out: &out, Err: &errb,
		Env: func(k string) string { return env[k] },
		Now: func() time.Time { return time.Unix(0, 0) },
	}, &out, &errb
}

func TestVersionAndHelp(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		io, out, _ := testIO("", nil)
		if code := Main(args, io); code != 0 || !strings.HasPrefix(out.String(), "baton ") {
			t.Errorf("%v: code %d out %q", args, code, out.String())
		}
	}
	io, out, _ := testIO("", nil)
	if code := Main([]string{"help"}, io); code != 0 || !strings.Contains(out.String(), "hook") {
		t.Errorf("help: code %d out %q", code, out.String())
	}
}

// A malformed hook invocation must never exit 2 (which Claude Code reads as "block").
func TestHookArgumentProblemsFailOpen(t *testing.T) {
	hosted := map[string]string{"BATON_HOST": "1"}
	for _, args := range [][]string{{"hook"}, {"hook", "--bogus"}, {"hook", "Stop", "--bogus"}, {"hook", "NotAnEvent"}} {
		io, out, _ := testIO(`{}`, hosted)
		if code := Main(args, io); code != 0 || out.Len() != 0 {
			t.Errorf("%v: code %d stdout %q", args, code, out.String())
		}
	}
}

func TestHookPanicFailsOpenThroughCLI(t *testing.T) {
	old := hookHandlers
	defer func() { hookHandlers = old }()
	hookHandlers = map[string]hooks.Handler{"Stop": func(hooks.Context) (hooks.Result, error) { panic("boom") }}
	io, out, _ := testIO(`{}`, map[string]string{"BATON_HOST": "1"})
	if code := Main([]string{"hook", "Stop"}, io); code != 0 || out.Len() != 0 {
		t.Fatalf("code %d stdout %q", code, out.String())
	}
}

func TestVersionAtLeast(t *testing.T) {
	for _, c := range []struct {
		have, want string
		ok         bool
	}{{"2.1.289", "2.1.289", true}, {"2.1.300", "2.1.289", true}, {"2.2.0", "2.1.289", true}, {"2.1.288", "2.1.289", false}, {"1.9.999", "2.1.289", false}} {
		if versionAtLeast(c.have, c.want) != c.ok {
			t.Errorf("%s >= %s", c.have, c.want)
		}
	}
}
