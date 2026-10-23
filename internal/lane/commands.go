package lane

import "strings"

// Commands splits a shell command line into its simple commands, each as its words with the quoting taken
// off, as baton reads a line it runs no shell for (simpleCommands): a here-document's body is no command,
// and the command after its closing line is one.
func Commands(line string) [][]string { return simpleCommands(line) }

// hereDoc is a here-document a command line opened (<<WORD, <<'WORD', <<"WORD", <<-WORD): the line equal
// to word closes its body, after any leading tabs when strip (<<-).
type hereDoc struct {
	word  string
	strip bool
}

// openHereDoc reads the here-document operator at line[i:], which starts with "<<" and is no here-string
// (<<<): the document, the word the command keeps for it ("<<WORD", or "<<-WORD", with its quoting taken
// off), and where the operator's word ends. ok is false where no here-document opens, its word missing or
// not starting with a letter, an underscore or a quote: 1<<3 in an arithmetic expansion shifts bits.
func openHereDoc(line string, i int) (d hereDoc, kept string, end int, ok bool) {
	j := i + 2
	if j < len(line) && line[j] == '-' {
		d.strip = true
		j++
	}
	for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
		j++
	}
	if j == len(line) || !wordStart(line[j]) {
		return hereDoc{}, "", i, false
	}
	var w strings.Builder
	for ; j < len(line) && !strings.ContainsRune(" \t\n;&|()<>", rune(line[j])); j++ {
		switch c := line[j]; {
		case c == '\'':
			k := strings.IndexByte(line[j+1:], '\'')
			if k < 0 {
				k = len(line) - j - 1
			}
			w.WriteString(line[j+1 : j+1+k])
			j += 1 + k
		case c == '"':
			for j++; j < len(line) && line[j] != '"'; j++ {
				if line[j] == '\\' && j+1 < len(line) && strings.IndexByte("\"\\$`", line[j+1]) >= 0 {
					j++
				}
				w.WriteByte(line[j])
			}
		case c == '\\' && j+1 < len(line):
			j++
			w.WriteByte(line[j])
		default:
			w.WriteByte(c)
		}
	}
	if d.word = w.String(); d.word == "" {
		return hereDoc{}, "", i, false
	}
	kept = "<<" + d.word
	if d.strip {
		kept = "<<-" + d.word
	}
	return d, kept, min(j, len(line)), true
}

// wordStart reports whether c may start a here-document's word, as baton reads one.
func wordStart(c byte) bool {
	return c == '_' || c == '\'' || c == '"' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z'
}

// pastHereDocs is where line goes on after the bodies of docs, in order, which start at at, the line after
// the one that opened them: past each one's closing line, or at the line's end where one never closes.
func pastHereDocs(line string, at int, docs []hereDoc) int {
	for _, d := range docs {
		for at < len(line) {
			text, rest, found := strings.Cut(line[at:], "\n")
			next := len(line)
			if found {
				next = len(line) - len(rest)
			}
			if d.strip {
				text = strings.TrimLeft(text, "\t")
			}
			at = next
			if text == d.word {
				break
			}
		}
	}
	return at
}
