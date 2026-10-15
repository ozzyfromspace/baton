package decide

import "testing"

func TestDoneByLeadWords(t *testing.T) {
	what, undo := DoneByLead("P3", "baton/sess-1/P3", "main", 2)
	for _, c := range []struct {
		name, got, want string
	}{
		{"done of a worker's phase with a commit unlanded", WorkerPhaseOpen("P3", "baton/sess-1/P3", "main", 1),
			"not done — P3 is a worker's phase, and its branch baton/sess-1/P3 has 1 commit that is not on main, the run's branch: baton marks P3 done once its agent's own `baton done P3` lands it. " +
				"If its work is finished some other way, merge baton/sess-1/P3 into main first (git merge, never a rebase), then run baton done P3 again."},
		{"done of a worker's phase with commits unlanded", WorkerPhaseOpen("P3", "baton/sess-1/P3", "main", 3),
			"not done — P3 is a worker's phase, and its branch baton/sess-1/P3 has 3 commits that are not on main, the run's branch: baton marks P3 done once its agent's own `baton done P3` lands them. " +
				"If its work is finished some other way, merge baton/sess-1/P3 into main first (git merge, never a rebase), then run baton done P3 again."},
		{"done of a worker's phase whose branch is gone", WorkerPhaseOpen("P3", "baton/sess-1/P3", "main", -1),
			"not done — P3 is a worker's phase, and baton cannot read its branch baton/sess-1/P3: baton marks P3 done once its agent's own `baton done P3` lands its work. " +
				"If its work is finished some other way, merge baton/sess-1/P3 into main first (git merge, never a rebase), then run baton done P3 again."},
		{"the lead's done recorded", what, "recorded P3 as landed, on the lead's baton done: every commit of its worker's branch baton/sess-1/P3 (2 of its own) was on main already, " +
			"so its phase agent is retired, with what it had not committed saved as a snapshot first"},
		{"its undo", undo, "if P3 is not finished, add what it lacks to the plan file as a new phase, which baton then runs; baton status lists the snapshot of what its agent had not committed, " +
			"and git restore --source=<snapshot> --worktree -- . puts it back"},
		{"the lead told", DoneLanded("d4", "P3", "baton/sess-1/P3", "main"), "baton: noted (d4): P3 is done — every commit of its branch baton/sess-1/P3 is on main already, so baton records its work landed. " +
			"baton ends its phase agent and retires it: what it had not committed is saved as a snapshot first (baton status lists it), then its worktree and branch go. The human will be told."},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}
