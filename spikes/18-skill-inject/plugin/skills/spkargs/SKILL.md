---
name: spkargs
description: Spike skill spkargs. Use only when the user runs /spkargs.
allowed-tools: Bash(echo:*)
---
BEGIN-INJECTED
!`echo "ARGS:[$ARGUMENTS]"`
END-INJECTED

Reply with exactly the text between BEGIN-INJECTED and END-INJECTED, verbatim, and nothing else.
