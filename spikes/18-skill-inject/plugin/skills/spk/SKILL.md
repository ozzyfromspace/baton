---
name: spk
description: Spike skill spk. Use only when the user runs /spk.
allowed-tools: Bash(spkbin:*)
---
The user ran: `/spk $ARGUMENTS`

BEGIN-INJECTED
!`spkbin`
END-INJECTED

Reply with exactly the text between BEGIN-INJECTED and END-INJECTED, verbatim, and nothing else.
