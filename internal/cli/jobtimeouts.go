package cli

import (
	"time"

	"github.com/ozzyfromspace/baton/internal/lane"
	"github.com/ozzyfromspace/baton/internal/state"
)

// withTimeouts adds to fields, job_finished's for job as it ended, the failures of a red gate that waited
// out a timeout (lane.TimedOut, against ran, how long its command ran): how many as timeout_failures, and
// the seconds they took in all as timeout_seconds. Nothing when its log names none, or cannot be read.
func withTimeouts(job, ended state.Job, ran time.Duration, fields map[string]any) map[string]any {
	if job.Kind == state.JobAwait || !ended.Red() {
		return fields
	}
	fs, _, err := lane.FailureIDs(job.Log, 0)
	if err != nil {
		return fields
	}
	if waited := lane.TimedOut(fs, ran); len(waited) > 0 {
		_, all := lane.Times(waited)
		fields["timeout_failures"], fields["timeout_seconds"] = len(waited), int(all.Seconds())
	}
	return fields
}
