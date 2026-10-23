package decide

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/ozzyfromspace/baton/internal/lane"
)

// A side log that is not there says so; one that cannot be read says why; either way the stage reads its
// output alone.
func TestStageLogUnread(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("open results/e2e.log: %w", fs.ErrNotExist),
			"baton: stage e2e's log results/e2e.log does not exist, so what failed in it is read from its output alone"},
		{errors.New("open results/e2e.log: permission denied"),
			"baton: stage e2e's log results/e2e.log could not be read (open results/e2e.log: permission denied), so what failed in it is read from its output alone"},
	} {
		if got := StageLogUnread("e2e", "results/e2e.log", c.err); got != c.want {
			t.Errorf("StageLogUnread(%v) =\n%s\nwant\n%s", c.err, got, c.want)
		}
	}
}

func TestStageHaltedQA(t *testing.T) {
	got := StageHaltedQA("e2e", lane.Stage{Name: "build", Exit: 2})
	if want := "■ e2e skipped: stage build failed (exit 2) earlier in this gate, and the stages after it need it (baton stage --halts)"; got != want {
		t.Errorf("StageHaltedQA =\n%s\nwant\n%s", got, want)
	}
}
