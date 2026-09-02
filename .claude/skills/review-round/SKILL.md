---
name: review-round
description: Use when asked to code-review the incus-dev repository, to run one review round, or to run rounds in a loop - including any instruction of the form "keep reviewing and fixing until there are no findings". Covers what the loop carries between rounds, choosing a lens that has not been used, briefing read-only review agents, verifying findings before acting, and when to stop - which is not at zero findings, because that is not a state this loop reaches. Read the stopping rule before starting such a loop.
---

# Reviewing incus-dev: a round, and the loop of rounds

Thirty-six rounds have run on this repository, most of them under a standing
instruction to keep going until no findings remained. They never ran out.
[Know when to stop](#know-when-to-stop) is why, and is the part to read first
if that is the instruction you are working under.

What the rounds showed:

- **Coverage says nothing.** Every round ran at 99% line coverage. Every round
  found real defects, including data loss and an instance that could not be
  deleted.
- **The lens decides what you find.** Repeating a lens finds little. A lens
  nobody has used finds several, immediately.
- **The last round's fixes are the most likely place for the next defect.**
- **The count of findings never fell. The severity did.** Read
  [Know when to stop](#know-when-to-stop) before starting a round whose purpose
  is to reach zero, because that is not a state this loop reaches.

## What the loop carries between rounds

One round is the unit; the loop is what makes them add up. Four things have to
survive from one round to the next, and none of them are in the code:

- **Which lenses have been used, and what each found.** Kept in
  [references/lenses.md](.claude/skills/review-round/references/lenses.md).
  Without it the loop repeats a lens and mistakes a quiet round for progress.
- **The severity of what each round found.** The count does not fall; the
  severity does, and that is the only thing that says whether to continue.
- **What was decided not to fix, and why.** Two things here are open by
  decision rather than oversight; both are named at the end of the catalogue.
  A round that "finds" them has found nothing.
- **What the round cost.** A full round is two review agents and a six-minute
  integration run. That is worth paying while findings reach users, and is not
  once they are about comment wording.

## Pick a lens, or sweep them all

Reviewing "the code" again produces less each time. Reviewing it *as* something
produces findings on the first pass.

Every lens run here is catalogued, with the question to put to an agent and
what it actually found:
[references/lenses.md](.claude/skills/review-round/references/lenses.md).
Pick from it rather than inventing one. Every lens in it has now been run at
least once, so each row's Re-run column says what it is worth today; the four
things left open by decision are named there too, and a new lens will otherwise
"find" them every time.

**Mutation testing is the highest-yield lens.** Copy the repo to `/tmp`, apply
20-40 targeted mutations one at a time, run the suite, and record which
survive. Never mutate the working tree. A survivor is proof of an unasserted
behaviour. **Check the mutation landed** -- a substitution that misses, or
lands in another function with the same line, reports itself as survived.

### Sweeping several lenses at once

One agent per lens, run together, is worth it when the question is "where does
this repository stand" rather than "what did the last round break". It costs
what its lenses cost and returns one report per lens, so:

- Give every agent the same standing constraints (below) and one **Ask** from
  the catalogue. Do not merge two lenses into one agent; the brief is what
  makes a lens a lens.
- **At most one agent may touch Incus.** The daemon is shared, and two agents
  creating instances at once produce failures that belong to neither. Name
  which one it is in its brief; tell the rest to use the fake.
- Expect duplicates. Two lenses looking at the same code report the same
  defect in different words -- that is a signal it is real, not noise to
  suppress.
- Consolidate before acting: merge duplicates, drop what is already open by
  decision, then rank by severity and reproduce each one yourself. A sweep
  produces more findings than a round can absorb, and taking them in report
  order means fixing comment wording before data loss.

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

## Know when to stop

"Until there are no findings" is not a condition this loop reaches, and it is
worth saying why before spending another round on it.

**The thing being reviewed grows faster than the review retires it.** Over
thirty-six rounds:

```
              implementation    tests     docs
before             3,121        3,795    5,321
after              9,020       21,157   10,874
```

Every finding is answered with a test and a paragraph, and both are reviewable.
The findings of the last five rounds were almost entirely about tests and
comments written in the rounds before them. Fixing at this level adds surface
at the same rate it removes defects.

**The count is the wrong measure. Severity is the signal.**

| Rounds | What the findings were |
| --- | --- |
| early | secrets corrupted, volumes orphaned, Ctrl-C dead, data lost between two idevs |
| middle | wrong beliefs about Incus, encoded identically in the fake and its tests |
| late | a dead `_ = err`, a comment that misstates, a threshold that measures itself |

Nothing in the late rounds is reachable by a user. Counting findings hides
that completely.

So stop on a severity bar, not on zero:

- **Stop** when a full round produces nothing a user could hit — no data loss,
  no wrong answer, no command that fails or hangs, no message that misleads.
- **Keep going** while a finding names something a user runs into, however
  small the diff that fixes it.
- **Say which it was.** "No findings" is never demonstrable; "this round found
  nothing a user could reach, and here are the lenses used" is.

If the loop must continue past that bar, narrow it to verification against the
real thing (below) and drop the lenses that read the repository's own prose.

## Check against the real thing, not against your own model

The one mechanism that kept working. Three times in this history a defect was
invisible to every unit test and caught only by asking the real system:

- `internal/incus/contract` run against the daemon, where the fake agreed with
  a belief that was wrong — including a write that silently lost another run's
  record, and an `Instance` that returned no ETag at all
- real `ansible-playbook`, which showed that the shape chosen to stop templating
  a secret re-read its type instead: `"0123456"` arrived as `42798`
- the built binary under real signals, where a fix that passed its own test did
  nothing at all

A fake is written from the same belief as the code it stands in for, so a
review that only runs unit tests re-reads that belief back. Prefer, in order:
the real daemon, the real external tool, the real binary, then the fake.
