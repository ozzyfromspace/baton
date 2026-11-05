package decide

// BackgroundHabits is how a phase keeps its background work easy to judge, for baton's reviewer and for a
// model alike, so that honest work is never taken for a stuck task, nor a stuck task for honest work: checks
// and waits on something outside go to baton's jobs, never a background shell; a background shell's
// description names what it is for, its phase and when it ends (a duration or a condition); it prints as it
// goes, so silence means stuck; and it is stopped once its job is done. Every brief carries it beside
// GateHabits, and a background shell's start reminder ends with it (BackgroundStarted). Without git (git
// false) there is no lane, so checks go through baton await too. Each form stays under 450 characters.
func BackgroundHabits(git bool) string {
	jobs := "Background shells: run a check with `baton verify` and a wait on something outside the project with `baton await`, never in a background shell. "
	if !git {
		jobs = "Background shells: run a check, or a wait on something outside the project, with `baton await`, never in a background shell. "
	}
	return jobs +
		"Describe each background shell by what it is for, its phase and when it ends: `~15m`, or `until P12's browser checks pass`. " +
		"Have it print a line every few minutes at least (a verbose runner, an `echo` per step), so silence means stuck. " +
		"Stop it once its job is done, and before `baton done`."
}

// NoEndDeclared is what a background shell's start reminder adds when its description, description,
// declares no end: neither a run time (`~15m`) nor a condition (`until …`). baton's reviewer reads what a
// shell declares, and so does the human, so one with no end can only be judged by its silence.
func NoEndDeclared(description string) string {
	if description == "" {
		return "[baton] This shell declared no end: it has no description. Give each background shell a description naming what it is for, its phase and when it ends (`~15m`, or `until <a condition>`), since baton's reviewer and the human judge it by what it declares."
	}
	return "[baton] This shell declared no end: its description (" + description + ") names neither a run time (`~15m`) nor a condition (`until <a condition>`). Give each background shell one, since baton's reviewer and the human judge it by what it declares."
}
