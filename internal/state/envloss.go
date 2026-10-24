package state

import (
	"slices"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// A stage the machine killed is the machine's (v0.8.17): a whole run of a gate that lost a stage to its
// environment (lane.StageEnvironment) says so in its result (decide), the next whole run of its command is
// questioned once (cli/envagain.go), and when a second whole run of a phase's gate loses a stage to the same
// kind, the human is pushed once per phase and kind (envrepeat.go).

// EnvLoss is a stage a gate run lost to its environment (lane.StageEnvironment): its name, what took it
// (lane.EnvKilled, say), and the log's line that says so.
type EnvLoss struct {
	Stage string `json:"stage"`
	Kind  string `json:"kind"`
	Line  string `json:"line,omitempty"`
}

// Losses are the stages of m, as StageEnvironment found them, in the order the gate ran them (stages).
func Losses(m map[string]lane.EnvMatch, stages []lane.Stage) []EnvLoss {
	var out []EnvLoss
	for _, s := range stages {
		if e, ok := m[s.Name]; ok && e.Kind != "" && !slices.ContainsFunc(out, func(l EnvLoss) bool { return l.Stage == s.Name }) {
			out = append(out, EnvLoss{Stage: s.Name, Kind: e.Kind, Line: e.Line})
		}
	}
	return out
}

// EnvLost reads the stages job j's environment took, from its log and its stages as recorded (StagesOf), in
// the order they ran. It reads files, so it is called outside the run's lock.
func (s *Store) EnvLost(j Job) []EnvLoss {
	stages := s.StagesOf(j)
	path := j.Log
	if path == "" {
		path = s.JobLog(j.ID)
	}
	return Losses(lane.StageEnvironment(path, stages), stages)
}

// WholeRun reports a gate run of its whole command: a verify job that reruns nothing (Reruns) and narrows
// nothing (Narrows). A landing's re-verification and a QA phase's confirmation are whole runs too.
func WholeRun(j Job) bool { return j.kind() == JobVerify && j.Reruns == "" && len(j.Narrows) == 0 }

// LastWhole is the whole run of command in st that ended last, whatever its end but a lane error (it never
// ran); nil for none.
func (st State) LastWhole(command string) *Job {
	var last *Job
	for i := range st.Jobs {
		j := &st.Jobs[i]
		if WholeRun(*j) && j.Command == command && j.Finished() && j.Status != JobLaneError && (last == nil || !j.Ended.Before(last.Ended)) {
			last = j
		}
	}
	return last
}
