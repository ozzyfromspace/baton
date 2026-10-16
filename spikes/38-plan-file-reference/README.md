# 38 — the plan-mode file out of Claude Code's way (Claude Code 2.1.301, Haiku, macOS arm64, 2026-10-16)

Question: after every compaction, Claude Code adds the session's plan file in full to the conversation (an attachment
entry of type `plan_file_reference`, "A plan file exists from plan mode at: … Plan contents: …"), on top of baton's
brief, which already quotes the phase, the standing rules and the notes. In a real run that was about 32k tokens of
every phase's starting context. No setting turns it off. Does renaming the session's plan-mode file,
`<plansDirectory>/<slug>.md`, stop it? What does the session do after: does a later planning write a fresh file, and
does anything else break?

Harness: `run.py <workroot>` runs an interactive claude in a PTY (stdlib only, `../common/scenario.py`) in a fresh git
repo whose `.claude/settings.json` sets `plansDirectory` to `plans` (Claude Code wants it inside the project) and logs
the hook events of interest (`hook.py`); no user settings (`--setting-sources project,local`), no baton variables in
the child's environment. It plays the human: plan mode, a one-phase plan, approved with Enter; `/compact` (the
control); the plan-mode file renamed in its folder; a turn; `/compact`; a turn; plan mode again, a second plan,
approved; `/compact`. Then it reads the transcript: per compact boundary, the attachments written after it.

| boundary | the plan-mode file | attachments after it |
|---|---|---|
| 1 (control) | `plans/plan-this-tiny-task-humming-nebula.md`, as approved | `plan_file_reference` with its path and its 69 characters, among 16 others |
| 2 | renamed to `plans/spike-38-plan.md` | **no `plan_file_reference`**; the other attachments as before |
| 3 | written afresh by the second planning | `plan_file_reference` again, of the new file (73 characters) |

- **The rename stops it.** After the rename, the next compaction attached no plan, and nothing else changed: the turn
  after the rename and the one after the compaction ran as before, with no error on screen or in the transcript.
- **A later planning writes a fresh file at the same path,** and leaves the renamed one alone (its hash unchanged).
  Plan mode told the model "No plan file exists yet. You should create your plan at `<plansDirectory>/<slug>.md`":
  the model does not see the renamed plan unless something tells it where it is. The second approval named the same
  path (`tool_response.filePath`), so an approval of that path is the session's next plan.
- **The attachment came back with the file** (boundary 3), which is the control the other way round.

From the code (2.1.301, read only): the compaction's post-compact attachments read the plan from
`<plansDirectory>/<slug>.md` (`<slug>-agent-<id>.md` for a subagent) on disk at each compaction, and add the
reference only when the file has content. The slug is the session's and is written on its transcript entries
(`"slug"`); 2.1.301 starts it with words of the session's first prompt (`plan-this-tiny-task-humming-nebula`). It can
change during a session's life: in a real lead's transcript (this plan's own run, resumed after a restart), the slug
changed at a compaction, after which the plan file of the old slug was no longer attached, and the new slug's file was
attached at each of the six compactions after the next planning wrote it (27,884 to 32,851 characters each).

The session was not hosted: the attachment is Claude Code's own, and what it reads (the slug and the folder) is the
same under baton. A hosted run, where baton moves the plan as it attaches it and the mod compacts the lead at the
phase boundary, is the e2e scenario of P11.

## What it means for baton (v0.8.6)

- **baton moves the plan out of the way as it attaches it** (`state.Aside`, `plan.MoveAside`): when the plan file is
  the session's plan-mode file (its base name is the slug the session's transcript carries, plus `.md`) and no other
  live run's plan, it is renamed in its folder to a name from the plan's title (`plan.AsideName`, made unique, never
  over a file), and the run follows it there. `plan.json` keeps the new path as the plan file and the old one as its
  `plan_origin`; the move is announced (`decide.PlanMoved`) and logged (`plan_moved`, or `plan_not_moved` with why).
- **An approval of the origin is a revision of the run's plan,** as an approval of the run's own file was: Claude
  Code writes the session's next plan there. It is moved aside in its turn as it is approved, and the run keeps the
  origin.
- **A run attached before baton did this** (an earlier baton's) has its plan moved at the lead's next compaction that
  starts a phase, once (`State.PlanLooked`), before that compaction would attach the file once more.
