// Package skill holds what the /baton skill does, and finds where a conversation still holds an older
// copy of it.
//
// The plugin's SKILL.md is a stub that loads Text when /baton is invoked (an inline command that runs
// `baton skill`), so each /baton follows the instructions of the baton that carries them out. A copy
// loaded earlier stays in the conversation, though, until a compaction summarizes it away; when a session
// restarts on a newer baton, the hooks hand the model the current Text if InConversation finds one.
package skill

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
)

// Text is the /baton skill's instructions, for every subcommand.
//
// The stub must never pass $ARGUMENTS into the command that prints it: Claude Code substitutes them into
// the command line before the shell runs it, so the human's text would run as shell (spikes/18-skill-inject).
//
//go:embed skill.md
var Text string

var (
	loadMark     = []byte("Base directory for this skill: ")
	boundaryMark = []byte(`"compact_boundary"`)
	loadRE       = regexp.MustCompile(`Base directory for this skill: \S*/skills/baton(\s|$)`)
)

// InConversation reports whether the conversation recorded in transcript (Claude Code's JSONL file)
// holds a copy of /baton's instructions in its context: a /baton load after the last compaction. Claude
// Code records a skill load as a meta user entry starting "Base directory for this skill: <dir>", and a
// compaction as a system entry of subtype compact_boundary.
func InConversation(transcript string) (bool, error) {
	f, err := os.Open(transcript)
	if err != nil {
		return false, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	loaded := false
	for {
		line, err := r.ReadBytes('\n')
		switch {
		case bytes.Contains(line, boundaryMark) && isBoundary(line):
			loaded = false
		case bytes.Contains(line, loadMark) && isLoad(line):
			loaded = true
		}
		if errors.Is(err, io.EOF) {
			return loaded, nil
		}
		if err != nil {
			return loaded, err
		}
	}
}

type entry struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsMeta  bool   `json:"isMeta"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func isBoundary(line []byte) bool {
	var e entry
	return json.Unmarshal(line, &e) == nil && e.Type == "system" && e.Subtype == "compact_boundary"
}

// isLoad reports a /baton skill load: not the phrase quoted in a command or a reply.
func isLoad(line []byte) bool {
	var e entry
	if json.Unmarshal(line, &e) != nil || e.Type != "user" || !e.IsMeta {
		return false
	}
	var text string
	if json.Unmarshal(e.Message.Content, &text) != nil {
		var blocks []struct {
			Text string `json:"text"`
		}
		json.Unmarshal(e.Message.Content, &blocks)
		for _, b := range blocks {
			text += b.Text + "\n"
		}
	}
	return loadRE.MatchString(text)
}
