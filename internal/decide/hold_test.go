package decide

import (
	"strings"
	"testing"
	"time"
)

// What the human and the model read when a hold for the human lapses: the lead's proposal or an agent's,
// one that goes ahead or one answered Not now, the last decision a phase may make without the human, and
// a hold with no proposal behind it.
func TestAHoldThatLapsedIsSaid(t *testing.T) {
	after := 20 * time.Minute
	lead := func(outward, last bool) (string, string) {
		return HoldLapsed("d2", "commit P1 unsigned.", "re-sign it", "P1", after, outward, last)
	}
	agent := func(outward, last bool) (string, string) {
		return RelayHoldLapsed("P3.d1", "push the branch", "delete the remote branch", "P3", after, outward, last)
	}
	for _, c := range []struct {
		name      string
		say, tell string
		sayHas    []string
		tellHas   []string
		tellHasnt []string
	}{
		{"the lead's, goes ahead", first(lead(false, false)), second(lead(false, false)),
			[]string{"baton: nobody answered in 20 minutes, so baton lifted the hold on d2 → Claude goes ahead with it, or a more reversible way — commit P1 unsigned"},
			[]string{"[baton] Your proposal d2 (commit P1 unsigned) was held for the human, and nobody has written for 20 minutes, so baton lifted the hold for them",
				"Do what you proposed now, or a more reversible way to the same end", `baton note "<what you did>" --undo "<how to reverse it>"`,
				"what you proposed is undone with: re-sign it.", "Then carry on with P1."},
			[]string{"Not now", "review"}},
		{"the lead's, the last decision", first(lead(false, true)), second(lead(false, true)), nil,
			[]string{"That was the last decision baton lets P1 make without the human: once it is done, end your turn, and baton stops P1 for them to review the decisions."},
			[]string{"Then carry on"}},
		{"the lead's, outward: Not now", first(lead(true, false)), second(lead(true, false)),
			[]string{"baton answered Not now for you on d2 → Claude does it only if it is safely reversible and stays in the project; otherwise it is left for you — commit P1 unsigned"},
			[]string{"so baton answered Not now for them", NotNowRule(), "Then carry on with P1."},
			[]string{"Do what you proposed now", "undone with"}},
		{"an agent's, goes ahead", first(agent(false, false)), second(agent(false, false)),
			[]string{"baton lifted the hold on the proposal P3.d1 of the agent on P3 → the agent goes ahead with it, or a more reversible way — push the branch"},
			[]string{"[baton] Your proposal P3.d1 (push the branch) was held for the human", "undone with: delete the remote branch.", "Then carry on with P3."},
			[]string{"Claude"}},
		{"an agent's, outward: Not now", first(agent(true, false)), second(agent(true, false)),
			[]string{"baton answered Not now for you on the proposal P3.d1 of the agent on P3 → the agent does it only if"},
			[]string{NotNowRule()}, nil},
		{"no proposal behind it", first(HoldLifted("proposal d1 waits for you: x.", "", after)), second(HoldLifted("proposal d1 waits for you: x.", "", after)),
			[]string{"baton: nobody answered in 20 minutes, so baton lifted the hold — proposal d1 waits for you: x"},
			[]string{"[baton] The run was held for the human (proposal d1 waits for you: x), and nobody has written for 20 minutes", "Carry on with the plan."}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, w := range c.sayHas {
				if !strings.Contains(c.say, w) {
					t.Errorf("say lacks %q:\n%s", w, c.say)
				}
			}
			for _, w := range c.tellHas {
				if !strings.Contains(c.tell, w) {
					t.Errorf("tell lacks %q:\n%s", w, c.tell)
				}
			}
			for _, w := range c.tellHasnt {
				if strings.Contains(c.tell, w) {
					t.Errorf("tell has %q:\n%s", w, c.tell)
				}
			}
		})
	}
}

// As baton holds a proposal for the human, it says how long the hold lasts and what it does then.
func TestHoldingSaysWhenItLapses(t *testing.T) {
	if HoldLapse != 20*time.Minute {
		t.Errorf("HoldLapse %s: nothing waits on the human longer than 20 minutes", HoldLapse)
	}
	for _, c := range []struct {
		outward bool
		want    string
	}{
		{false, "baton: holding d1 for you — answer in the session; with no word from you in 20 minutes, baton lifts the hold and Claude goes ahead with it, or a more reversible way"},
		{true, "baton: holding d1 for you — answer in the session; with no word from you in 20 minutes, baton answers Not now: Claude does it only if it is safely reversible and stays in the project"},
	} {
		if got := Holding("d1", HoldLapse, c.outward); got != c.want {
			t.Errorf("outward %v:\n got %q\nwant %q", c.outward, got, c.want)
		}
	}
}

// Not now lets through only the safe kind: reversible, inside the project, and noted.
func TestNotNowRule(t *testing.T) {
	r := NotNowRule()
	for _, w := range []string{"safely reversible and stays inside the project", "pushing your own branch", "a draft pull request", "deleting files you generated",
		"baton note", "irreversible or reaches outside the project", "publishing, deploying, paying, sending, force-pushing, deleting data others rely on",
		"leave it undone for the human", "carry on with everything that does not need it"} {
		if !strings.Contains(r, w) {
			t.Errorf("lacks %q:\n%s", w, r)
		}
	}
}

func first(a, _ string) string  { return a }
func second(_, b string) string { return b }
