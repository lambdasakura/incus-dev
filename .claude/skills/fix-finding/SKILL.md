---
name: fix-finding
description: Use when acting on a review finding, a bug report, or a failing behaviour in the incus-dev repository - anything of the form "X is wrong, fix it". Covers reproducing before fixing, where the regression test has to go, checking every caller of a shared helper, verifying by exit code, and what belongs in one commit. Reach for it whenever a fix is about to be written.
---

# Fixing a finding in incus-dev

Two dozen rounds of review of this repository have produced upwards of thirty
fixes. In almost every round, the previous round's fix introduced a new defect. This is what those
failures had in common, and what stops them.

## 1. Reproduce it first, in this repository

Do not fix from a description. A report can be wrong about the cause, right
about the symptom, or describe something that no longer happens.

Write the failing test, run it, and read the failure. If it passes, the finding
is not real as stated — say so and stop.

## 2. Put the regression test where the mistake lives

This is the rule that matters most, and the one that was missed for fifteen
rounds.

| The finding is about | Test goes in | Why |
| --- | --- | --- |
| logic idev owns | the package's `_test.go` | a fake is enough |
| **what Incus does** | **`test/integration/` as well** | a fake cannot catch a wrong belief |
| what a command prints | `internal/cli` and, if the wording differs per command, `test/integration/` | wording drifted across three rounds |
| a shipped file (example, doc, skill) | `test/` | nothing else reads them |

**Why the integration test is not optional.** `internal/incus/incustest` is a
fake, and idev wrote it. A wrong belief about Incus produces a wrong fake and a
wrong test that agree with each other — the mistake written twice. Every
regression in those fifteen rounds was of that shape:

- a 404 means "not found" (it also means the *project* or *pool* is not found)
- a snapshot may be named anything (`.` wedges the instance on btrfs)
- `both 1000 0` differs from separate uid/gid lines (same mapping)
- receive-then-close is idempotent (two goroutines both take the default)

None of these could fail a unit test. All of them fail an integration test.

The integration test also runs the real CLI, so it catches what a unit test
that calls a method directly cannot — `idev snapshot create -wip` is refused by
the flag parser regardless of what the name rule allows.

## 3. Before changing a shared helper, list its callers

Three separate defects came from changing something with more than one caller
and thinking about one of them:

- `VolumeExists` was given a new error, which reached `destroy --volumes` and
  made it fail after the instance was already gone.
- `remainingVolumes` was written for a caller that knows the instance is gone,
  then used by one that does not, so the message hedged in one place and
  asserted too much in the other.
- `finish()` was called from two places and closed a channel that must be
  closed once.

Run `grep -rn '<name>(' --include='*.go'` and write down, per caller, what it
needs. If two callers need different things, that is two functions, or one
parameter that says which.

## 4. State what the change could break, then test that

For anything that is not purely additive, name the paths that behave
differently now — cleanup, cancellation, an error path, a second run, an
existing instance, an instance made by an older version. One fix in this
history refused Incus mutations after cancellation, which read as safety and
in fact stranded volumes permanently; it was reverted a round later.

## 5. If the check reads a file another tool owns, ask the tool

Four rounds running, the defect was in the test machinery rather than in the
code it guards: a hand-written parser for the Makefile and for the workflows,
wrong in both directions each time it was patched.

| Question | Do not | Do |
| --- | --- | --- |
| what `check` depends on | read `Makefile` | `make -np`, sliced at its `# Files` banner |
| which steps CI runs | match `run:` lines | unmarshal with `sigs.k8s.io/yaml` |
| whether a step runs `make vuln` | parse the shell | require a line of `make` plus words that are all real targets |

The last row is the general move. A check that has to tell a command from a
sentence about a command cannot win on syntax — quoting defeats it, and each
patch reopens the other direction. Take the **vocabulary** from the tool
instead: `make vuln runs nightly` offers `runs`, which make does not know, so
it is prose, and prose cannot pass however it is quoted.

Two things follow. Prefer the strict, loud reading: `make vuln || true` failing
this test is correct, because swallowing the failure is how a gate gets
neutered without being removed, and a false alarm is cheaper here than a silent
pass. And let the helper take its directory as a parameter, so it can be
pointed at a fixture — `-n` echoes the default goal's recipe *before* `-p`
prints the database, and that was undetectable while the helper could only read
this repository.

## 6. Check the test can fail

A test that passes before the fix asserts nothing. Revert the fix, watch the
test fail, restore it:

```bash
cp internal/cli/app.go /tmp/app.bak
# undo the fix by hand
go test ./internal/cli/ -run TestTheThing   # must FAIL
cp /tmp/app.bak internal/cli/app.go
```

Do this for integration tests too. It is the single most effective check in
this repository's history: a sweep of it across the suite found 24 behaviours
that were executed but never asserted, at 99% line coverage.

## 7. Verify by exit code, never by reading output

```bash
make check   >/dev/null 2>&1; echo "check exit=$?"      # 0 or it failed
make cover   >/dev/null 2>&1; echo "cover exit=$?"
go test -race -count=1 ./... >/dev/null 2>&1; echo "race exit=$?"
```

`make check | grep -c FAIL` was used once. golangci-lint does not print
"FAIL", so a gofmt violation reached two commits and CI found it. Any check
whose result is decided by grepping its output is not a check.

Run `make test-integration` when the change touches Incus behaviour, and read
its exit code the same way. Save the full log — a failure two hundred lines up
is gone if the command ended in `| tail -3`.

## 8. One topic per commit, staged explicitly

`git add -A` swept unrelated work into one commit three times in this history,
each needing a `reset --soft` to split. Stage the files for the topic:

```bash
git add internal/cli/app.go internal/cli/app_test.go && git commit
```

The message says **why**, not what. Reference the spec as `spec 04-cli.md 4.7`.
See `## コミット` in CLAUDE.md.

## 9. Say what you did not fix

If part of a finding is genuinely not fixable, say what information is missing
and where it would have to come from. Three things were called unknowable in
this history and two turned out to be recoverable on a second look — a volume
was still attached as a disk device, and an image the instance had no record of
could be reported as unrecorded instead of guessed.
