# 7. Driving idev from AI coding tools

This tool is designed to be driven by AI agents such as Codex and Claude Code.

*[日本語版 / Japanese](ja/07-ai-agents.md)*

## 7.1 The basics

An agent only needs these.

```bash
idev up
idev provision
idev exec -- <command>
idev status --json
```

`idev exec` allocates no pseudo-terminal. Its behaviour does not change with
the presence of a terminal, which is what you want from a script or an agent
(use `idev shell` only when you need interaction).

None of this requires knowing anything about Incus internals — instance names,
devices, profiles.

To change the environment, the only files an agent should edit are the ones
**under `.incus-dev/`**. That consistency is what makes "where do I fix this?"
an easy question.

## 7.2 It runs non-interactively

These never ask for confirmation.

```bash
idev up
idev provision
idev status
idev validate
```

Only the destructive operations ask, and a flag skips that.

```bash
idev destroy --force
idev rebuild --force
```

## 7.3 Exit codes mean something

```bash
idev up || exit 1
```

`idev exec` and `idev shell` pass the container command's exit code straight
through, so a test result is usable as it is.

```bash
idev exec -- make test        # non-zero if the tests fail
```

Output survives being sent somewhere other than a terminal, so it is safe to
pipe.

```bash
idev exec -- go test ./... 2>&1 | tail -20
```

## 7.4 State is machine-readable

```bash
idev status --json
```

```json
{
  "project": "my-project",
  "instance": "dev-my-project",
  "status": "Running",
  "exists": true,
  "managed": true,
  "workspace": "/workspace",
  "provision_steps": 2
}
```

```bash
# build it if it is not built yet
[ "$(idev status --json | jq -r .exists)" = "true" ] || idev up
```

Standard output carries the result and nothing else, so it is safe to capture:
`status --json`, `validate`, `provision --list`, `snapshot list`, `up
--dry-run`, and the output the command relays for `idev exec` and `idev shell`. Everything else --
logs, warnings, errors, the confirmation prompts, and the output of
provisioning steps during `up`, `provision` and `rebuild` -- goes to standard
error.

The two listings print one row per thing, tab-separated, and nothing at all
when there is none:

```bash
idev provision --list | wc -l     # 0 when there are no steps
idev snapshot list | cut -f1      # names, one per line
```

## 7.5 What to put in the project's instruction file

Something like this in `CLAUDE.md` or `AGENTS.md` stops an agent from trying to
build on the host.

```markdown
## Development environment

Build, test and run inside the Incus container. Never directly on the host.

    idev up                    # build the environment (first time, and after config changes)
    idev exec -- make test     # test
    idev exec -- make build    # build
    idev shell                 # interactive shell

To add something the environment needs, edit `provision` in
`.incus-dev/dev.yml` and apply it with `idev provision`. Do not install tools
on the host.

Provisioning steps are re-run, so write them to be idempotent.
```

## 7.6 Agent Skill

In tools that load Agent Skills, such as Claude Code, this repository's
[`skills/incus-dev/`](../../skills/incus-dev/) works as it is.

```bash
cp -r skills/incus-dev ~/.claude/skills/          # for every project
cp -r skills/incus-dev <project>/.claude/skills/  # for one project
```

It covers the principles, a task-to-command table, how to write `dev.yml`, and
how to work back from an error.

A Japanese version is available as
[`skills/incus-dev-ja/`](../../skills/incus-dev-ja/).

---

## 7.7 Things to keep in mind

- `idev up` never destroys an existing instance. Use `idev rebuild --force`
  only when you actually want to start over
- `idev destroy` deletes the container alone, and does not touch the source
  tree on the host
- An agent has no reason to reach for the `incus` command. Needing it is a sign
  that something is not expressible in `dev.yml` yet — so look at expressing it
  there first
