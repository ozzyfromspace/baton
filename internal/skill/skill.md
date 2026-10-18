baton runs an approved multi-phase plan in this session, phase by phase, with no human needed at phase boundaries. Between phases it compacts the context, gives you a brief for the next phase, and wakes you. You work with it through `baton`, a small command-line tool in this plugin's `bin/` directory, so it is on the Bash tool's PATH. Every `baton` command reads and writes only the project's `.baton/` directory.

Act on the first word of the arguments; with none, act as for `help`. If it names no section below, say in one line that baton has no such command, then explain as under `help`.

## help

Explain briefly:
- `/baton plan <goal>`: plan the work in plan mode. Once the user approves the plan, baton runs it.
- `/baton run [plan file]`: run a plan. This session's own plan goes on where it stopped (after a pause or a block, or with phases the user added to its file); any other plan is attached and started. If this conversation had other turns, baton compacts them away before a new plan's first phase.
- `/baton start`: hand this session to baton, keeping the conversation. `/baton plan` and `/baton run` do it when they need to.
- `/baton replan [what to consider]`: restructure the running plan's phases not started yet, so more can run at once, weighing what the user gives; baton checks the result, never changes a phase that started, and records the change with its undo.
- `/baton status`: where the run stands.
- `/baton board`: the plan step by step: how each phase stands and what the lead is doing (the line above the prompt says it in brief, and `/baton-board` opens or closes the board while Claude works).
- `/baton pause`: take the wheel; `/baton run` gives it back.
- `/baton drop`: drop the run. Its plan is set aside, and baton does nothing more until the next plan.
- `/baton clean`: end what no live session uses: phase agents, jobs, lanes, worktrees and leftover files (`--dry-run` only says what it would end).
- `/baton exit`: leave baton; this conversation goes on as plain Claude Code.
- `/baton drafts`: drafts baton saved out of the input box (`--last` prints the newest).
- `/baton version`: which baton this session runs, the one installed, and the latest release.
- `/baton setup`: check this machine and say what is left to set up.
- `/baton update`: update baton to the latest release (`--check` only looks).

Each session runs its own plan, so several terminals can each run one in the same project. While a plan runs, baton may also start phase agents: background agents of this session, each on another phase that can go ahead, in a git worktree of its own, as many as the plan and the machine have room for. Claude Code lists them in the agent panel below the prompt, and baton lands their work on this session's branch when they finish. `@P3 <your words>` at this prompt writes to the agent on P3, which gets them at its next tool call (an idle one wakes), and Claude here never sees them; for a phase no agent works on, the words come to Claude here. Typing in an agent's own view wakes it whatever it is, but baton refuses every tool call of an agent it let go.

## status

Run `baton status` and show its output verbatim.

## board

Run `baton board` and show its output verbatim, in a code block. (Where baton's mod runs, `/baton board` opens or closes the board's pane instead, and this is never asked of you.)

## drafts

Run `baton drafts` (passing through any `--last`, `N` or `--path`) and show its output verbatim.

Where baton runs without its mod (the phases one at a time in this session), it saves an unsent draft that is in its way and clears the input box, unless an agent may be at work; with its mod, the draft stays in the box. Each draft is a file in the run's `drafts/` folder (`baton drafts --path` prints it) holding exactly what was typed, so `cat` recovers it even if baton will not start.

## plan <goal>

1. Make sure baton hosts this session, so that baton itself is watching when the user approves the plan: run `baton start "Plan this goal in plan mode, as /baton plan describes: <goal>"`.
   - If it says the session is already hosted by baton, go on to step 2.
   - If it says "starting baton", end your turn immediately (see **start**). Once the session is hosted, you are handed the goal again; then go on to step 2.
   - If it cannot start baton here (the shell does not restart sessions under baton yet, or there is no terminal), tell the user so in one line and go on to step 2. After the approval, attach the plan yourself as described under **run**.
2. Call the `EnterPlanMode` tool and plan the goal as a formal plan of one or more phases, written so baton can run it:
   - Give each phase its own heading in exactly this form: `## P0 — <title>`, `## P1 — <title>`, and so on, in order.
   - Right under each heading, on a line of its own, say which earlier phases it needs done first: `After: P1, P3`, `After: none`, or `After: all` (every phase before it). A phase without the line follows the phase before it. baton uses these lines to tell which phases can go ahead while another waits, and which ones phase agents can take at the same time.
     - baton works on the phases that can go ahead at the same time, each by a phase agent in a worktree of its own, as many as the machine has room for. What sets how long the plan takes is its longest chain of `After:` lines, not its number of phases, so build a graph, not a list. Shape it by these rules:
       1. **A dependency is a need.** Put a phase `After:` another only if it cannot start without something that phase makes: code, a schema, a file, a decision. Being checked by another phase's tool or gate is not a need: the QA phase checks both. A phase that needs nothing from the phases before it is `After: none`.
       2. **Split enabling work.** When other phases need a small part of a piece of work (an interface, a schema, a local tool, a fixture), give that part a small phase of its own that goes first, and make the rest of the work a leaf that nothing waits on.
       3. **Contract first.** Where two pieces of work meet at an interface, fix the interface in a small phase, then build both sides at once, each `After:` that phase.
       4. **Split by files.** Where phases would edit the same files, divide the work along which files each phase owns rather than chaining it. Only as a last resort, for work that truly shares files, do phases go one after the other (`After:` the earlier one): their work would collide when it lands.
       5. **Outside waits off the main chain.** A phase on the main chain checks itself locally. Waiting on CI, a deploy or an outside service (`baton await`) belongs in a leaf, a QA phase or the release, so that no phase waits on a round trip it does not need.
       6. **A small task can be one phase.** Split work into phases only where it gains something: a fresh context, work at the same time, or a QA phase.
     - Integration phases, phases that verify everything, and release phases use `After: all`.
     - When the plan goes to the user for approval, baton shows its shape in one line: its phases, its longest chain, and how many can run at once. If the longest chain holds most of a long plan's phases, baton asks you once, at your first `ExitPlanMode` (with the check of competing instructions below), to check each link of that chain against these rules. Shorten the chain where a rule allows; where the chain is real, say why in the plan and call `ExitPlanMode` again unchanged.
   - Under each heading, give the goal, the steps, and when the phase counts as done. Every phase should end with its work committed (in a git repository; otherwise saved).
   - Put the heavy checks in **QA phases**: a phase with a line `Type: qa` under its heading (beside its `After:` line) runs the full test suites, lints and end-to-end runs, and fixes what the phases since the last QA phase left incoherent. The phases between QA phases run scoped checks only (the tests of what they change, by name) and may end in a work-in-progress state, so make their "done when" a commit, not a green full suite. Place QA phases at real seams, never per phase, to protect the human's time: at the end, and in a long plan at the end of a group of related phases that later work builds on, each `After:` everything it checks. (The human can turn this off with `aggregate_qa: false`, and then every phase checks itself in full.)
   - Give a spike or an exploration, a phase whose findings should reshape the phases after it, a line `Replan: after` under its heading (beside its `After:` line): once it is done, baton has you replan the phases not started with what it found, at the next phase boundary, reworking their steps or splitting them, never dropping work.
   - Size each phase to finish comfortably in one context window. Between phases the context is compacted, and only the plan, the brief and your notes carry over, so each phase must be understandable from the plan document alone.
   - Put rules that apply to every phase in a `## Standing rules` section before the first phase.
   - End with a `## Verification` section.
   - Before you ask for approval, check the plan against the instructions baton does not control (CLAUDE.md files, MEMORY.md and its memories, skills, settings), and address in the plan anything that competes with it, such as a rule to run the full suite before every commit against its QA phases. baton asks for this, with its gate rules, once at your first `ExitPlanMode`; then call it again.
3. When the user approves the plan, baton attaches it by itself and tells you so. Do what it says. Normally that is to end your turn, so that baton can compact the planning conversation away and start P0 with a fresh brief. Do not attach the plan yourself, and do not start P0 in the same turn.
   - If baton says the plan was **not attached** (for example, it found no phase headings), attach it as described under **run**.
   - If a run is already under way, baton prints a question for the user about the approved plan. Ask it exactly as printed; baton acts on the answer.

## run [plan file]

1. Run `baton run`, adding the plan file if the arguments name one. baton looks at this session's run and says what running means here:
   - **resumed**: the session's plan was paused or blocked, and goes on. Report that in one line. If baton asks you first to record where the phase stands, do exactly that: run `baton checkpoint --notes "…"`, including anything from the paused conversation that the rest of the phase needs, then end your turn. baton compacts, and the phase goes on from your notes.
   - **adds** phases to a plan that had finished: do what it says. Normally that is to end your turn: baton compacts the conversation, then starts the first new phase with a fresh brief.
   - **already running**, **complete**, or **not resumed**: tell the user what it says in one line. If it says to ask which plan to run next, list `~/.claude/plans/` newest first, ask the user with `AskUserQuestion`, and attach their choice from step 3.
   - **another plan is running or paused** in this session: ask the user with `AskUserQuestion` whether to discard that run's progress and run the new plan. Only if they agree, attach it from step 3, adding `--replace` to the attach command.
   - **carried on**: another session left this plan's run with no baton host (Claude Code moved that conversation to its background, say), and this session takes it on where it stood. Report that in one line, then do what baton says next.
   - **attach**: no plan is under way here; go on to step 2.
2. Find the plan document:
   - the path given in the arguments, or
   - the plan approved in this conversation, or
   - otherwise list `~/.claude/plans/` newest first and ask the user which one with `AskUserQuestion`.
3. Run `baton attach <file> --suggest`. It prints a JSON spec of the phases it found. Check it against the document:
   - every phase is present, in order;
   - ids and titles are right;
   - each `anchor` is a verbatim snippet that starts that phase's section and occurs only once in the document;
   - each `after` lists the phases that phase waits for, read from its `After:` line in the plan (without one, the phase before it). To change them, edit the plan, and leave `after` out of a corrected spec;
   - set `rules_anchor` to the standing-rules heading if there is one, and `end_anchor` to the first heading after the last phase (for example `## Verification`).
4. Attach it with one plain command (no heredocs or pipes, so that the allow rule for `baton` covers it):
   - If the suggestion is right as printed: `baton attach <file> --suggested`.
   - Otherwise pass the corrected spec inline, in single quotes: `baton attach <file> --spec '{"title": …, "phases": [ … ]}'`.

   If baton reports a problem with an anchor, fix the spec and try again.
5. If `baton attach` says the session is **not hosted** by baton, nothing would compact automatically, so hand the session to baton instead of starting: run `baton start "The plan is attached. Do what baton says about its first phase."` and end your turn (see **start**).
6. Otherwise, do what `baton attach` says. In a conversation that had turns before this one, that is to end your turn: baton compacts the conversation, then starts the first phase with a fresh brief. Otherwise, begin the first phase.

## replan [what to consider]

1. Run `baton replan`, adding what the user gave to consider, if anything, as one argument in single quotes (write a single quote inside it as `'\''`): `baton replan 'the CI phase can wait until the end'`.
2. Do what it prints, now, in this same turn: it lists the phases not started, the ones that started (never change those), the plan's shape and baton's shape rules, and what this replan may change. Edit only the draft it names, never the plan file, then run `baton replanned --notes "<what changed and why>"`, or `baton replanned --none "<why nothing should change>"` when the plan is right as it is.
3. If `baton replanned` says what failed, fix the draft and run it again. Once it says the plan is replanned, tell the user in one or two lines what changed, then do what it says next.
4. If `baton replan` says no replan was opened (no plan runs here, or every phase has started), tell the user what it says in one line.

## pause

Run `baton pause` and report the result in one line. It hands the session to the human: baton stops compacting, nudging and escalating until `/baton run` gives the session back.

## drop

Run `baton drop` and report the result in one line. The run's plan is set aside (its record is kept, and `baton status` lists it), and baton does nothing more here until the next plan; the session stays hosted.

## clean

Run `baton clean` (passing through `--dry-run`) and show its output verbatim. It ends what no live session in this project uses (the phase agents, jobs, worktrees, lanes and leftover files of runs nobody will come back to), leaves every live run alone, this one included (`/baton drop` first to clean it), and lists what it keeps, with the command to remove each.

## exit

Run `baton exit`.

- **If it says "leaving baton":** end your turn immediately. When the turn ends, baton stops this claude process, and the terminal resumes this same conversation as plain Claude Code. A running plan is paused first; relay what baton says about picking it up later.
- **Otherwise,** relay its instructions to the user word for word, then stop.

## start

This hands a plain `claude` session to baton without losing the conversation: same session, same context.

Run `baton start "<what you should do next once hosted>"`, for example `baton start "The plan is attached. Do what baton says about its first phase."`, or plain `baton start` if the user gave no next step. Then:

- **If it says the session is already hosted by baton:** say so and stop.
- **If it says "starting baton":** end your turn immediately. When the turn ends, baton stops this claude process, the shell restarts the same conversation under baton, and you receive the next step.
- **If it says the shell does not restart sessions under baton yet:** relay its two options to the user word for word, then stop.
- **If it says this session has no terminal (desktop app or IDE panel):** tell the user that baton needs a terminal session started with `baton`.
- **If it says this session is not hosted by baton because it inherited baton's settings** (Claude Code moved it to its background, say): relay the way back it gives (`baton --resume <session id>` in a terminal) to the user word for word, then stop.

baton does not stop claude while background tasks or subagents are still running; it tries again at the next stop.

## version

Run `baton version`, then `baton update --check`, and show both outputs. If baton hosts this session on an older version than the one installed, say what happens next: the session restarts on the newer one by itself once idle, or at its next phase boundary while a plan runs. After a major update, it stays on its version until the user restarts it (exit, then `baton --continue`).

## setup

Run `baton setup`. It downloads baton's binary on first use and checks this machine: ✓ lines are done, ✗ lines need the user, · lines are notes. Show its output, then explain in plain words what each ✗ line asks the user to do, and mention the optional items:
- **Starting sessions:** start sessions with `baton` instead of `claude`. It takes the same arguments.
- **Automatic elevation and `baton` on the PATH:** the `eval "$(… init zsh)"` line it prints goes in the shell config (`~/.zshrc`, or `~/.bashrc` with `bash`); then open a new terminal.
- **Push notifications (optional):** put an ntfy topic in `~/.baton/config.json` as `{"ntfy_topic": "<a long random name>"}`, and subscribe to it in the ntfy app. Treat the topic like a password.

Do not edit the user's shell config or baton's config yourself unless they ask you to.

## update

Run `baton update` (passing through `--check`) and show its output. It updates the plugin through Claude Code, then downloads and verifies the matching binary. If it updated, relay what it says about the sessions already running, this one included.

## While a plan runs

### Reporting progress

- Report progress only with `baton done <phase> --notes "…"` and `baton checkpoint --notes "…"`.
- When the phase is complete and committed, run `baton done` even while its gate still runs. The phase is then verifying: baton marks it done when the gate passes, and the phases that need it wait until then. If the gate fails, baton reopens the phase later, and you resume it from your notes.
- `baton done` is refused while the phase's gate or its latest wait on something outside is red: fix what failed, commit, and run it again. A red gate stands until the same command, or its `baton verify --failed` rerun, passes: a narrower check that passes does not clear it, so verify again with the gate's own command, or make another command the phase's gate with `baton verify --replace`. Only if the failure is not the phase's to fix, `baton done <phase> --despite-red "<why>"` finishes it anyway. Either is a decision made without the human, and they are told.
- Run each `baton` command as a plain command on its own. Inside double quotes, the shell runs backticks and `$( )` as commands and expands `$NAME`, so keep those out of notes, or use single quotes.

### When something goes wrong

- A run must not stop for something it can work around. When something goes wrong, keep the plan moving on a reversible path, and tell the human in the lightest way that fits:
  - `baton note "<what you did>" --undo "<how to reverse it>"`: you already took a reversible path, and the human should know. The run continues.
  - `baton propose "<what you will do>" --because "<what went wrong>" --undo "<how to reverse it>" --wait 5m`: a rule or convention of the plan can't be followed right now, or a human might choose differently. baton prints the exact question to put to the human; ask it exactly as printed. If nobody answers by its deadline (even if the question went away unanswered), baton picks Go ahead, and you do it. If the human answers Wait for me, wait for their word; with none for 20 minutes, baton tells you to go on. An outward or irreversible act (a push, a release, a deploy, a delete, a payment, a message sent, a force-push) is answered Not now at its deadline instead: do it only if it is safely reversible and stays inside the project (pushing your own branch, a draft pull request, deleting files you generated), and `baton note` it; otherwise leave it for the human, and carry on with what does not need it. Give the shortest wait that fits, from 5 to 20 minutes, since a waiting proposal costs the run time: 5m for most, longer only for an action the human would most want to catch. With no `--wait`, or one outside that range, it waits 10 minutes (unless the human set another). A proposal is an action that makes progress: waiting, doing nothing or stopping is not a proposal. One proposal covers the case in front of you; when the same thing happens again, `baton note` it each time.
  - `baton blocked "<why>" --tried "<what you tried>"`: only if every way forward is irreversible, needs a secret only the human holds, or is one the model's safeguards stopped (below). Your phase stops until they answer; only they can clear it. If nobody answers in 30 minutes, baton lifts it: look again for a safe way to complete the phase without dropping work (the parts that do not need the answer first, or a reversible way around), and propose it, which goes ahead if nobody answers. If you are still blocked for the same reason, block again.
- If the model's safeguards stop one of your responses (Claude Code notes it, and lets you go on once), never rephrase, split up or disguise the work to get past them. Go on with what stays clear of it. If the phase cannot go on without it, run `baton blocked "<what the safeguards stopped>" --tried "<what you did instead>"` and end your turn: the human decides how the phase goes on. When the safeguards refuse a turn outright, it ends, and baton tells the human; baton never retries it.
- Don't ask the human questions of your own (AskUserQuestion) while a plan runs: nobody may be there, and the run would wait. Decide and carry on, or use `baton propose`. (baton refuses such questions unless the human started the turn, and even then may answer one for them at its deadline, 10 minutes unless they set another, with the option marked "(Recommended)", or else the first: so put the safe choice there.) The exception is a question baton gives you to put to the human: ask it exactly as given. A phase agent's questions are baton's to ask, not yours.
- One gate is never overridden: an **open permission prompt**, which only the human can answer.

### Long gates and background work

- When the plan has QA phases, a phase that is not one runs focused checks only: full suites, lints and end-to-end runs wait for the QA phase, even where a convention says to run them before every commit. Run one early only for a particular reason, and flag it: `baton verify "<command>" --qa "<why now>" --notes "…"` (or `baton await … --qa`). baton asks about each gate you queue before a QA phase, and each `baton await` (a wait on CI is often a full check in disguise); you decide, and the job runs either way. A red result there may be other phases' work in progress, which the QA phase makes whole: fix what is your phase's, and if the rest is not, finish with `baton done <phase> --despite-red "<why>"`.
- You do not feel the time a gate takes; the human does, and a check that cannot change what ships only spends it. Build a long gate as a committed script of `baton stage <name> -- <command>` steps, which streams each stage, stops at the first failure and tears down what it starts (`trap … EXIT`); when a gate fails, end it with `baton stop <job>` and rerun only what failed with `baton verify --failed` (a gate without stages: `baton verify --failed -- <narrower command>`), never the full suite. To run a whole gate again, say why with `--why "…"`: baton tells the human.
- Don't sit and wait on a long gate: hand it to baton, and keep working on what does not depend on its result.
  - `baton verify "<command>" --notes "<where the phase stands>"` runs the gate on HEAD, in a lane of its own outside the checkout, so commit first; your changes meanwhile cannot disturb it. Use it for gates that take more than about 2 minutes, or that need state of their own (a database, a server, ports). Run quick checks in place.
  - A lane is a separate checkout of the commit: it has none of this checkout's running services, nor its files that are not in git, unless the project's `.baton-lane` script sets them up. When the gate needs this checkout itself and the project has no `.baton-lane`, run `baton verify --here "<command>" --notes "…"`. It runs on HEAD in your own working tree, and until it ends baton refuses edits there, starts no other phase there, and lands no phase agent's work in it. Never write a gate that reaches into the checkout from a lane (a `cd` to it, or its path): baton refuses it, since landings and edits would change the files under it.
  - `baton await "<command>" --notes "…"` waits the same way on something outside the project, from the working tree (a CI run: `gh run watch --exit-status`). It needs no git.
  - A phase runs one gate and one wait on something outside at a time, side by side, and `baton done` waits for both. The same command queued again replaces the one of its kind that runs. Another command of that kind is refused while one runs: add `--replace` to stop the one that runs and run yours, fold both into one command, or queue yours once the result comes.
  - The notes are what the phase resumes from if baton sets it aside while the job runs, so say what is done and what is left.
  - With nothing left to do until the result, commit your work and end your turn: that stop is allowed. If the job runs long (past 5 minutes or the human's `switch_after`, or known to take that long) and another phase can go on, baton compacts and starts that phase; the waiting phase resumes from its notes once the job ends. baton never sets a phase aside with its work uncommitted.
  - `baton checkpoint --notes "…"` is also where baton goes back to a phase whose job has ended: it sets the phase in hand aside with your notes (its work committed) and resumes the other.
  - baton gives you the result when the job ends, waking you if you are idle. A result names the other phases that are red now: failures in their area are theirs, so leave them alone.
  - A lane error is not a test result: the gate never ran on its own terms. Run the gate in place, and `baton note` that you did.
- A command that runs long moves to the background by itself (after 2 minutes, unless the human set `foreground_max`), and wakes you when it ends; so does a background subagent. Never start a loop, a `sleep` or another background task only to wait for one that runs: Claude Code wakes you when it ends. Keep working on what does not depend on it, and with nothing left to do, end your turn; that stop is allowed (on work whose start baton did not see, your first stop is refused once, as a reminder: end your turn again). Your own edits invalidate a gate run that way in the checkout, so a long or stateful gate goes to `baton verify` instead. If background work runs on for 20 minutes with no progress, baton reminds you to check on it.

### Phase agents and other sessions

- baton may run phase agents beside you: background agents of this session that baton starts, each on another phase of the plan, in a worktree and on a branch of its own. Leave their phases and the agents to baton: it starts, messages and stops them, takes their reports, and lands each one's work on your branch, and the phases that need it wait until then.
- If a phase agent's work reached your branch some other way (merged by hand, never by a rebase), `baton done <its phase>` records it landed, as a decision made without the human, and baton retires its agent. While the agent's branch has commits that are not on your branch, baton refuses that and says how to finish the phase.
- When baton lands a phase agent's work, it tells you which commits and files moved under you. Read those files again before you edit them, and never amend, reset, rebase or revert commits you did not make (baton refuses to amend, reset or rebase over them).
- Another baton session may be running its own plan in the same checkout. While one is, baton refuses git commands that take every change (`git add -A` or `.`, `commit -a`, `stash`, `reset --hard`, `checkout .`, `clean -f`): stage and commit your own files by path, and leave the rest alone.

### Compaction

- Never type `/compact` yourself. baton does that at the right moment.
- When baton takes up a phase by itself while your turn runs (a gate's verdict, a landing), it tells you once that the phase comes after this turn: finish what the turn is doing, but don't start that phase in it. baton compacts at the turn's end and gives you the phase's brief.
- baton never loses the human's unsent draft: it compacts and gives you its lines around it, or, without its mod, saves it among its drafts first. If they ask where their message went, run `baton drafts`.
