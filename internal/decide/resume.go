package decide

import "fmt"

// CheckpointOnResume is what the model must do first when the human resumed the run after the
// conversation grew while it was paused: record where the phase stands, so the phase goes on from a
// compacted context and a brief that carries those notes, rather than from everything said meanwhile.
func CheckpointOnResume(phase string) string {
	return fmt.Sprintf("The conversation grew while baton was paused, so baton compacts it before %s goes on. "+
		"First record where %s stands: run `baton checkpoint --notes \"<what is done, what is left, and anything from the paused conversation the rest of the phase needs>\"`, then end your turn. "+
		"baton compacts the context, and you continue %s from your notes.", phase, phase, phase)
}
