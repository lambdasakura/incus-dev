---
name: review-round
description: Use when asked to code-review the incus-dev repository, to run a review round, or to keep reviewing until no findings remain. Covers choosing a lens that has not been used, briefing read-only review agents, verifying findings before acting, and knowing when a round is finished. Reach for it whenever the request is to review rather than to build.
---

# Running a review round on incus-dev

Two dozen rounds have run on this repository. What they showed:

- **Coverage says nothing.** Every round ran at 99% line coverage. Every round
  found real defects, including data loss and an instance that could not be
  deleted.
- **The lens decides what you find.** Repeating a lens finds little. A lens
  nobody has used finds several, immediately.
- **The last round's fixes are the most likely place for the next defect.**

## Pick a lens that has not been used

Reviewing "the code" again produces less each time. Reviewing it *as* something
produces findings on the first pass. Lenses already used, and what each found:

| Lens | Found |
| --- | --- |
| General code review | crashes, a secret in a log |
| Output text, read as a user | warnings that stated the opposite of the truth |
| Spec conformance (`docs/spec/`) | six rounds of implementation drift |
| **Mutation testing** | **24 behaviours executed but never asserted** |
| Executing the documentation | a shipped example that fails on first run |
| Hostile input and interference | data orphaned, an instance made undeletable, Ctrl-C dead |
| Idempotency and convergence | a state that never converged, whose advice killed the user's work |
| Upgrade from an older idev | an environment silently stranded |
| Concurrency and resource lifetime | a data race, Ctrl-C hanging for two seconds |
| Secret handling, from the inside | nothing — a clean result |
| Build, dependency and release | a release archive missing files its own docs use |
| Regression check on the last round | something every single time |
| The test machinery, not the code | four rounds of defects in the guards themselves |

Untried lenses worth a round: API round-trip efficiency (measured once, no
finding), cross-package API design, error-message quality end to end,
accessibility of the output to non-interactive consumers.

**Mutation testing is the highest-yield lens.** Copy the repo to `/tmp`, apply
20–40 targeted mutations one at a time (invert a condition, drop a call, swap
`&&`/`||`, move a boundary, delete a branch), run the suite, and record which
survive. Never mutate the working tree. A survivor is proof of an unasserted
behaviour.

## Brief the agents to reproduce, not to speculate

Two read-only agents per round is enough; one of them checks the previous
round's commits for regressions. In the brief:

- read-only, scratch files in `/tmp` and never in the repo, `git status` clean
  at the end
- unit tests only unless the lens needs a real daemon; if it does, prefix every
  instance with the round number and delete them all before finishing
- **prove every finding with a test or command actually run; report nothing
  unreproduced**
- "**If you find nothing real, say exactly that** — a clean report is a useful
  result, and padding is worse than an empty list"

Without that last line a report fills to its limit with speculation.

## Verify every finding yourself

Agents are wrong sometimes: some findings are stale, some are already-decided
design, some are real but misattributed. Reproduce each one before acting. Ones
correctly rejected in this history include a device without a `type` (the
schema requires it) and a volume `size` warning (it would fire on every run).

Then follow the `fix-finding` skill for each one that survives.

## Finish the round

```bash
make check   >/dev/null 2>&1; echo "check exit=$?"
make cover   >/dev/null 2>&1; echo "cover exit=$?"
go test -race -count=1 ./... >/dev/null 2>&1; echo "race exit=$?"
make test-integration > /tmp/int.log 2>&1; echo "integration exit=$?"
```

Exit codes, and keep the full log. Then review the round's own commits — that
check found a defect in fourteen rounds out of fifteen.

## What "no findings remain" can and cannot mean

It cannot mean no defect exists; that is not demonstrable, and saying so is
honest rather than evasive. It can mean: every finding raised is closed, every
lens named is executed, and the round's own output has been reviewed. State
which lenses were used and which were not, so the next person can choose.
