package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Drafts are what baton takes out of the input box so that a half-typed message can never hold a run.
//
// The old behaviour was to wait: a draft gated every keystroke baton wanted to send, and the gate only
// ever reported itself to the human. Measured 2026-10-05, that parked a plan with a compaction queued
// for 35 minutes, and nothing but a person noticing could have cleared it. A run that has to be watched
// is not unattended, so baton now saves the text and clears the box.
//
// Each draft is one file holding EXACTLY what was typed and nothing else — no header, no JSON, no
// escaping — because the whole point is to hand it back. `cat` recovers it even if baton cannot start,
// which is the property a recovery path has to have.

// DraftsDirName is the subdirectory of .baton that holds saved drafts.
const DraftsDirName = "drafts"

// DraftsDir is where saved drafts live.
func (s *Store) DraftsDir() string { return s.path(DraftsDirName) }

// Draft is one saved draft.
type Draft struct {
	Path  string
	At    time.Time
	Phase string
	Text  string
}

// Preview is the draft's first line, shortened for a listing.
func (d Draft) Preview(width int) string {
	line := d.Text
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if width > 1 && len(line) > width {
		return line[:width-1] + "…"
	}
	return line
}

// draftStamp names a file: sortable, filename-safe on every platform, and readable.
func draftStamp(at time.Time) string { return at.UTC().Format("20060102T150405Z") }

// SaveDraft writes text to a new file in the drafts directory and returns its path. An empty or
// whitespace-only draft is not saved and returns "" — a phantom draft (the key tracker errs toward
// "there is a draft", so it can drift there on its own) has nothing worth keeping, and saving empty
// files would bury the real ones.
func (s *Store) SaveDraft(text, phase string, at time.Time) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	dir := s.DraftsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := draftStamp(at)
	if phase != "" {
		name += "-" + phase
	}
	path := filepath.Join(dir, name+".txt")
	// Never overwrite: two drafts inside one second are rare and losing one is the thing being fixed.
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.txt", name, i))
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Drafts lists saved drafts, newest first.
func (s *Store) Drafts() ([]Draft, error) {
	entries, err := os.ReadDir(s.DraftsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Draft
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		path := filepath.Join(s.DraftsDir(), e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue // a draft we cannot read must not hide the ones we can
		}
		stem := strings.TrimSuffix(e.Name(), ".txt")
		at, phase := time.Time{}, ""
		if parts := strings.SplitN(stem, "-", 2); len(parts) > 0 {
			at, _ = time.Parse("20060102T150405Z", parts[0])
			if len(parts) == 2 {
				phase = parts[1]
			}
		}
		out = append(out, Draft{Path: path, At: at, Phase: phase, Text: string(b)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return out[i].Path > out[j].Path
		}
		return out[i].At.After(out[j].At)
	})
	return out, nil
}
