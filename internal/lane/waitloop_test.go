package lane

import "testing"

func TestIsWaitLoop(t *testing.T) {
	// The 74-phase campaign's loop, as its model started it in the background (its paths' project left out): its
	// target ended without the pattern, and it polled for 22 hours.
	const campaign = `until [ -s /private/tmp/claude-501/-Users-me-Projects-app/77ee516d-53ba-4648-a763-0a22ca6723f9/tasks/bj514tpgn.output ] && tail -2 /private/tmp/claude-501/-Users-me-Projects-app/77ee516d-53ba-4648-a763-0a22ca6723f9/tasks/bj514tpgn.output | grep -qE "passed|failed|error|Error"; do sleep 5; done; tail -40 /private/tmp/claude-501/-Users-me-Projects-app/77ee516d-53ba-4648-a763-0a22ca6723f9/tasks/bj514tpgn.output`
	cases := []struct {
		command string
		want    bool
	}{
		{campaign, true},
		// the same campaign's other waits on a task's output
		{`until grep -q "exited with code" /tmp/claude-501/p/s/tasks/b7hecvvd2.output 2>/dev/null; do sleep 3; done; grep -v "orphan" /tmp/claude-501/p/s/tasks/b7hecvvd2.output | tail -15`, true},
		{`F=/tmp/claude-501/p/s/tasks/bj514tpgn.output; until grep -qE "passed|failed|error" <(tail -3 "$F" 2>/dev/null) && ! pgrep -f "pytest" >/dev/null 2>&1; do sleep 5; done; tail -3 "$F"`, true},
		{"cd /p && until grep -q done out.log; do sleep 3; done", true},
		{"while true; do curl -s localhost:3000/health; sleep 5; done", true},
		{"while ! nc -z localhost 5432; do sleep 1; done; echo ready", true},
		{"while sleep 5; do pgrep pytest || break; done", true}, // the sleep in its condition
		{"while :; do date; sleep 60; done > clock.log", true},
		{"until [ -f done.txt ]\ndo\n  sleep 5\ndone", true},
		{"for i in 1 2 3; do while ! test -f $i; do sleep 1; done; done", true},
		{"if [ -d x ]; then until test -f x/y; do sleep 2; done; fi", true},
		{"timeout 600 bash -c 'until curl -sf localhost:8000/health; do sleep 2; done'", true},
		{`sh -c "while true; do ls; /bin/sleep 1; done"`, true},
		{"while true; do make; gsleep 2; done", true},
		{"while (-not (Test-Path out.txt)) { Start-Sleep -Seconds 5 }", true},
		{"do { Start-Sleep 2 } until (Test-Path done.txt)", true},
		{"tail -f server.log", true},
		{"tail -F watch.log", true},
		{"tail -n0 -F watch.log", true},
		{"tail -fn 100 app.log", true},
		{"tail -n 100 -f app.log", true},
		{"tail --follow=name app.log", true},
		{"tail --follow app.log | grep -m1 ready", true},
		{"tail -20f app.log", true},
		// waits the hooks question (internal/hooks' TestAWaitLoopIsQuestioned)
		{"while pgrep -f pytest >/dev/null; do sleep 10; done; echo finished", true}, // a process waited out
		{"until go test ./...; do sleep 5; done", true},                              // a check retried until it passes
		{"cd web && while ! curl -sf localhost:3000 >/dev/null; do sleep 1; done && pnpm e2e", true},
		{"until [ \"$(gh run view 123 --json status -q .status)\" = completed ]; do sleep 30; done", true}, // CI, polled
		{"for f in a b c; do make $f; sleep 1; done", false},
		{"go build ./... && ./dist/baton version", false},

		{"tail -2 out.log", false},
		{"tail -n 50 out.log", false},
		{"tail -c 100 out.log", false},
		{"git log --follow -- file.go", false},
		{"while read -r line; do echo \"$line\"; done < list.txt", false}, // no sleep: it ends with its input
		{"for f in *.json; do jq . \"$f\"; sleep 1; done", false},         // a for loop ends with its list
		{"sleep 5 && while read l; do echo $l; done < f", false},
		{"while read l; do echo $l; done < f; sleep 5", false},
		{"if [ -f x ]; then sleep 1; fi", false},
		{"sleep 20", false},
		{"pnpm dev", false},
		{"go test ./...", false},
		{`echo "while true; do sleep 1; done"`, false},
		{`git commit -m "until it works, sleep 5"`, false},
		{"", false},
		{"'unterminated", false},
	}
	for _, c := range cases {
		if got := IsWaitLoop(c.command); got != c.want {
			t.Errorf("IsWaitLoop(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}
