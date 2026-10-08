baton runs an approved multi-phase plan phase by phase in this session, with no human needed at phase boundaries. Between phases it compacts the context, gives you a brief for the next phase, and wakes you up. It works through a small command-line tool, `baton`, which ships in this plugin's `bin/` directory, so it is on the Bash tool's PATH. Every `baton` command only reads and writes the project's `.baton/` directory.

Act on the first word of the arguments. If there is none, treat it as `help`.

## help

Explain briefly:
- `/baton plan <goal>`: plan the work in plan mode. Once the user approves the plan, baton runs it.
- `/baton run [plan file]`: run a plan that already exists. If this conversation had other turns, baton compacts them away before the first phase.
- `/baton start`: hand this session to baton (same conversation); `/baton plan` and `/baton run` do it when they need to.
- `/baton status`: where the run stands.
- `/baton pause` / `/baton resume`: take or give back the wheel.
- `/baton drop`: drop the run; its plan is set aside, and baton does nothing more until the next plan.
- `/baton exit`: leave baton; this conversation goes on as plain Claude Code.
- `/baton drafts`: drafts baton saved out of the input box (`--last` prints the newest).
- `/baton version`: which baton this session runs, the one installed, and the latest release.
- `/baton setup`: check this machine and say what is left to set up.
- `/baton update`: update baton to the latest release (`--check` only looks).

Each session runs its own plan, so several terminals can each run one in the same project.

## status

Run `baton status` and show its output verbatim.

## drafts

Run `baton drafts` (passing through any `--last`, `N` or `--path`) and show its output verbatim.

baton clears an unsent draft rather than waiting on it — a half-typed message used to be able to park a
whole run — and saves the text first. Each draft is a file in the run's `drafts/` folder (`baton drafts
--path` prints it) holding exactly what was typed, so `cat` recovers it even if baton will not start.

## plan <goal>

1. Make sure baton hosts this session, so that baton itself is watching when the user approves the plan: run `baton start "Plan this goal in plan mode, as /baton plan describes: <goal>"`.
   - If it says the session is already hosted by baton, go on to step 2.
   - If it says "starting baton", end your turn immediately (see **start**). Once the session is hosted, you are handed the goal again; then go on to step 2.
   - If it cannot start baton here (the shell does not restart sessions under baton yet, or there is no terminal), tell the user so in one line and go on to step 2. After the approval, attach the plan yourself as described under **run**.
2. Call the `EnterPlanMode` tool and plan the goal as a formal, multi-phase plan. Write the plan so baton can run it:
   - Give each phase its own heading in this exact form: `## P0 — <title>`, `## P1 — <title>`, and so on, in order.
   - Under each heading give the goal, the steps, and when the phase counts as done. Every phase should end with its work committed (in a git repository; otherwise saved).
   - Size each phase to finish comfortably in one context window. Between phases, context is compacted and only the plan, the brief and your notes carry over, so each phase must be understandable from the plan document alone.
   - If the run has rules that apply to every phase, put them in a `## Standing rules` section before the first phase.
   - End with a `## Verification` section.
3. When the user approves the plan, baton attaches it by itself and tells you so. Do what it says: normally that is to end your turn, so baton can compact the planning conversation away and start P0 with a fresh brief. Do not attach the plan yourself, and do not start P0 in the same turn.
   - If baton says the plan was **not attached** (for example, it found no phase headings), attach it as described under **run**.
   - If a run is already under way, baton prints a question for the user about the approved plan. Ask it exactly as printed; baton acts on the answer.

## run [plan file]

1. Find the plan document:
   - the path given in the arguments, or
   - the plan approved in this conversation, or
   - otherwise list `~/.claude/plans/` newest first and ask the user which one with `AskUserQuestion`.
2. Run `baton attach <file> --suggest`. It prints a JSON spec of the phases it found. Check it against the document:
   - every phase is present, in order;
   - ids and titles are right;
   - each `anchor` is a verbatim snippet that starts that phase's section and occurs only once in the document;
   - set `rules_anchor` to the standing-rules heading if there is one, and `end_anchor` to the first heading after the last phase (for example `## Verification`).
3. Attach it with one plain command (no heredocs or pipes, so the allow rule for `baton` covers it):
   - If the suggestion is right as printed: `baton attach <file> --suggested`.
   - Otherwise pass the corrected spec inline, in single quotes: `baton attach <file> --spec '{"title": …, "phases": [ … ]}'`.

   If baton reports a problem with an anchor, fix the spec and try again. If a plan is already running in this session, ask the user before adding `--replace`.
4. If `baton attach` says the session is **not hosted** by baton, nothing would compact automatically, so hand the session to baton instead of starting: run `baton start "The plan is attached. Do what baton says about its first phase."` and end your turn (see **start**).
5. Otherwise, do what `baton attach` says. In a conversation that had turns before this one, that is to end your turn: baton compacts the conversation, then starts the first phase with a fresh brief. Otherwise, begin the first phase.

## pause / resume

Run `baton pause` or `baton resume` and report the result in one line. Pause hands the session to the human: baton stops compacting, nudging and escalating. Resume gives it back to baton and also clears a "blocked" state.

If the conversation grew by more than 20k tokens while baton was paused, `baton resume` says so and asks you to record where the phase stands first. Do exactly that: run `baton checkpoint --notes "…"`, including anything from the paused conversation the rest of the phase needs, then end your turn. baton compacts, and the phase goes on from your notes.

## drop

Run `baton drop` and report the result in one line. The run's plan is set aside (its history stays in baton's log) and baton does nothing in this session until the next plan. The session stays hosted.

## exit

Run `baton exit`.

- **If it says "leaving baton":** end your turn immediately. When the turn ends, baton stops this claude process and the terminal resumes this same conversation as plain Claude Code. A running plan is paused first; relay what baton says about picking it up later.
- **Otherwise** relay its instructions to the user word for word, then stop.

## start

This hands a plain `claude` session to baton without losing the conversation: same session, same context. `/baton plan` and `/baton run` do it when they need to.

Run `baton start "<what you should do next once hosted>"`, for example `baton start "The plan is attached. Do what baton says about its first phase."`, or plain `baton start` if the user gave no next step. Then:

- **If it says the session is already hosted by baton:** say so and stop.
- **If it says "starting baton":** end your turn immediately. When the turn ends, baton stops this claude process. The shell restarts the same conversation under baton, and you will receive the next step.
- **If it says the shell does not restart sessions under baton yet:** relay its two options to the user word for word, then stop.
- **If it says this session has no terminal (desktop app or IDE panel):** tell the user that baton needs a terminal session started with `baton`.

baton will not stop claude while background tasks or subagents are still running. It tries again at the next stop.

## version

Run `baton version`, then `baton update --check`, and show both outputs. If baton hosts this session on an older version than the one installed, say what happens next: the session restarts on the newer one by itself once idle, or at its next phase boundary while a plan runs; after a major update, it stays on its version until the user restarts it (exit, then `baton --continue`).

## setup

Run `baton setup`. It downloads baton's binary on first use and checks this machine: ✓ lines are done, ✗ lines need the user, · lines are notes. Show its output, then explain in plain words what each ✗ line asks the user to do, and mention the optional items:
- **Starting sessions:** start sessions with `baton` instead of `claude`. It accepts the same arguments.
- **Automatic elevation and `baton` on the PATH:** the `eval "$(… init zsh)"` line it prints goes in the shell config (`~/.zshrc`, or `~/.bashrc` with `bash`); then open a new terminal.
- **Push notifications (optional):** put an ntfy topic in `~/.baton/config.json` as `{"ntfy_topic": "<a long random name>"}` and subscribe to it in the ntfy app. Treat the topic like a password.

Do not edit the user's shell config or baton's config yourself unless they ask you to.

## update

Run `baton update` (passing through `--check`) and show its output. It updates the plugin through Claude Code, then downloads and verifies the matching binary. If it updated, relay what it says about the sessions already running, this one included.

## While a plan runs

- Report progress only with `baton done <phase> --notes "…"`, `baton waiting "<what>" --until <duration>` and `baton checkpoint --notes "…"`.
- A run must not stop for something it can work around. When something goes wrong, keep the plan moving on a reversible path, and tell the human in the lightest way that fits:
  - `baton note "<what you did>" --undo "<how to reverse it>"`: you already took a reversible path, and the human should know. The run continues.
  - `baton propose "<what you will do>" --because "<what went wrong>" --undo "<how to reverse it>"`: a rule or convention of the plan can't be followed right now, or a human might choose differently. baton prints the exact question to put to the human; ask it exactly as printed. If nobody answers by its deadline (5 minutes unless the human set another), baton picks Go ahead, and you do it. A proposal is an action that makes progress: waiting, doing nothing or stopping is not a proposal. One proposal covers the case in front of you; when the same thing happens again, `baton note` it each time.
  - `baton blocked "<why>" --tried "<what you tried>"`: only if every way forward is irreversible, or needs a secret only the human holds. The run stops until they answer, and only they can clear it.
- Run each of these as a plain command on its own. Inside double quotes, the shell runs backticks and `$( )` as commands and expands `$NAME`, so keep those out of notes, or use single quotes.
- Background work (a dev server, a build, a background subagent) is not a status. If you stop to wait for it, declare the wait with `baton waiting "<what>" --until <how long it should take>`; waits are capped at 2 hours.
- Don't ask the human questions (AskUserQuestion) of your own while a plan runs: nobody may be there, and the run would wait. Decide and carry on, or use `baton propose`. (baton refuses such questions unless the human started the turn; the questions baton prints for you are the exception.)
- Never type `/compact` yourself. baton does that at the right moment.
- If the human's unsent draft is in the way of a compaction, baton saves it among its drafts and
  clears the input box. That is deliberate and needs nothing from you; if they ask where their message
  went, run `baton drafts`.
- Another baton session may be running its own plan in the same checkout. While one is, baton refuses
  git commands that take every change (`git add -A` or `.`, `commit -a`, `stash`, `reset --hard`,
  `checkout .`, `clean -f`): stage and commit your own files by path, and leave the rest alone.
- One gate is never overridden: an **open permission prompt**. baton cannot answer it and must not type
  through it, so if a run is waiting on one, the human really is the only way forward.
