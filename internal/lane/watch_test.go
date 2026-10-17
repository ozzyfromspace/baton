package lane

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWatcher(t *testing.T) {
	many := []string{"::baton stage unit start\n"}
	for i := range MaxFailures + 1 {
		many = append(many, fmt.Sprintf("::baton fail Test%d\n", i))
	}
	many = append(many, "::baton stage unit failed exit=1 seconds=3\n")
	long := "--- FAIL: TestLong " + strings.Repeat("x", 100<<10)

	for _, c := range []struct {
		name   string
		writes []string
		stages []string // name status exit took failures, as each ended
		fail   string   // the first failure's line and stage, "" for none
	}{
		{"no markers, no failure", []string{"ok\nall good\n"}, nil, ""},
		{"a failure line", []string{"=== RUN TestA\n--- FAIL: TestA (0.00s)\nFAIL\n"}, nil, "--- FAIL: TestA (0.00s) | "},
		{"stages, and the fail lines of a failed one", []string{
			"::baton stage lint start\nlint ok\n::baton stage lint passed exit=0 seconds=9\n",
			"::baton stage unit start\n::baton fail TestA\n::baton fail TestB\n::baton fail TestA\n--- FAIL: TestA\n",
			"::baton stage unit failed exit=1 seconds=4\n",
		}, []string{"lint passed 0 1s []", "unit failed 1 1s [TestA TestB]"}, "::baton fail TestA | unit"},
		{"a failed stage with no line before it", []string{"::baton stage e2e start\n  ::baton stage e2e failed exit=2 seconds=5\n"},
			[]string{"e2e failed 2 1s []"}, "::baton stage e2e failed exit=2 seconds=5 | e2e"},
		{"an end with no start", []string{"::baton stage lint carried exit=0 seconds=0\n::baton stage unit skipped exit=1 seconds=7\n"},
			[]string{"lint carried 0 0s []", "unit skipped 1 7s []"}, ""},
		{"more fail lines than a stage keeps", many, []string{"unit failed 1 1s []"}, "::baton fail Test0 | unit"},
		{"a stage that passed keeps no failures", []string{"::baton stage unit start\n::baton fail TestFlaky\n::baton stage unit passed exit=0 seconds=1\n"},
			[]string{"unit passed 0 1s []"}, "::baton fail TestFlaky | unit"},
		{"a fail line outside a stage", []string{"::baton fail TestLoose\n"}, nil, "::baton fail TestLoose | "},
		{"lines split across writes, with carriage returns", []string{"::baton st", "age lint start\r\n--- FA", "IL: TestX\r\n"}, nil, "--- FAIL: TestX | lint"},
		{"a last line with no end, read at the flush", []string{"ok\n--- FAIL: TestZ"}, nil, "--- FAIL: TestZ | "},
		{"a long line, read by its start", []string{long[:40<<10], long[40<<10:] + "\n"}, nil, long[:FailingClip] + "… | "},
		{"a marker inside a line", []string{"echo ::baton stage x start\nsaid ::baton fail TestQ\n"}, nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := time.Date(2026, 10, 17, 14, 0, 0, 0, time.UTC)
			var out strings.Builder
			var stages []string
			fails := 0
			fail := ""
			w := &Watcher{Out: &out, Commit: "abc123", Now: func() time.Time { now = now.Add(time.Second); return now },
				Ended: func(st Stage) {
					if st.Commit != "abc123" || st.Ended.Before(st.Started) {
						t.Errorf("stage %+v", st)
					}
					stages = append(stages, fmt.Sprintf("%s %s %d %s %v", st.Name, st.Status, st.Exit, st.Took(), st.Failures))
				},
				Failed: func(line, stage string) { fails++; fail = line + " | " + stage },
			}
			for _, s := range c.writes {
				if n, err := w.Write([]byte(s)); n != len(s) || err != nil {
					t.Fatalf("Write: %d, %v", n, err)
				}
			}
			w.Flush()
			if want := strings.Join(c.writes, ""); out.String() != want {
				t.Errorf("passed on %d bytes, want the %d written", out.Len(), len(want))
			}
			if !slices.Equal(stages, c.stages) {
				t.Errorf("stages %q, want %q", stages, c.stages)
			}
			if fail != c.fail || fails > 1 {
				t.Errorf("failure %q (told %d times), want %q", fail, fails, c.fail)
			}
		})
	}
}
