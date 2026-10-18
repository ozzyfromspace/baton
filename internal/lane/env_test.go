package lane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each signature of an environment failure is known by the line real tools print, and lines that only look
// alike are not.
func TestEnvironmentLine(t *testing.T) {
	for _, c := range []struct{ line, kind string }{
		{"Error response from daemon: driver failed programming external connectivity on endpoint lane-db-1: Bind for 0.0.0.0:5432 failed: port is already allocated", "a port already in use"},
		{"Error: port already allocated", "a port already in use"},
		{"listen tcp :8080: bind: address already in use", "a port already in use"},
		{"OSError: [Errno 98] Address already in use", "a port already in use"},
		{"Error: listen EADDRINUSE: :::3000", "a port already in use"},
		{"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", "the Docker daemon out of reach"},
		{"dependency failed to start: container lane-db-1 is unhealthy", "a container that is unhealthy"},
		{"write /tmp/x: no space left on device", "a full disk"},
		{"npm ERR! code ENOSPC", "a full disk"},
		{"toomanyrequests: You have reached your pull rate limit.", "a registry's rate limit"},
		{"Error: You have reached your pull rate limit", "a registry's rate limit"},
		{"npm ERR! 429 Too Many Requests - GET https://registry.npmjs.org/left-pad", "a registry's rate limit"},
		{"error: API rate limit exceeded for 1.2.3.4", "a registry's rate limit"},
		{"fatal: unable to access 'https://github.com/x/y/': Could not resolve host: github.com", "a host name that does not resolve"},
		{"ssh: Temporary failure in name resolution", "a host name that does not resolve"},
		{"dial tcp: lookup proxy.golang.org on 127.0.0.53:53: no such host", "a host name that does not resolve"},
		{"dial tcp: lookup db: no such host", "a host name that does not resolve"},

		{"--- PASS: TestRateLimiter (0.01s)", ""},
		{"the service is unhealthy", ""}, // no container named
		{"port 5432 is free", ""},
		{"no such host is known to this test", ""},
		{"", ""},
	} {
		got := EnvironmentLine(c.line)
		if got.Kind != c.kind {
			t.Errorf("EnvironmentLine(%q) = %q, want %q", c.line, got.Kind, c.kind)
		}
		if c.kind != "" && got.Line != strings.TrimSpace(c.line) {
			t.Errorf("EnvironmentLine(%q) quotes %q", c.line, got.Line)
		}
	}
}

// A log is the environment's when it shows a signature and no test failure: its first such line is the one
// quoted. A test failure anywhere beside it (a runner's failure line, a fail marker) makes it the change's,
// while Docker Compose's cross for a container it could not start does not count as one.
func TestEnvironment(t *testing.T) {
	const port = "Bind for 0.0.0.0:5432 failed: port is already allocated"
	for _, c := range []struct {
		name, log string
		want      EnvMatch
	}{
		{"a taken port", "Starting the stack\n" + port + "\nexit status 1\n", EnvMatch{"a port already in use", port}},
		{"the first of two", "  cannot connect to the Docker daemon at unix:///var/run/docker.sock\nwrite: no space left on device\n",
			EnvMatch{"the Docker daemon out of reach", "cannot connect to the Docker daemon at unix:///var/run/docker.sock"}},
		{"compose's cross is not a test failure", " ✘ Container lane-db-1  Error\ndependency failed to start: container lane-db-1 is unhealthy\n",
			EnvMatch{"a container that is unhealthy", "dependency failed to start: container lane-db-1 is unhealthy"}},
		{"a failing test before it", "--- FAIL: TestLogin (0.20s)\n" + port + "\n", EnvMatch{}},
		{"a failing test after it", port + "\nFAIL\tgithub.com/x/y\t0.3s\n", EnvMatch{}},
		{"a fail marker", port + "\n::baton fail TestLogin\n", EnvMatch{}},
		{"a test's cross", port + "\n  ✗ renders the board (12 ms)\n", EnvMatch{}},
		{"a failed stage is not a test failure", port + "\n::baton stage up failed exit=1 seconds=3\n", EnvMatch{"a port already in use", port}},
		{"no signature", "--- PASS: TestA (0.00s)\nexit status 2\n", EnvMatch{}},
		{"an empty log", "", EnvMatch{}},
		{"no newline at the end", port, EnvMatch{"a port already in use", port}},
		{"baton's own lines are not the gate's", "[baton] job j1: echo 'port is already allocated'; exit 1, on 8b0ff5d in lane 1\n" +
			"[baton 14:02:11] failed (exit 1) after 0s\n", EnvMatch{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "j1.log")
			if err := os.WriteFile(path, []byte(c.log), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := Environment(path); got != c.want {
				t.Errorf("Environment = %+v, want %+v", got, c.want)
			}
		})
	}
	if got := Environment(filepath.Join(t.TempDir(), "none.log")); got != (EnvMatch{}) {
		t.Errorf("a log that is not there: %+v", got)
	}
}

// A long line is quoted clipped, and a line longer than the reader's buffer is read past whole.
func TestEnvironmentLongLines(t *testing.T) {
	long := "Error: " + strings.Repeat("x", 600) + " address already in use"
	m := EnvironmentLine(long)
	if m.Kind != "a port already in use" || len(m.Line) > envClip+len("…") || !strings.HasSuffix(m.Line, "…") {
		t.Errorf("clipped to %d bytes: %+v", len(m.Line), m.Kind)
	}
	path := filepath.Join(t.TempDir(), "j1.log")
	huge := strings.Repeat("y", 200<<10) + "\n"
	os.WriteFile(path, []byte(huge+"could not resolve host: x\n"), 0o644)
	if got := Environment(path); got.Kind != "a host name that does not resolve" {
		t.Errorf("past a huge line: %+v", got)
	}
}
