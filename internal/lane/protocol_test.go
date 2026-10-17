package lane

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestParseMarker(t *testing.T) {
	cases := []struct {
		line string
		want Marker
		ok   bool
	}{
		{"::baton stage unit start", Marker{Kind: MarkStart, Stage: "unit"}, true},
		{"  ::baton stage go:test start\r\n", Marker{Kind: MarkStart, Stage: "go:test"}, true},
		{"::baton stage unit passed exit=0 seconds=134", Marker{Kind: MarkEnd, Stage: "unit", Status: StagePassed, Seconds: 134}, true},
		{"::baton stage e2e failed exit=1 seconds=12.5", Marker{Kind: MarkEnd, Stage: "e2e", Status: StageFailed, Exit: 1, Seconds: 12.5}, true},
		{"::baton stage lint skipped exit=1 seconds=0", Marker{Kind: MarkEnd, Stage: "lint", Status: StageSkipped, Exit: 1}, true},
		{"::baton stage unit carried exit=0 seconds=0", Marker{Kind: MarkEnd, Stage: "unit", Status: StageCarried}, true},
		{"::baton stage hung failed exit=-1 seconds=3600", Marker{Kind: MarkEnd, Stage: "hung", Status: StageFailed, Exit: -1, Seconds: 3600}, true},
		{"::baton fail TestTheGate/one", Marker{Kind: MarkFail, ID: "TestTheGate/one"}, true},
		{"::baton fail tests/test_x.py::test_a[a b]  ", Marker{Kind: MarkFail, ID: "tests/test_x.py::test_a[a b]"}, true},

		{"", Marker{}, false},
		{"baton stage unit start", Marker{}, false},
		{"echo ::baton stage unit start", Marker{}, false}, // a marker is a line of its own
		{"::baton stage unit", Marker{}, false},
		{"::baton stage unit begin", Marker{}, false},
		{"::baton stage unit start now", Marker{}, false},
		{"::baton stage unit done exit=0 seconds=1", Marker{}, false},
		{"::baton stage unit passed exit=0", Marker{}, false},
		{"::baton stage unit passed seconds=1 exit=0", Marker{}, false},
		{"::baton stage unit passed exit=x seconds=1", Marker{}, false},
		{"::baton stage unit passed exit=0 seconds=-1", Marker{}, false},
		{"::baton stage unit passed exit=0 seconds=NaN", Marker{}, false},
		{"::baton stage unit passed exit=0 seconds=Inf", Marker{}, false},
		{"::baton stage " + strings.Repeat("x", MaxStageName+1) + " start", Marker{}, false},
		{"::baton fail", Marker{}, false},
		{"::baton fail ", Marker{}, false},
		{"::baton fail " + strings.Repeat("x", MaxFailID+1), Marker{}, false},
		{"::baton fail a\x1bb", Marker{}, false},
		{"::baton note hello", Marker{}, false},
		{"::BATON stage unit start", Marker{}, false},
	}
	for _, c := range cases {
		got, ok := ParseMarker(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseMarker(%q) = %+v, %v; want %+v, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}

// The markers baton writes read back as what they say.
func TestMarkersReadBack(t *testing.T) {
	cases := []struct {
		line string
		want Marker
	}{
		{StartMarker("unit"), Marker{Kind: MarkStart, Stage: "unit"}},
		{EndMarker("unit", StagePassed, 0, 2*time.Minute+14400*time.Millisecond), Marker{Kind: MarkEnd, Stage: "unit", Status: StagePassed, Seconds: 134}},
		{EndMarker("e2e", StageFailed, 2, 1600*time.Millisecond), Marker{Kind: MarkEnd, Stage: "e2e", Status: StageFailed, Exit: 2, Seconds: 2}},
		{FailMarker("TestA"), Marker{Kind: MarkFail, ID: "TestA"}},
	}
	for _, c := range cases {
		if got, ok := ParseMarker(c.line); !ok || got != c.want {
			t.Errorf("%q reads as %+v, %v; want %+v", c.line, got, ok, c.want)
		}
	}
}

func TestStagesFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs", "P3.j2.stages")
	if got, err := ReadStages(path); got != nil || err != nil {
		t.Fatalf("no file reads as %v, %v; want none", got, err)
	}
	t0 := time.Date(2026, 10, 17, 14, 2, 11, 0, time.UTC)
	want := []Stage{
		{Name: "lint", Status: StagePassed, Started: t0, Ended: t0.Add(40 * time.Second), Commit: "72b7740"},
		{Name: "unit", Status: StageFailed, Exit: 1, Started: t0.Add(time.Minute), Ended: t0.Add(3 * time.Minute),
			Commit: "72b7740", Failures: []string{"TestA", "tests/x.py::test_b[a b]"}, KeepGoing: true},
		{Name: "build", Status: StageCarried, Commit: "5d1e2aa", From: "j80"},
		{Name: "e2e", Status: StageSkipped, Exit: 1},
	}
	for _, s := range want[:2] {
		if err := AppendStage(path, s); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"name":"torn","sta` + "\n\n") // a writer stopped mid-line, then a blank one
	f.Close()
	for _, s := range want[2:] {
		if err := AppendStage(path, s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadStages(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back\n%+v\nwant\n%+v", got, want)
	}
	if d := got[1].Took(); d != 2*time.Minute {
		t.Errorf("unit took %v, want 2m", d)
	}
	if d := got[2].Took(); d != 0 {
		t.Errorf("a carried stage took %v, want 0", d)
	}
}

func TestHalted(t *testing.T) {
	passed := Stage{Name: "lint", Status: StagePassed}
	going := Stage{Name: "unit", Status: StageFailed, Exit: 1, KeepGoing: true}
	failed := Stage{Name: "e2e", Status: StageFailed, Exit: 1}
	cases := []struct {
		stages []Stage
		want   string
	}{
		{nil, ""},
		{[]Stage{passed}, ""},
		{[]Stage{passed, going}, ""}, // a stage run with --keep-going stops nothing after it
		{[]Stage{passed, going, failed}, "e2e"},
		{[]Stage{failed, {Name: "late", Status: StageSkipped, Exit: 1}}, "e2e"},
	}
	for _, c := range cases {
		s, ok := Halted(c.stages)
		if ok != (c.want != "") || s.Name != c.want {
			t.Errorf("Halted(%v) = %q, %v; want %q", c.stages, s.Name, ok, c.want)
		}
	}
}

func TestFailedRE(t *testing.T) {
	ids := []string{"TestA", "TestA.b", "tests/x.py::test[a+b]"}
	re := regexp.MustCompile(FailedRE(ids))
	for _, id := range ids {
		if !re.MatchString(id) {
			t.Errorf("%s does not match %s", id, re)
		}
	}
	for _, other := range []string{"TestAB", "xTestA", "TestAxb", "tests/x.py::test[aab]"} {
		if re.MatchString(other) {
			t.Errorf("%s matches %s", other, re)
		}
	}
}
