package lane

import (
	"slices"
	"testing"
)

func TestScripts(t *testing.T) {
	for _, c := range []struct {
		command string
		want    []string
	}{
		{"go test ./...", nil},
		{"make e2e", nil},
		{"./scripts/gate.sh", []string{"./scripts/gate.sh"}},
		{"sh /tmp/gate.sh --fast", []string{"/tmp/gate.sh"}},
		{"bash -x ~/bin/check", []string{"~/bin/check"}},
		{"timeout 600 python3 -u /private/tmp/s/run.py", []string{"/private/tmp/s/run.py"}},
		{"python -m pytest tests", nil},
		{"python3 -c 'print(1)'", nil},
		{"node --require ./setup.js e2e/run.js", []string{"e2e/run.js"}},
		{"go run /tmp/x.go -v", []string{"/tmp/x.go"}},
		{"go run ./cmd/gate", []string{"./cmd/gate"}},
		{"sh -c 'cd web && /tmp/gate.sh'", []string{"/tmp/gate.sh"}},
		{"FOO=1 /usr/local/bin/golangci-lint run", []string{"/usr/local/bin/golangci-lint"}},
		{"make build && $TMPDIR/g.sh", []string{"$TMPDIR/g.sh"}},
	} {
		if got := Scripts(c.command); !slices.Equal(got, c.want) {
			t.Errorf("Scripts(%q) = %q, want %q", c.command, got, c.want)
		}
	}
}

func TestIsSuite(t *testing.T) {
	for _, c := range []struct {
		command string
		want    bool
	}{
		{"go test ./...", true},
		{"go test -race ./internal/...", true},
		{"go test -run TestParse ./...", false},
		{"go test ./internal/state", false},
		{"go test ./internal/e2e", true},
		{"go test -run 'TestScenario' ./internal/e2e", false},
		{"go vet ./...", true},
		{"go build ./...", false},
		{"make test", true},
		{"make e2e", true},
		{"make build", false},
		{"make -C web lint", true},
		{"npm test", true},
		{"npm test -- parse", false},
		{"pnpm lint", true},
		{"pnpm run test:e2e", true},
		{"pnpm run test:e2e -- --grep login", false},
		{"pnpm vitest run", true},
		{"pnpm vitest run src/parse.test.ts", false},
		{"pytest", true},
		{"pytest -x -q", true},
		{"pytest tests/test_parse.py", false},
		{"cargo test", true},
		{"cargo test parse", false},
		{"cargo clippy", true},
		{"tsc --noEmit", true},
		{"playwright test", true},
		{"playwright test login.spec.ts", false},
		{"eslint .", true},
		{"eslint src/parse.ts", false},
		{"golangci-lint run", true},
		{"golangci-lint run ./...", true},
		{"ruff check .", true},
		{"sh -c 'make build && make test'", true},
		{"./scripts/gate.sh", false},
		{"echo hi", false},
	} {
		if got := IsSuite(c.command); got != c.want {
			t.Errorf("IsSuite(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}
