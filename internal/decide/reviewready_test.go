package decide

import "testing"

// The review notice names each QA phase a review reaches by its handle, and asks nothing.
func TestReviewReady(t *testing.T) {
	for _, c := range []struct {
		qa   []string
		want string
	}{
		{[]string{"P13"}, "Every phase but the QA phase has landed. If you review now, what you find reaches the QA phase in time: " +
			"write it to `@P13`, or add phases. Nothing waits on you."},
		{[]string{"P13", "P14"}, "Every phase but the QA phases has landed. If you review now, what you find reaches the QA phases in time: " +
			"write it to `@P13` or `@P14`, or add phases. Nothing waits on you."},
		{[]string{"Q1", "Q2", "Q3"}, "Every phase but the QA phases has landed. If you review now, what you find reaches the QA phases in time: " +
			"write it to `@Q1`, `@Q2` or `@Q3`, or add phases. Nothing waits on you."},
	} {
		if got := ReviewReady(c.qa); got != c.want {
			t.Errorf("%v:\n got %q\nwant %q", c.qa, got, c.want)
		}
		if got := ReviewReadyLine(c.qa); got != "baton: "+c.want {
			t.Errorf("%v: the line %q", c.qa, got)
		}
	}
}
