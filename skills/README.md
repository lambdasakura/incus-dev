# Agent Skills

Agent Skills for driving `idev` from an AI coding agent.

| Skill | Language |
| --- | --- |
| [incus-dev](incus-dev/) | English |
| [incus-dev-ja](incus-dev-ja/) | 日本語 |

The two carry the same content. Install whichever language you want the agent
to work in — installing both is not useful, since they would compete for the
same task.

```bash
cp -r skills/incus-dev ~/.claude/skills/          # for every project
cp -r skills/incus-dev <project>/.claude/skills/  # for one project
```

The content is based on `docs/` in this repository. When the CLI's behaviour
changes, update both alongside the manual. `test/skills_test.go` checks that
the templates stay valid and that the commands the skills mention actually
exist.
