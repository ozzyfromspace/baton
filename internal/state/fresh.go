package state

// A new plan starts fresh (v0.8.17). Reattach used to carry the replaced plan's whole Runtime over and reset
// three fields of it, so the rest leaked into the next plan: CompleteNotified, set as one plan finished,
// kept the next plan's end from ever being told, and in a real run a plan of 5 phases finished with no line
// and no push. So a new plan's Runtime starts from zero and takes over only what freshRun names: the
// session's, the host's or the account's, still true under a new plan. fresh_test.go sorts every field of
// Runtime and of State, and fails on one in neither of its lists, so a field added later must be sorted
// too.

// freshRun is the Runtime of a plan attached over a run whose Runtime was prev: zero, but for the fields
// below, each with why it outlives the plan it was recorded under.
func freshRun(prev Runtime) Runtime {
	return Runtime{
		SessionID:      prev.SessionID,      // the session's own
		PermissionMode: prev.PermissionMode, // the session's
		TurnOpen:       prev.TurnOpen,       // the turn under way goes on: a new plan is often approved in one
		TurnBy:         prev.TurnBy,         // who started that turn
		TurnStarted:    prev.TurnStarted,    // when it started
		LastStop:       prev.LastStop,       // when the session last stopped, which says how long it has been idle
		LastActivity:   prev.LastActivity,   // when its last hook ran
		History:        prev.History,        // the conversation's turns before this one, which it still has
		HumanAt:        prev.HumanAt,        // when the human last took part
		Dialogs:        prev.Dialogs,        // what Claude Code waits on the human for, still on screen
		Gate:           prev.Gate,           // why the host may not type now: the host's, read afresh each tick

		Subagents:        prev.Subagents,        // the session's subagents, still running
		Background:       prev.Background,       // the session's background work, still running
		BackgroundTold:   prev.BackgroundTold,   // the model was told of that work as it started, and need not be again
		BackgroundToldAt: prev.BackgroundToldAt, // when, which says how long each has run
		Monitors:         prev.Monitors,         // the session's Monitor watchers, still running
		Crons:            prev.Crons,            // the session's wakeups, still armed
		Tools:            prev.Tools,            // tool calls whose results have not come back: each may be changing files
		Spawning:         prev.Spawning,         // a phase agent the mod is starting: its call's end clears it
		AgentCalls:       prev.AgentCalls,       // the model's own Agent calls, whose agents may still ask for a worktree
		Plain:            prev.Plain,            // worktrees baton made for the model's own agents, removed as they stop

		Compaction: Compaction{Epoch: prev.Compaction.Epoch}, // its epoch alone: compactions count on across plans
		ModReports: prev.ModReports,                          // the mod's last report of a compaction begun
		ModCounted: prev.ModCounted,                          // the mod's last report counted, which it may send again
		Serial:     prev.Serial,                              // the host's finding that its mod is missing, which another host makes afresh
		Fleet:      prev.Fleet,                               // the host that runs phase agents
		Proven:     prev.Proven,                              // where the lead's transcript stood at the last compaction baton counted
		Notices:    prev.Notices,                             // notices not pushed yet: the human's to get
		Announce:   prev.Announce,                            // lines not shown yet: the human's to see

		Context:         prev.Context,         // the conversation's size, which a new plan does not change
		ContextNoticed:  prev.ContextNoticed,  // the context valves fired for that size, which re-arm as it falls
		ContextNudged:   prev.ContextNudged,   // (the same)
		ContextNudgedAt: prev.ContextNudgedAt, // (the same)
		ContextWarned:   prev.ContextWarned,   // (the same)
		WarnQuestion:    prev.WarnQuestion,    // the context question, the human's to answer, about the same conversation
		WarnOwed:        prev.WarnOwed,        // that question not on screen yet

		LastError:   prev.LastError,   // the API error that ended the last turn: a refusal or a limit stands until the next
		PendingDone: prev.PendingDone, // the elevation's next step, handed once: the relaunched claude still carries it
		PlanFile:    prev.PlanFile,    // the plan approval Claude Code last showed
		ClearedPlan: prev.ClearedPlan, // a plan approved with "clear context", confirmed by the new session's first tool call
		ExitAt:      prev.ExitAt,      // the human asked to leave baton
		Restart:     prev.Restart,     // the newer baton the host is restarting on
		NewerNoted:  prev.NewerNoted,  // the newer baton the human was told of
		StaleSkill:  prev.StaleSkill,  // /baton instructions in the conversation from an older baton
		Ended:       prev.Ended,       // the session ended
		Mark:        prev.Mark,        // where the transcript stood at the last prompt, for a rewind
		Recoveries:  prev.Recoveries,  // the crash loop's recent recoveries, which set the next one's backoff
	}
}
