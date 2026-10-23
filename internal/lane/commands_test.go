package lane

import (
	"reflect"
	"testing"
)

// A here-document's body is no command, whatever it holds (quotes left open, operators, a check's words);
// the command after its closing line is one. Its word comes bare, quoted either way, or after <<-, whose
// closing line may start with tabs.
func TestCommandsSkipAHereDocument(t *testing.T) {
	for _, c := range []struct {
		name, line string
		want       [][]string
	}{
		{"a bare word", "cat <<EOF > a.txt\ngo test ./...\nEOF\nmake test",
			[][]string{{"cat", "<<EOF", ">", "a.txt"}, {"make", "test"}}},
		{"a word in single quotes, its body holding a quote left open", "python3 - <<'EOF' && pytest -x\n# the lead's edit\nprint(\"done\")\nEOF",
			[][]string{{"python3", "-", "<<EOF"}, {"pytest", "-x"}}},
		{"a word in double quotes", "cat <<\"END\"\nx && y; z |\nEND\necho done",
			[][]string{{"cat", "<<END"}, {"echo", "done"}}},
		{"a word after a blank", "cat << 'EOF'\nbody\nEOF\necho after",
			[][]string{{"cat", "<<EOF"}, {"echo", "after"}}},
		{"<<- closes on a line of tabs and its word", "\tcat <<-EOF\n\t\tgo test ./...\n\tEOF\necho after",
			[][]string{{"cat", "<<-EOF"}, {"echo", "after"}}},
		{"<<- closes on no line of spaces and its word", "cat <<-EOF\n  EOF\nEOF\necho after",
			[][]string{{"cat", "<<-EOF"}, {"echo", "after"}}},
		{"<< closes on no line of tabs and its word", "cat <<EOF\n\tEOF\necho hidden\nEOF\necho after",
			[][]string{{"cat", "<<EOF"}, {"echo", "after"}}},
		{"two on a line, their bodies in order", "paste <<A <<B\na1\nA\nb1\nB\necho after",
			[][]string{{"paste", "<<A", "<<B"}, {"echo", "after"}}},
		{"a commit message", "git commit -F - <<'EOF'\nfix: x\n\nRun go test ./... && make e2e\nEOF\ngit log -1",
			[][]string{{"git", "commit", "-F", "-", "<<EOF"}, {"git", "log", "-1"}}},
		{"never closed: the rest is its body", "cat <<EOF\ngo test ./...",
			[][]string{{"cat", "<<EOF"}}},
		{"closed at the line's end", "cat <<EOF\nbody\nEOF",
			[][]string{{"cat", "<<EOF"}}},
		{"a here-string is no here-document", "grep x <<< \"go test\"\nmake test",
			[][]string{{"grep", "x", "<<<", "go test"}, {"make", "test"}}},
		{"no word", "cat <<\nmake test",
			[][]string{{"cat", "<<"}, {"make", "test"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Commands(c.line); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Commands(%q):\n got %q\nwant %q", c.line, got, c.want)
			}
		})
	}
}

// A check after a here-document is found, and one inside its body is not (gap 1: in a real run, a
// background shell that edited a file through python3 - <<'EOF' and then ran the tests went unquestioned).
func TestIsCheckPastAHereDocument(t *testing.T) {
	for _, c := range []struct {
		command string
		want    bool
	}{
		{"python3 - <<'EOF'\nimport pathlib\n# the lead's fix\np = pathlib.Path(\"a.py\")\np.write_text(p.read_text().replace('a', 'b'))\nEOF\ncd backend && pytest tests/test_status.py", true},
		{"cat <<\"EOF\" > notes.md\nIt's done\nEOF\nnpx vitest run", true},
		{"\tcat <<-EOF >> log.txt\n\tdon't\n\tEOF\ngo test ./internal/lane", true},
		{"cat > notes.md <<'EOF'\ngo test ./... runs before every commit.\nEOF", false},
		{"git commit -F - <<'EOF'\nfix(lane): a parser fix\n\nmake test passes again\nEOF", false},
		{"echo $((1<<3))\ngo test ./...", true}, // a shift, no here-document
	} {
		if got := IsCheck(c.command); got != c.want {
			t.Errorf("IsCheck(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}
