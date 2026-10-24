package lane

import "strings"

// A stage the machine killed is the machine's (v0.8.17): in a real run the out-of-memory killer of Docker's
// VM took a QA set's build in three of its four whole runs, beside a second copy of the stack, and the
// agent read each as one more red stage, since baton knew no memory signature. EnvKilled is that
// signature, one of envSigns.

// EnvKilled is the kind of environment failure of a process killed for memory: a runtime out of heap, the
// kernel's out-of-memory killer, a SIGKILL (exit code 137, 128 and the signal's 9).
const EnvKilled = "a process killed for memory"

// killedPhrases are the lines' words EnvKilled knows, in lower case: Node's "JavaScript heap out of memory"
// and the kernel's "Out of memory: Killed process", its "oom-kill:" and "invoked oom-killer", a child
// process "killed with SIGKILL", a package manager's or a CI step's "exit code 137", Docker Compose's
// "exited with code 137", and Go's "signal: killed".
var killedPhrases = []string{"out of memory", "oom-kill", "invoked oom-killer", "killed with sigkill", "exit code 137", "exited with code 137", "signal: killed"}

// killedForMemory reports a line, in lower case, that says a process was killed for memory (killedPhrases),
// unless it is a test runner's line naming a test (namesTest), whose name may well hold those words.
func killedForMemory(lower string) bool {
	if namesTest(lower) {
		return false
	}
	for _, p := range killedPhrases {
		if hasPhrase(lower, p) {
			return true
		}
	}
	return false
}

// hasPhrase reports whether s holds phrase as words of its own: with no letter or digit just before it,
// and, for a phrase ending in a digit, no digit just after it ("exit code 137", not "exit code 1370").
func hasPhrase(s, phrase string) bool {
	for i := 0; ; {
		k := strings.Index(s[i:], phrase)
		if k < 0 {
			return false
		}
		at, end := i+k, i+k+len(phrase)
		before := at == 0 || !alnum(s[at-1])
		after := !digit(phrase[len(phrase)-1]) || end == len(s) || !digit(s[end])
		if before && after {
			return true
		}
		i = at + 1
	}
}

func digit(c byte) bool { return '0' <= c && c <= '9' }

func alnum(c byte) bool { return digit(c) || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// testMarks open a line that reports a test as runners print it: passed, failed, skipped or running.
var testMarks = []string{"✓", "✔", "√", "✗", "✘", "×", "✕", "○", "●", "↓", "❯", "--- ", "=== ", "ok ", "ok\t", "pass ", "pass:", "pass\t", "fail ", "fail:", "fail\t"}

// namesTest reports a line, in lower case, that reports a test by its name, as a runner prints one: Go's
// `--- FAIL: TestX` and `=== RUN`, a mark (✓, ✗, ×, …), vitest's and jest's `FAIL  file > name`, pytest's
// `file::name PASSED`, jest's and Playwright's `file › name`, mocha's and Playwright's `1) name`, and
// cargo's `test name ... ok`.
func namesTest(lower string) bool {
	t := strings.TrimLeft(lower, " \t")
	for _, m := range testMarks {
		if strings.HasPrefix(t, m) {
			return true
		}
	}
	switch {
	case strings.Contains(t, "::") && containsAny(t, " passed", " failed", " skipped", " error", " xfail", " xpass"):
		return true
	case strings.Contains(t, " › "):
		return true
	case strings.HasPrefix(t, "test ") && strings.Contains(t, " ... "):
		return true
	}
	n := 0
	for n < len(t) && digit(t[n]) {
		n++
	}
	return n > 0 && strings.HasPrefix(t[n:], ") ")
}
