package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/config"
)

func TestPushIsContentFreeByDefault(t *testing.T) {
	var gotTitle, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotTitle, gotBody, gotPath = r.Header.Get("Title"), string(b), r.URL.Path
	}))
	defer srv.Close()
	off := false
	n := New(config.Config{NtfyTopic: "t0p1c", NtfyServer: srv.URL, Desktop: &off})
	if err := n.Notify("my-project", "blocked", "the secret migration plan failed"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/t0p1c" || gotTitle != "baton — my-project" || gotBody != "baton needs you: blocked" {
		t.Fatalf("path %q title %q body %q", gotPath, gotTitle, gotBody)
	}
	n = New(config.Config{NtfyTopic: "t0p1c", NtfyServer: srv.URL, Desktop: &off, Details: true})
	n.Notify("p", "blocked", "need a decision")
	if gotBody != "baton needs you: blocked: need a decision" {
		t.Fatalf("details body %q", gotBody)
	}
}

// A note needs nothing from the human, so it pushes quietly; a proposal has a deadline, and pushes loud.
func TestPriorityFollowsWhatTheHumanMustDo(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Priority"))
	}))
	defer srv.Close()
	off := false
	n := New(config.Config{NtfyTopic: "t", NtfyServer: srv.URL, Desktop: &off})
	for _, kind := range []string{"note", "proposal", "proceeded", "decision", "review", "blocked"} {
		if headline[kind] == "" {
			t.Errorf("%s has no headline", kind)
		}
		n.Notify("p", kind, "")
	}
	if want := []string{"2", "4", "", "4", "4", ""}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("priorities %q, want %q", got, want)
	}
}
