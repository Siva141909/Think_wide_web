# AI_USAGE.md

## Tools I used

Claude Code, running on the Opus 5 model, in the terminal, for the whole thing. Didn't bring in ChatGPT or Copilot or anything else this time — just the one tool, used pretty heavily.

## What I used it for

Basically everything: reading through the assignment and starter code, working out the Part 2 design, writing the actual Go service and its tests, running build/vet/test/race over and over to check the work, tracking down the four bugs in Part 3, and drafting these notes. What I did myself was drive it — I had it walk me through its understanding of the assignment before it wrote anything, had it lay out and argue for the specific design decisions before touching any code, and later made it go back and justify a couple of things I wasn't sold on (see below). I didn't type the Go out by hand, but I read all of it, pushed back on parts of the reasoning, and made it change things before they became code — not just skimmed and approved.

## Something I made it change

Early in the design pass, its plan for a missing `contact_id` was to let the event through anyway and just skip it for `unique_opens`, since that's the only stat that actually needs a contact. That felt like the convenient answer rather than one the assignment actually supported, so I told it to go re-read the brief and the starter README before locking it in. Turns out the brief calls out `metadata` as optional and says nothing like that about `contact_id` — so I had it flip the rule: `contact_id` is required now, same as the other core fields, and a missing one rejects the event outright instead of quietly falling out of one metric.

## Something it helped me see more clearly

Running the buggy debugging program five times and diffing the outputs against each other made the race bug concrete instead of theoretical — every single run disagreed with every other one. Combined with `-race` pointing at the exact three lines doing the increments, "this looks racy" turned into something I could point at directly. It also wrote an independent Python version of the correct logic from scratch and checked it against `expected_output.txt` before we touched the Go code at all — that's what actually convinced me the four bugs we'd found were the whole story and not just four things that happened to look plausible.

## Something it wrote that I sent back

One of the concurrency tests (`TestConcurrentConflictingPayloads`) had the math wrong — it assumed that out of 100 concurrent submissions split across two conflicting payloads, 99 would come back as `conflict`. It failed immediately: `conflict = 50, want 99`. The actual correct behavior makes sense once you think about it: whichever payload happens to win the race gets `accepted`, and the other 49 copies of *that same* payload come back as `duplicate`, not `conflict` — only the 50 copies of the losing payload are conflicts. The test just hadn't done that arithmetic. I had it fix the assertion, not the locking code underneath (which was fine), and reran it under `-race` a few times to make sure the corrected expectation actually held up.
