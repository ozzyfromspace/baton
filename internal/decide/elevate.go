package decide

import (
	"fmt"
	"strings"
)

// ElevateWaits is what baton says as /baton start in a plain session waits on the model's background work
// (work, what each task is called), which would end with claude: baton restarts the session at a stop
// after it finishes.
func ElevateWaits(work []string) string {
	const most = 3
	var names []string
	for i, w := range work {
		if i == most {
			names = append(names, fmt.Sprintf("%d more", len(work)-most))
			break
		}
		names = append(names, clipTo(oneLine(w), 80))
	}
	return "baton: starting baton waits until background work finishes (it would be stopped with claude): " + strings.Join(names, "; ")
}
