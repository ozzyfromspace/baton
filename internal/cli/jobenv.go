package cli

import (
	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// withEnvironment adds to fields, job_finished's for job as it ended, what failed a red gate whose log
// shows an environment failure and no test failure (lane.Environment): its kind as environment, and the
// line that says so as environment_line.
func withEnvironment(job, ended state.Job, fields map[string]any) map[string]any {
	if job.Kind != state.JobVerify || !ended.Red() {
		return fields
	}
	if m := lane.Environment(job.Log); m.Kind != "" {
		fields["environment"], fields["environment_line"] = m.Kind, m.Line
	}
	return fields
}
