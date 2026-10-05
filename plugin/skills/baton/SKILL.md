---
name: baton
description: Run a multi-phase plan with baton, which hosts this Claude Code session and compacts the context at every phase boundary so a long plan runs unattended. Use when the user runs /baton (plan, attach, status, pause, resume, elevate, setup, help), or asks to run, attach, pause or resume a plan with baton.
---

# /baton

baton runs an approved multi-phase plan phase by phase in this session, with no human needed at phase boundaries. Between phases it compacts the context, gives you a brief for the next phase, and wakes you up. It works through a small command-line tool, `baton`, which ships in this plugin's `bin/` directory, so it is on the Bash tool's PATH. Every `baton` command only reads and writes the project's `.baton/` directory.

The user ran: `/baton $ARGUMENTS`

Act on the first word of the arguments. If there is none, treat it as `help`.

## help

Explain briefly:
- `/baton plan <goal>`: plan the work in plan mode, then run it.
- `/baton attach [plan file]`: run a plan that already exists.
- `/baton status`: where the run stands.
- `/baton drafts`: drafts baton saved out of the input box (`--last` prints the newest).
- `/baton pause` / `/baton resume`: take or give back the wheel.
- `/baton elevate`: hand this session over to baton.
- `/baton setup`: check this machine and say what is left to set up.
- `/baton update`: update baton to the latest release.

## status

Run `baton status` and show its output verbatim.

## drafts

Run `baton drafts` (passing through any `--last`, `N` or `--path`) and show its output verbatim.

baton clears an unsent draft rather than waiting on it — a half-typed message used to be able to park a
whole run — and saves the text first. Each draft is a file under `.baton/drafts/` holding exactly what
was typed, so `cat` recovers it even if baton will not start.

## plan <goal>

1. Call the `EnterPlanMode` tool and plan the goal as a formal, multi-phase plan. Write the plan so baton can run it:
   - Give each phase its own heading in this exact form: `## P0 — <title>`, `## P1 — <title>`, and so on, in order.
   - Under each heading give the goal, the steps, and when the phase counts as done. Every phase should end with its work committed.
   - Size each phase to finish comfortably in one context window. Between phases, context is compacted and only the plan, the brief and your notes carry over, so each phase must be understandable from the plan document alone.
   - If the run has rules that apply to every phase, put them in a `## Standing rules` section before the first phase.
   - End with a `## Verification` section.
2. When the user approves the plan (ExitPlanMode), attach it as described under **attach** below, using the plan file you just wrote, and then start P0.

## attach [plan file]

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

   If baton reports a problem with an anchor, fix the spec and try again. If a plan is already running, ask the user before adding `--replace`.
4. If `baton attach` says the session is **not hosted** by baton, nothing would compact automatically, so elevate instead of starting: run `baton elevate "Begin the first phase of the attached plan."` and end your turn (see **elevate**).
5. Otherwise, begin the first phase.

## pause / resume

Run `baton pause` or `baton resume` and report the result in one line. Pause hands the session to the human: baton stops compacting, nudging and escalating. Resume gives it back to baton and also clears a "blocked" state.

## elevate

Elevation hands this plain `claude` session to baton without losing the conversation: same session, same context. It is only needed when this session was not started with `baton`. If `baton status` already reports a hosted session, say so and stop.

Run `baton elevate "<what you should do next once hosted>"`, for example `baton elevate "Begin the first phase of the attached plan."`. Then:

- **If it says "elevating":** end your turn immediately. When the turn ends, baton stops this claude process. The shell relaunches the same conversation under baton, and you will receive the next step.
- **If it says the shell does not relaunch sessions yet:** relay its two options to the user word for word, then stop.
- **If it says this session has no terminal (desktop app or IDE panel):** tell the user that baton needs a terminal session started with `baton`.

Elevation refuses to stop claude while background tasks or subagents are still running. It retries at the next stop.

## setup

Run `baton setup`. It downloads baton's binary on first use and checks this machine: ✓ lines are done, ✗ lines need the user, · lines are notes. Show its output, then explain in plain words what each ✗ line asks the user to do, and mention the optional items:
- **Starting sessions:** start sessions with `baton` instead of `claude`. It accepts the same arguments.
- **Automatic elevation and `baton` on the PATH:** the `eval "$(… init zsh)"` line it prints goes in the shell config (`~/.zshrc`, or `~/.bashrc` with `bash`); then open a new terminal.
- **Push notifications (optional):** put an ntfy topic in `~/.baton/config.json` as `{"ntfy_topic": "<a long random name>"}` and subscribe to it in the ntfy app. Treat the topic like a password.

Do not edit the user's shell config or baton's config yourself unless they ask you to.

## update

Run `baton update` and show its output. It updates the plugin through Claude Code, then downloads and verifies the matching binary. If it updated, tell the user that sessions already running (this one included) keep the old version until they restart: exit and run `baton --continue`, or start a new session with `baton`.

## While a plan runs

- Report progress only with `baton done <phase> --notes "…"`, `baton blocked "<why>"`, `baton waiting "<what>" --until <duration>` and `baton checkpoint --notes "…"`.
- Run each of these as a plain command on its own. Inside double quotes, the shell runs backticks and `$( )` as commands and expands `$NAME`, so keep those out of notes, or use single quotes.
- Background work (a dev server, a build, a background subagent) is not a status. If you stop to wait for it, declare the wait with `baton waiting "<what>" --until <how long it should take>`; waits are capped at 2 hours.
- Don't ask the human questions (AskUserQuestion) while a plan runs: nobody may be there, and the run would wait. Decide, note your assumption, and carry on, or run `baton blocked "<your question>"` if you truly cannot continue. (baton refuses such questions unless the human started the turn.)
- Never type `/compact` yourself. baton does that at the right moment.
- If the human's unsent draft is in the way of a compaction, baton saves it to `.baton/drafts/` and
  clears the input box. That is deliberate and needs nothing from you; if they ask where their message
  went, run `baton drafts`.
- One gate is never overridden: an **open permission prompt**. baton cannot answer it and must not type
  through it, so if a run is waiting on one, the human really is the only way forward.
