package lane

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"strings"
)

// A gate can fail for its environment rather than for the change it checks: the port its stack wants is
// taken (by the dev stack, or another lane's), the Docker daemon is not running, a container never got
// healthy, the disk is full, a registry rate-limits its pulls, a host name does not resolve, a process was
// killed for memory (killed.go). A model that reads such a failure as its own code's goes looking for a bug
// that is not there, and every phase finds the same fix again. Environment reads a red gate's log for the
// lines that say so, so that its result can say it (decide.EnvFailed); StageEnvironment reads each failed
// stage's part of it (stageenv.go).

// EnvMatch is what a log says of a gate's environment: Kind names what failed it ("a port already in
// use"), and Line is the log's line that says so, trimmed and clipped. The zero EnvMatch is none.
type EnvMatch struct {
	Kind string
	Line string
}

// envClip is how much of its line an EnvMatch keeps.
const envClip = 300

// envSign is one signature of an environment failure: Kind, and whether a line, in lower case, shows it.
type envSign struct {
	kind  string
	shows func(lower string) bool
}

var (
	unhealthyRE = regexp.MustCompile(`container \S+ is unhealthy`)
	noHostRE    = regexp.MustCompile(`lookup \S+(?: on \S+)?: no such host`)
)

// envSigns are the signatures Environment knows, in the order it tries them on each line.
var envSigns = []envSign{
	{"a port already in use", func(l string) bool {
		return containsAny(l, "port is already allocated", "port already allocated", "address already in use", "eaddrinuse")
	}},
	{"the Docker daemon out of reach", func(l string) bool { return strings.Contains(l, "cannot connect to the docker daemon") }},
	{"a container that is unhealthy", func(l string) bool { return strings.Contains(l, " is unhealthy") && unhealthyRE.MatchString(l) }},
	{"a full disk", func(l string) bool { return containsAny(l, "no space left on device", "enospc") }},
	{"a registry's rate limit", func(l string) bool {
		return containsAny(l, "toomanyrequests", "pull rate limit", "rate limit exceeded", "429 too many requests")
	}},
	{"a host name that does not resolve", func(l string) bool {
		return containsAny(l, "could not resolve host", "temporary failure in name resolution") ||
			strings.Contains(l, ": no such host") && noHostRE.MatchString(l)
	}},
	{EnvKilled, killedForMemory},
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// EnvironmentLine is the signature line shows (Kind, and the line trimmed and clipped), or none.
func EnvironmentLine(line string) EnvMatch {
	lower := strings.ToLower(line)
	for _, s := range envSigns {
		if s.shows(lower) {
			return EnvMatch{Kind: s.kind, Line: clipLine(strings.TrimSpace(line), envClip)}
		}
	}
	return EnvMatch{}
}

// Environment reads the log at path for the first line that shows an environment failure (envSigns). It
// gives none when the log also reports a test failure anywhere (testFailure): a failing test beside a
// taken port may well be the change's, so only a log with no test failure in it is called the
// environment's. A log that cannot be read gives none.
func Environment(path string) EnvMatch {
	f, err := os.Open(path)
	if err != nil {
		return EnvMatch{}
	}
	defer f.Close()
	return environmentOf(f)
}

// environmentOf is Environment of the log r. The lines baton's runner writes into it ("[baton] job j1:
// <command> …") are not the gate's output, and are passed over: the command they quote may well name what
// it works around.
func environmentOf(r io.Reader) EnvMatch {
	br := bufio.NewReaderSize(r, 64<<10)
	var found EnvMatch
	for {
		line, long, err := br.ReadLine()
		text := string(line)
		switch {
		case strings.HasPrefix(text, "[baton"):
		case testFailure(text):
			return EnvMatch{}
		case found.Kind == "":
			found = EnvironmentLine(text)
		}
		for long && err == nil { // the rest of a line longer than the reader's buffer
			_, long, err = br.ReadLine()
		}
		if err != nil {
			return found
		}
	}
}

// testFailure reports a line that says a test failed: one that reports a failure as test runners do
// (Failure), apart from Docker Compose's status of a container, network, volume or image it could not
// bring up, whose cross is the environment's; or a fail marker of the gate protocol (::baton fail).
func testFailure(line string) bool {
	if m, ok := ParseMarker(line); ok {
		return m.Kind == MarkFail
	}
	return Failure(line) && !composeStatus(line)
}

// composeStatus reports a status line of Docker Compose's for something it could not bring up:
// "✘ Container lane-db-1  Error".
func composeStatus(line string) bool {
	t := strings.TrimLeft(line, " \t")
	for _, cross := range []string{"✘ ", "✗ "} {
		if rest, ok := strings.CutPrefix(t, cross); ok {
			first, _, _ := strings.Cut(strings.TrimLeft(rest, " "), " ")
			switch first {
			case "Container", "Network", "Volume", "Image", "Service":
				return true
			}
		}
	}
	return false
}
