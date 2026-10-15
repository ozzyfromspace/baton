package hooks

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/state"
)

// inherited handles event in a session that only inherited baton's environment (hostcheck.Inherited):
// Claude Code moved the conversation to its background service, say, under a new session id, and no baton
// host is above it. Nothing of baton's runs there. At SessionStart or a prompt, baton tells the human
// once that it does not host the session, and how to give it back (decide.Inherited), and logs it in the
// run its host instance drove, if that is still linked. Where no mark of the telling can be kept, it tells
// them at each SessionStart.
func inherited(event string, stdin io.Reader, stdout, stderr io.Writer, env func(string) string, now func() time.Time) {
	if event != "SessionStart" && event != "UserPromptSubmit" {
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(stdin, maxInput))
	var in struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(raw, &in)
	session, dir := cmp.Or(in.SessionID, env("CLAUDE_CODE_SESSION_ID")), env("BATON_DIR")
	tell, err := state.TellInherited(dir, session)
	if err != nil {
		tell = event == "SessionStart"
	}
	if !tell {
		return
	}
	out, err := json.Marshal(map[string]any{"systemMessage": decide.Inherited(session)})
	if err != nil {
		fmt.Fprintf(stderr, "baton: hook %s produced unencodable output: %v\n", event, err)
		return
	}
	stdout.Write(out)
	if dir == "" {
		return
	}
	if p, err := state.OpenProject(dir, now); err == nil {
		if s, ok := p.Lookup("", env("BATON_INSTANCE")); ok {
			s.Event("inherited_notice", map[string]any{"session": session, "event": event})
		}
	}
}
