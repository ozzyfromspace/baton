package decide

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// StageLogUnread is the line of a failed stage whose side log (baton stage --log, path as given) could not
// be read: what failed in it is read from its output alone.
func StageLogUnread(name, path string, err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("baton: stage %s's log %s does not exist, so what failed in it is read from its output alone", name, path)
	}
	return fmt.Sprintf("baton: stage %s's log %s could not be read (%v), so what failed in it is read from its output alone", name, path, err)
}

// StageHaltedQA is the line of a stage of a QA phase's gate that does not run because an earlier stage the
// later ones need failed (baton stage --halts): in a QA phase, every other failure lets the stages after it
// run, so that one run collects every red.
func StageHaltedQA(name string, failed lane.Stage) string {
	return fmt.Sprintf("■ %s skipped: stage %s failed (exit %d) earlier in this gate, and the stages after it need it (baton stage --halts)",
		name, failed.Name, failed.Exit)
}
