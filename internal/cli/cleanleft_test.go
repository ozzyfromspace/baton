package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/leftover"
)

// baton clean lists the containers compose started in a worktree of a run not left alone, its removed
// worker's among them, with the command to remove each, and removes none: those of a live run's worktree,
// and the project's own, are not its to name. A docker that does not answer is a note.
func TestCleanListsTheContainersPhasesLeft(t *testing.T) {
	x := newCleanRig(t)
	c := x.cleaner(false, nil)
	c.containers = func(context.Context) ([]leftover.Container, error) {
		return []leftover.Container{
			{Name: "a-db-1", Status: "Up 2 hours", Dir: x.worker},
			{Name: "live-db-1", Status: "Up 1 minute", Dir: x.live},
			{Name: "main-db-1", Status: "Up 3 hours", Dir: x.repo},
			{Name: "stray-db-1", Status: "Exited (0) 1 hour ago", Dir: filepath.Join(x.stray, "deploy")},
		}, nil
	}
	var out bytes.Buffer
	c.clean(&out)
	report := out.String()
	for _, want := range []string{
		"container a-db-1 (Up 2 hours), which compose started in " + x.worker + ": docker rm -f a-db-1",
		"container stray-db-1 (Exited (0) 1 hour ago), which compose started in " + filepath.Join(x.stray, "deploy") + ": docker rm -f stray-db-1",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "live-db-1") || strings.Contains(report, "main-db-1") {
		t.Errorf("the report names a container not baton clean's:\n%s", report)
	}

	x = newCleanRig(t)
	c = x.cleaner(true, nil)
	c.containers = func(context.Context) ([]leftover.Container, error) {
		return nil, errors.New("docker ps: Cannot connect to the Docker daemon")
	}
	out.Reset()
	c.clean(&out)
	if !strings.Contains(out.String(), "  · leftover containers: docker ps: Cannot connect to the Docker daemon\n") {
		t.Errorf("the report:\n%s", out.String())
	}
}
