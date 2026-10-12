# Spike 33: `/baton board` beside the /baton skill

Can baton's mod serve `/baton board`, opening its pane at once, when `/baton` is the plugin's own skill?
(2026-10-12, Claude Code 2.1.296, print mode, Haiku; `./run.sh`.) The probe plugin `bt` has a skill `/bt`
and a mod that, by `PROBE_MODE`, registers the command `/bt` or `/bt-board`, or none, and hooks every
`command.run`, answering `{ text }` when the arguments are `board`.

| Question | Finding |
|---|---|
| May a mod register a command named like its plugin's skill? | **No.** `$.command.register({ name: 'bt' })` is refused: `"/bt" refused: it is the plugin's /bt:bt`. |
| What does a `command.run` hook see when the skill's command is typed? | `command: "bt:bt"` (the plugin's name, then the skill's) and `args: "board"`. |
| Can the mod answer the skill's command itself? | Yes: its `{ text }` was the command's output (`bt: MOD-ANSWERED board`), and the skill never ran, so no model was called. |
| May it register another name? | Yes: `/bt-board` registered (`{ command: "bt-board" }`). |

So baton's mod answers `/baton board` (the skill's `baton:baton` with the argument `board`) by opening
the board's pane, and leaves every other `/baton` command to the skill. A skill's command is not
`immediate`: typed while Claude works, Claude Code holds it until the turn ends. For the board while
Claude works, the mod registers `/baton-board` with `immediate: true`, which opens the same pane at once.
