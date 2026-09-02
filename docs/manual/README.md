# idev manual

How to use `idev`, a CLI that builds and manages per-project development
environments with Incus.

*[日本語版 / Japanese](ja/README.md)*

## What the tool does

Put a single `.incus-dev/dev.yml` in your repository, and anyone who clones it
reproduces the same development environment with two commands.

```bash
git clone <repository>
cd <repository>

idev up
idev shell
```

- Your source is not copied. The host's working tree is mounted into the
  container, so your IDE, Git and AI coding tools stay on the host while
  builds, tests and runs happen inside the container
- **The project decides** what goes into the container. idev carries no Ansible
  roles and no language-runtime installers

## Contents

| # | Document | Contents |
| --- | --- | --- |
| 1 | [01-installation.md](01-installation.md) | Requirements, installation, smoke test |
| 2 | [02-getting-started.md](02-getting-started.md) | Building your first project (tutorial) |
| 3 | [03-commands.md](03-commands.md) | Command reference |
| 4 | [04-dev-yml.md](04-dev-yml.md) | `dev.yml` reference |
| 5 | [05-provisioning.md](05-provisioning.md) | Writing the provisioning steps |
| 6 | [06-recipes.md](06-recipes.md) | Recipes by use case |
| 7 | [07-ai-agents.md](07-ai-agents.md) | Driving idev from AI coding tools |

When something does not work, see [troubleshooting](../troubleshooting.md).

The intent behind the design — the "why is it like this" — is recorded in the
[design specification](../spec/README.md), which is written in Japanese.

## Terminology

| Term | Meaning |
| --- | --- |
| `idev` | The command |
| `.incus-dev/` | The project's configuration directory |
| `dev.yml` | The environment definition file (`.incus-dev/dev.yml`) |
| workspace | The project working tree, mounted into the container |
| instance | The Incus container idev creates. Named `dev-<project name>-<hash of the checkout>` by default (`project.scope`) |
| provision | The steps that configure the inside of the container, declared in `dev.yml` |
| bootstrap | The minimum preparation needed before provisioning can run |
