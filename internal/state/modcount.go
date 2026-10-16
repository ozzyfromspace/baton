package state

// A ModSeq names one report of baton's mod: the host instance of the claude the mod runs in, the mod's
// number for the report, and what the report is, by its kind and the bridge command it names (0: none).
type ModSeq struct {
	Instance string `json:"instance"`
	Seq      int    `json:"seq"`
	Kind     string `json:"kind"`
	Command  int    `json:"command,omitempty"`
}

// CountOnce counts the mod's report id in st by count, which applies its word on the lead's compaction
// (Report, Declined) and reports whether that changed anything, unless id is the report counted last
// (Runtime.ModCounted): then it changes nothing, and reports false. `baton mod report` counts a report in
// the run's state before the bridge records it, so a report process killed between the two leaves the
// report with the mod, which sends it again, and only the first counts. The mod sends its reports one at a
// time, in order, each until the bridge records it, so the report sent again is always the one counted last.
func CountOnce(st *State, id ModSeq, count func(*State) bool) bool {
	if last := st.Run.ModCounted; last != nil && *last == id {
		return false
	}
	st.Run.ModCounted = &id
	return count(st)
}
