# The lenses, and what each one found

Nineteen lenses have been run on this repository, each of them at least once.
This is the record, and it is meant to be re-used: each entry carries the question to put to an agent, so a
lens can be re-run without being reinvented, and a sweep can be assembled by
picking rows.

Read `.claude/skills/review-round/SKILL.md` for how to brief an agent, how to
run several at once, and when to stop.

## How to read the table

- **Ask** is the seed of the brief. Drop it into the standing constraints in
  SKILL.md; do not send it alone.
- **Found** is what it actually produced here, which is the only honest
  estimate of what it might produce again.
- **Re-run** says whether it is worth paying for now. `after a change` means it
  is cheap and worth repeating whenever the area it names is touched.

## Spent, but worth repeating after a change

| Lens | Ask | Found | Re-run |
| --- | --- | --- | --- |
| Regression on the last round | Take the previous round's commits. For each, what did it change, what could that break, and does a test fail when the fix is reverted? | Something in thirty-two of thirty-four rounds -- the single highest-yield lens in this history | Every round, without exception |
| Mutation testing | Copy to /tmp. Apply 20-40 targeted mutations one at a time (invert a condition, drop a call, swap `&&`/`||`, move a boundary, delete a branch). Report every survivor. | 24 behaviours executed by the tests and asserted by nothing, at 99% coverage | After any change to the tests |
| Spec conformance | Read `docs/spec/`. For every promise it makes, find the code that keeps it, or report that nothing does. | Six rounds of drift, and a spec sentence describing behaviour the code never had | After any change to `docs/spec/` |
| The test machinery, not the code | Review the tests and helpers as the subject. What can each assertion fail on? Which pass for the wrong reason? | Four rounds of defects in the guards themselves, including checks that could not fail | After any change to the tests |
| API round-trip efficiency | Count the Incus calls each command makes. Which are redundant, and what does that cost on a slow daemon? | Eight redundancies, seven of them real: a write sent on every run with nothing to write, the same volume and the same instance asked about twice, one listing per profile, an image resolved twice per rebuild, and every write pre-reading the snapshots it never looks at | After any change to a command's call sequence |
| Cross-package API design | Read the boundaries between `internal/*` as an API. What leaks, what is named wrongly, what would be hard to change? | Eight, including a belief about Incus held in `internal/cli` where no contract could check it, and a panicking constructor holding the obvious name | After a boundary moves |

## Spent, and unlikely to pay again soon

| Lens | Ask | Found |
| --- | --- | --- |
| General code review | Read the implementation for defects, without a narrower brief. | Crashes, a secret reaching a log |
| Output text, read as a user | Read every message the CLI can print. Is it true, and does it say what to do next? | Warnings that stated the opposite of the truth; advice that would have destroyed the user's work |
| Executing the documentation | Run every command and example in `docs/` and `examples/` exactly as written. | A shipped example that failed on first run |
| Hostile input and interference | Feed idev what a careless or unlucky user would: absent files, wrong kinds, names that break what carries them, an instance changed by hand. | Data orphaned, an instance made undeletable, Ctrl-C dead |
| Idempotency and convergence | Run every command twice. Interrupt each between any two steps. Does running it again converge, or does the user need `incus` by hand? | A state that never converged, whose advice killed the user's work |
| Upgrade from an older idev | Construct an instance made by an earlier version and run every command against it. | An environment silently stranded |
| Concurrency and resource lifetime | Run with `-race`. Find every goroutine that can outlive its command, block forever, or write to a closed channel. | A data race; Ctrl-C hanging for two seconds |
| Interruption, and what it is left behind | For each multi-step operation, what is on disk and in Incus if the process dies between any two steps? | An idev that could not be interrupted at all after the first Ctrl-C |
| Secret handling, from the inside | Trace every path a resolved secret takes. Can it reach a log, an error, an argv, a file that outlives the run? | Nothing -- a clean result, twice |
| Build, dependency and release | Build the release artefacts. Does the archive hold what the documentation tells the user to run? | A release archive missing files its own docs use |
| Error and diagnostic quality | For every failure a user can hit: does it say what went wrong, what to do next, and is it true? | A preflight that passed exactly what the next command refused |
| The configuration surface as a contract | Compare `docs/spec/03-configuration.md`, `schemas/`, and what the code does, field by field. What is accepted and then ignored, or accepted and then refused by Incus? | A second YAML document dropped in silence; four shapes Incus refuses |
| Provisioning and secrets, end to end | Follow a step from `dev.yml` to the container. Check the prerequisite checks can fail, and that a secret arrives byte for byte. | A prerequisite check that could never fail; secrets silently templated |
| Two idevs at once | Two terminals, one project. Trace every read-modify-write on the instance's own records. | A lost update that orphaned volumes, eight runs in ten |
| Driven by a program, not a person | Read the CLI as a caller: exit codes, which stream carries what, whether listings parse. | Zero rows printed as a sentence on stdout |

## Open by decision, not by oversight

A lens that reports these has found nothing new. Each was measured, and each
was left alone for a stated reason.

- **Seven `Client` methods bind a context and discard it.** The Incus client
  library offers no per-call context -- its `WithContext` mutates the shared
  client rather than returning a copy -- and returning early would widen the
  window in which a write is reported as failed after the daemon applied it.
  The second-signal guard in `cmd/idev/main.go` is the escape.
- **Nothing serialises two `idev provision` runs.** The collision fails loudly
  inside a step rather than silently, which is the failure mode to prefer.
- **`ensureRunning` waits even for an instance already read as running.** The
  wait is one exec probe: "Running" is the daemon's word about the container,
  not about whether a command can run in it yet, and the probe is what makes
  `idev shell` fail with a reason instead of an obscure error.
- **Errors are worded where they are raised, below `internal/cli`.** The
  layering rule is that the lower packages do not know the CLI's output
  *format*; an error necessarily carries wording, and the package that raises
  it is the one that knows what happened. What was missing was a check, and
  `TestArchitecturalConstraintsHold` now reads every package's literals.
