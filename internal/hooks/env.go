package hooks

import (
	"github.com/ozzyfromspace/baton/internal/decide"
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// jobEnv is what the log at path of job j says of the gate's environment, for its result: for a red gate
// whose log shows an environment failure and no test failure (lane.Environment), what failed it and the
// line that says so, with the run's environment facts so far (baton env); nil for any other job. The
// run's state is read without its lock.
func jobEnv(s *state.Store, j state.Job, path string) *decide.Env {
	if j.Kind != state.JobVerify || !j.Red() {
		return nil
	}
	m := lane.Environment(path)
	if m.Kind == "" {
		return nil
	}
	st, _ := s.Peek()
	return &decide.Env{Kind: m.Kind, Line: m.Line, Facts: s.Facts(st)}
}
