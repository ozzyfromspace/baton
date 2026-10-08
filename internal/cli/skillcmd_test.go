package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ozzyfromspace/baton/internal/skill"
)

// Every /baton subcommand the help lists has its own section, and nothing names a retired command.
func TestSkillInstructionsCoverEverySubcommand(t *testing.T) {
	io, out, _ := testIO("", nil)
	if code := Main([]string{"skill"}, io); code != 0 || out.String() != skill.Text {
		t.Fatalf("baton skill: code %d", code)
	}
	help := skill.Text[strings.Index(skill.Text, "## help"):strings.Index(skill.Text, "## status")]
	listed := regexp.MustCompile("`/baton ([a-z]+)").FindAllStringSubmatch(help, -1)
	if len(listed) < 10 {
		t.Fatalf("help lists %d subcommands:\n%s", len(listed), help)
	}
	for _, m := range listed {
		if !regexp.MustCompile(`(?m)^## (` + m[1] + `\b|[a-z]+ / ` + m[1] + `\b)`).MatchString(skill.Text) {
			t.Errorf("/baton %s is listed but has no section", m[1])
		}
	}
	for _, retired := range []string{"elevate", "`baton stop", "/baton stop", "/baton attach", "/baton resume", "`baton resume", "$ARGUMENTS"} {
		if strings.Contains(skill.Text, retired) {
			t.Errorf("the instructions mention %q", retired)
		}
	}
}

// The plugin's skill is a stub that loads the instructions from the binary. Claude Code substitutes
// $ARGUMENTS into an inline command before the shell runs it (spikes/18-skill-inject), so no inline
// command may contain a $ at all.
func TestSkillStubLoadsTheInstructionsSafely(t *testing.T) {
	b, err := os.ReadFile("../../plugin/skills/baton/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	stub := strings.ReplaceAll(string(b), "\r\n", "\n") // a Windows checkout has CRLF line endings
	if !strings.Contains(stub, "\n!`baton skill`\n") || !strings.Contains(stub, "\nallowed-tools: Bash(baton skill)\n") {
		t.Fatalf("the stub does not load baton skill with permission to:\n%s", stub)
	}
	for _, cmd := range regexp.MustCompile("!`([^`]*)`").FindAllStringSubmatch(stub, -1) {
		if strings.Contains(cmd[1], "$") {
			t.Errorf("inline command %q would run the human's arguments as shell", cmd[1])
		}
	}
	if len(stub) > 2000 {
		t.Errorf("the stub has grown to %d bytes: instructions belong in internal/skill/skill.md", len(stub))
	}
}
