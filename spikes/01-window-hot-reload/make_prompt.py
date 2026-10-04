import random
random.seed(7)
words = ("river stone harbor lantern meadow copper violet thunder pencil garden orbit falcon velvet marble "
         "canyon ember willow saddle beacon quartz meadow ripple cobalt timber anchor glacier hollow prism "
         "summit thistle walnut basalt cinder fern juniper kettle ledger mosaic nectar oyster pewter quill").split()
paras = []
for i in range(1100):
    paras.append(f"[ref {i}] " + " ".join(random.choice(words) for _ in range(80)))
filler = "\n".join(paras)
task = """

=== TASK ===
The reference material above is background noise; do not summarize or analyze it.
Phase 1 steps, using the Bash tool, one command per call:
  1. run `echo P1-work`
  2. run `echo PHASE1_DONE`
Then end your turn with the single line: "phase 1 complete".
After that, follow any further instructions you receive.
"""
open('prompt.txt', 'w').write("REFERENCE MATERIAL:\n" + filler + task)
print(len(filler), "filler chars")
