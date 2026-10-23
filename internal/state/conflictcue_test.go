package state

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A file in a second landing conflict named by phases not started is recorded as a replan cue, with a
// copy of those phases; one no phase names, or one cued before, is passed over; the cue survives a round
// trip through the state file.
func TestAConflictCuesAReplanOnce(t *testing.T) {
	naming := map[string][]string{"notes.md": {"P5", "P7"}, "go.sum": {"P6"}}
	for _, c := range []struct {
		name  string
		cued  []string
		paths []string
		want  map[string][]string
	}{
		{"named files", nil, []string{"notes.md", "go.sum"}, map[string][]string{"notes.md": {"P5", "P7"}, "go.sum": {"P6"}}},
		{"a file none names", nil, []string{"README.md"}, nil},
		{"a file cued before", []string{"conflict:go.sum"}, []string{"notes.md", "go.sum"}, map[string][]string{"notes.md": {"P5", "P7"}}},
		{"no files", nil, nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := State{ReplanCued: c.cued}
			CueConflict(&st, c.paths, naming)
			if !reflect.DeepEqual(st.ConflictCue, c.want) {
				t.Fatalf("cue %v, want %v", st.ConflictCue, c.want)
			}
			b, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			var back State
			if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back.ConflictCue, c.want) {
				t.Errorf("round trip: %v, %v", back.ConflictCue, err)
			}
		})
	}
	st := State{}
	CueConflict(&st, []string{"notes.md"}, naming)
	naming["notes.md"][0] = "P9"
	if st.ConflictCue["notes.md"][0] != "P5" {
		t.Error("the cue shares its phases with the caller's map")
	}
}
