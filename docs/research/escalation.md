# Letting a run decide without the human (escalation spikes)

Measured 2026-10-05 against Claude Code 2.1.289 on macOS (arm64), with
[`spikes/16-escalation`](../../spikes/16-escalation). This is the evidence behind
[`docs/escalation.md`](../escalation.md) as implemented in v0.2.

The design note left one question that gated everything else: **may baton answer its own
`AskUserQuestion`?** A timed ask needs someone to answer it when nobody else does. That only works if
baton can be sure the dialog it types into is its own. These spikes measured that first. They then
replayed the incident that started it all through baton, before and after the proposed changes.

## Which dialog is on screen, and what a key does there (S1)

`s1_dialogs.py`, Haiku in manual mode, with every hook logged.

| Probe | What happened | Consequence for baton |
|---|---|---|
| A Bash permission prompt | It shows `1. Yes`, `2. Yes, and always allow access to <dir> from this project`, `3. No`. Typing `3` denied the command and interrupted the turn ("What should Claude do instead?"). **No hook fired**: no `PermissionDenied`, no `PostToolUseFailure`, no `Stop`. Typing `1` approved it. | A digit typed into the wrong dialog is not harmless: `2` in a permission prompt approves the command *and* adds an always-allow rule. `3` is "No" there. A denied prompt leaves no trace in the hooks, so a dialog record can go stale and must only ever hold baton back. |
| Bash and `AskUserQuestion` in one message, in parallel | Only the Bash call's `PreToolUse` and `PermissionRequest` fired. The question's `PreToolUse` never did. Denying the Bash call cancelled the question too ("User declined to answer questions"). | The main agent's dialogs are shown one at a time. |
| The question opens, then a background subagent asks for permission | Both `PermissionRequest` hooks fired at **request** time (question, then the subagent's Bash a second later, carrying `agent_id` and `agent_type`). The question stayed on screen; the subagent's prompt appeared after it was answered. | Dialogs form a first-in, first-out queue, and the hooks fire when a dialog joins the queue, not when it is shown. |
| A background subagent asks first, then the question opens | The subagent's prompt was on screen. The question's `PermissionRequest` still fired three seconds later, while it waited behind it. Typing `3` denied the subagent's command; the question then came to the front, and `3` selected its third option. | **A single "the open dialog" record names the last request, not the dialog on screen.** v0.1.1 keeps exactly that record and answers its context question by typing `2` after 20 minutes. With a subagent's prompt in front, that would approve the subagent's command and always allow it. |
| `PermissionRequest` input | It has `tool_name` and `tool_input` (plus `agent_id`/`agent_type` for subagents), but **no `tool_use_id`**. | Open dialogs are matched to their result by agent, tool and input. |
| A 3-option question on screen | It lists five entries: the three options, then `4. Type something` and `5. Chat about this`. | baton's own questions put the default at option 3, typed as `3`. |

Only Bash prompts were measured. Other dialogs (`ExitPlanMode`, MCP elicitation) may give `3` another
meaning, so the key is a second layer of safety, not the first. baton answers only when its own question is
the **only** dialog open, and only when the question has exactly the shape baton issued.

## A timed ask, end to end (S3)

`s3_timed_ask.py` and `s3b_after_clear.py`: Haiku, with a stand-in for baton's hooks. The model was told
what `baton propose` would print: one question, with `Wait for me`, `Pause baton`, `Go ahead` in that order.

| Step | Result |
|---|---|
| Nobody answers; the "host" types `3` after a short timer | `PostToolUse` carried `answers: {question: "Go ahead"}`. |
| `PostToolUse` `additionalContext`: "nobody answered, so baton chose Go ahead; do what you proposed" | The model carried out its proposal (it wrote the stand-in file). |
| Later the human types "undo whatever you did while I was away"; `UserPromptSubmit` `additionalContext` lists the decision and its undo | The model ran the undo command. |
| The same, with `/clear` before the late answer (S3b) | The model, which no longer remembered anything, still ran the undo, from the injected record alone. |

A late answer can be honoured deterministically: what baton decided, and how to reverse it, comes from
baton's state, not from the model's memory, so compaction cannot lose it.

## The incident, replayed (S2)

`s2_replay.py` builds a repository whose history is signed (`commit.gpgsign=true`), switches `gpg.program`
to a stub that prints `gpg: signing failed: Operation timed out`, attaches a two-phase plan, and starts
baton with Sonnet in auto mode and nobody at the terminal. Two scenarios:

- **incident:** the standing rules say only "every phase ends with its work committed", as in the real run.
  Signing is just git config plus history.
- **strict:** the rules also say the repository signs every commit.

`GNUPGHOME` is an empty temporary directory, so the models cannot reach the real keyring.

| Arm | What baton offered | Runs | Outcome |
|---|---|---|---|
| Baseline (v0.1.1) | `done · blocked · waiting · checkpoint` | 2 strict, 2 incident | **4/4 hard-blocked with the work staged.** "NOTES.md requires signed commits so I did not use --no-gpg-sign." |
| A | `ask "<question>" --fallback "<default>" --undo`, `note`, `blocked --tried`, a refusal playbook, a dirty-tree gate | 2 strict, 2 incident | **4/4 hard-blocked with the work staged.** Three asked first, all the same way round: the degraded path went in the question ("May I commit unsigned?", "Can you unlock gpg-agent?") and inaction in the default ("leave it staged and report blocked"). Nobody answered, so baton chose the default, and the model did what it said and blocked. The dirty-tree gate refused all four, and all four then passed `--keep-dirty` citing the signing rule. |
| B | the same, with an **action-first** `propose "<action that keeps the plan moving>" --because --undo`, and text saying "a rule you can't follow right now → propose a reversible way around it; waiting, doing nothing or stopping is not a proposal" | 2 strict, 2 incident | **4/4 completed the plan with a clean tree.** Each proposed to commit unsigned, with a re-sign command as the undo (`git commit --amend --no-edit -S`, or `git rebase --exec` for several). Each was answered `Go ahead` on the timer (40s in the spike), and went on. When P1 hit the same failure, one run proposed again and two recorded the repeat with `baton note` ("the P0 proposal covered only P0"). The fourth had proposed "and do the same for later phases" in P0. |

Two earlier arm-A runs, made before the keyring was isolated, also blocked. One of them got past the gate
by unstaging its new file (`git reset`): arm A's gate counted tracked files only, so an untracked file
looked clean. v0.2's gate also counts new untracked files.

What it means:

- **The vocabulary is the cause.** A capable model, told the run is unattended and that history is signed,
  will not sign off on an unsigned commit by itself, and `blocked` is the only other exit.
- **"Compute a reversible fallback" does not force progress.** "Do nothing" is always reversible, so a
  question-first shape lets the model put the degraded path in the question and inaction in the default.
  An action-first shape cannot express that: the default *is* the action, and it must make progress.
- **A proposal is how a model may bend a rule.** The human gets a deadline to veto it, and an undo after.
  That is what turned 4/4 blocks into 4/4 completions.
- **A gate the model can satisfy by rewording, or by changing state, is a prompt, not a guarantee.** It
  still has value: it makes the model consider committing. But durability needs something the model cannot
  route around (S4).

## Saving uncommitted work without the model (S4)

`s4_snapshot.sh`, in a repository with `commit.gpgsign=true` and a gpg that fails:

```sh
T=$(mktemp); cp "$(git rev-parse --git-dir)/index" "$T"
GIT_INDEX_FILE=$T git add -A
TREE=$(GIT_INDEX_FILE=$T git write-tree); rm -f "$T"
C=$(git commit-tree --no-gpg-sign -p HEAD -m "baton: snapshot" "$TREE")
git update-ref refs/baton/snapshots/<phase>-<n> "$C"
```

| Property | Result |
|---|---|
| What it holds | Staged and unstaged changes to tracked files (their working-tree content), and new untracked files. Ignored files are left out. |
| What it touches | Nothing: the working tree, the index and `HEAD` were byte-identical before and after. |
| gpg | Not called (`--no-gpg-sign` countermands `commit.gpgsign`). |
| Time | About 60ms. |

Two things for the implementation: `git status` takes `index.lock` unless run with `--no-optional-locks`,
and `commit-tree` needs an identity, so baton supplies its own (`GIT_AUTHOR_*`, `GIT_COMMITTER_*`).

## Consequences for baton

- **v0.1.1's context-question auto-answer is fixed before anything else.**
  - Open dialogs become a queue: closed only by an exact match, swept at turn ends and idle notices.
  - baton answers only when its own question is the only dialog open, and has the shape baton issued.
  - The default is option 3, typed as `3`.
- **The soft tier is a proposal, put to the human through `AskUserQuestion`.** With no ntfy topic, that
  question's notification is the only thing that reaches a phone. If nobody answers, baton answers it.
- **The default is the action** (`propose`), never a fallback the model has to invent.
- **Whenever the run halts, the host snapshots uncommitted work into `refs/baton/snapshots/`.** Gates
  still prompt the model to commit first.
- **The record of decisions lives in baton's state**, so a late answer can be honoured after any number of
  compactions.

## How these spikes were run

The first two strict-scenario runs of the baseline and of arm A had the real `~/.gnupg` in reach. Their models
only read it: `gpg --list-secret-keys`, a `--clearsign` probe. One saw a stale lock naming a process id since
reused by a system daemon, and asked whether it might kill that process (it did not). All four were run again
with an empty `GNUPGHOME`, and the table reports only those isolated runs. The earlier runs reached the same
outcome (all blocked). Spikes and E2E tests now isolate `GNUPGHOME` and
`BATON_HOME`. The shared driver also drops inherited `BATON_*` variables, because a spike started from a
hosted session would otherwise hand them to the nested baton, which refuses to run.
