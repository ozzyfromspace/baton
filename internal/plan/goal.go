package plan

import "strings"

// Goal is p's goal sections in doc as one text, as the risk assessment reads them: the title and the text
// before the first phase, the standing rules, and the part from the end anchor on, each left out when doc
// has none.
func (p Plan) Goal(doc []byte) string {
	g := p.goal(doc)
	var parts []string
	for _, k := range []string{GoalPreamble, GoalRules, GoalEnd} {
		if s := strings.TrimSpace(g[k]); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}
