---
name: incus-dev
description: Use when working with a per-project Incus development environment through the idev command and .incus-dev/dev.yml. Covers creating an environment, running builds and tests inside the container, adding tools and dependencies, diagnosing idev failures, and adopting it in an existing project. Reach for it whenever the work touches "idev", ".incus-dev", "dev.yml", or an Incus-based development environment.
---

# Using incus-dev (idev)

`idev` is a CLI that builds and manages a per-project development environment
as an Incus container. Use it to get a reproducible environment per repository
without installing anything on the host.

## Three principles

**1. Build, test and run inside the container.**
Do not install tools on the host. No `apt-get`, no `npm i -g` on the host.

```bash
idev exec -- make test
```

**2. Express every environment change inside `.incus-dev/`.**
Do not change settings by calling `incus` directly. A setting changed by hand
is either reverted by the next `idev up` (for the keys idev manages) or never
recorded at all. Wanting to reach for `incus` is a sign that something is not
expressible in `dev.yml` yet.

**3. Write provisioning steps to be idempotent.**
`idev provision` is meant to be re-run any number of times. Not `git clone` but
`test -d x || git clone`; not `useradd` but
`id -u dev >/dev/null 2>&1 || useradd`.

## Task to command

| Task | Command |
| --- | --- |
| Prepare the environment (first time, after a `dev.yml` change) | `idev up` |
| Run a command in the container | `idev exec -- make test` |
| Get an interactive shell | `idev shell` |
| Run something as another user | `idev exec --user root -- apt-get ...` |
| Re-run provisioning only | `idev provision` |
| See what would happen first | `idev up --dry-run` |
| Read the state machine-readably | `idev status --json` |
| Reach it over the network | `ssh user@$(idev ip)` |
| Check `dev.yml` alone (touches no Incus) | `idev validate` |
| Start over | `idev rebuild --force` |
| Remove it (host sources stay) | `idev destroy --force` |

`--user` overrides `shell.user` for one run, on both `shell` and `exec`. It
takes a name or a uid. Editing `shell.user` in `dev.yml` instead changes it for
everyone working on the project.

`idev ip` prints one address and nothing else, so it can be substituted.
IPv4 before IPv6, the same one every run, and nothing at all on stdout when
there is no address -- an empty substitution would leave `ssh` connecting to
the local user. Every address, with its interface, is in `idev status --json`.

The only difference between `idev exec` and `idev shell -- <cmd>` is the
pseudo-terminal. **From scripts and CI, use `idev exec`** — its behaviour does
not change with the presence of a terminal. Both pass the container command's
exit code straight through, so `idev exec -- make test || exit 1` works as it
is.

Only the destructive operations ask for confirmation (`destroy`, `rebuild`,
`snapshot restore|delete`), and `--force` skips it in every case. Everything
else runs non-interactively.

## Adopting it in a new project

1. **Read the existing setup instructions.** Pick up "on which OS", "with what
   installed" and "how it is started" from the README, the Dockerfile, the CI
   configuration and the Makefile.
2. **Write `.incus-dev/dev.yml`**, starting from `templates/dev.yml`.
   - Match the image to the Dockerfile or CI (`images:ubuntu/24.04`,
     `images:debian/12`, `images:alpine/3.21`, …)
   - **Write provisioning in shell.** Reach for Ansible only when the project
     already uses it, or when the steps are complex enough that keeping them
     idempotent by hand is painful
3. Check with `idev validate`, then `idev up`.
4. **Add a note to the project's `CLAUDE.md` / `AGENTS.md`** for the agents
   that come after you.

```markdown
## Development environment

Build, test and run inside the Incus container, never on the host.

    idev up                    # build the environment (first time, after dev.yml changes)
    idev exec -- make test     # test
    idev shell                 # interactive shell

To add something to the environment, edit `provision` in `.incus-dev/dev.yml`
and run `idev provision`. Do not install tools on the host.
```

## Adding something to the environment

Add a step to `provision` in `dev.yml` and run `idev provision`. The instance
is not recreated, so work in progress inside it survives.

```yaml
provision:
  - name: go toolchain
    run: |
      test -x /usr/local/go/bin/go && exit 0
      curl -fsSL https://go.dev/dl/go1.25.0.linux-amd64.tar.gz | tar -C /usr/local -xz
```

- After changing `instance.config` or `instance.devices` (CPU, memory, extra
  mounts), run **`idev up`**, not `idev provision`
- Settings that need a restart to take effect (`raw.idmap`,
  `security.nesting`, `security.privileged`) produce a warning. If restarting
  is fine, use `idev up --restart`
- To try part of a long provisioning run, get the numbers with
  `idev provision --list`, then `idev provision --step 3` or `--from 3`

## When it does not work

**Work from the lightest option up. Do not jump to `rebuild`** — it throws away
everything done inside the container.

```bash
idev validate          # 1. dev.yml syntax and referenced paths
idev status            # 2. instance state; is it managed by idev?
idev up --dry-run      # 3. what would be applied
idev provision         # 4. re-run provisioning only
idev up                # 5. re-run including re-applying the configuration
idev up --verbose      # 6. see the Incus operations and the commands
idev rebuild --force   # 7. last resort
```

Error-by-error guidance is in
[references/troubleshooting.md](references/troubleshooting.md).
**When package installation fails inside the container it is almost always a
host-side network problem** (a conflict with Docker), so check that before
editing `dev.yml`.

## What not to do

| Anti-pattern | Instead |
| --- | --- |
| `apt-get install` on the host, then work | Put it in `provision` in `dev.yml` |
| Call `incus exec` / `incus config set` directly | Write it in `dev.yml` and run `idev up` |
| Reach for `idev rebuild` on any failure | Work through the order above |
| `git clone` in provisioning (fails the second time) | `test -d dir \|\| git clone ...` |
| An API token written into `dev.yml` | Inject it from the host with `secrets:` |
| Put build output in `/root` or `/tmp` | Put it in `/workspace`, shared with the host |
| Use host paths inside the container | Write them relative to `/workspace` |

## Reference

| Document | Contents |
| --- | --- |
| [references/dev-yml.md](references/dev-yml.md) | Every `dev.yml` field, with defaults |
| [references/troubleshooting.md](references/troubleshooting.md) | Look up a cause and a fix from the error text |
| [templates/dev.yml](templates/dev.yml) | An annotated starting point |

`idev <command> --help` is always authoritative. When in doubt, read that.
