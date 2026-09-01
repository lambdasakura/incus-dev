# incus-devkit skill

An Agent Skill for driving `idev` from an AI coding agent.

*[日本語版 / Japanese](../incus-devkit-ja/README.md)*

## Installing

Copy it somewhere the agent reads.

```bash
# for every project of this user
cp -r skills/incus-devkit ~/.claude/skills/

# for one project only
cp -r skills/incus-devkit /path/to/project/.claude/skills/
```

## Layout

```text
incus-devkit/
├── SKILL.md                      # principles, commands, workflows
├── references/
│   ├── dev-yml.md                # every dev.yml field
│   └── troubleshooting.md        # look up an error message
└── templates/
    └── dev.yml                   # an annotated starting point
```

The content is based on `docs/` in this repository. When the CLI's behaviour
changes, update this alongside the manual.
