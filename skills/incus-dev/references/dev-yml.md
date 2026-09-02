# `.incus-dev/dev.yml` reference

The project root — the directory holding `.incus-dev/` — is what every relative
path is resolved from. `idev` walks up the directory tree looking for
`.incus-dev/dev.yml`, so you can run it from anywhere in the repository.

## The whole picture

```yaml
schema: 1                      # required. currently 1 is the only value

runtime:
  version: "1.0"               # optional. the devkit version this project expects

project:
  name: my-project             # required. the instance is named dev-<name>
  scope: name                  # name (default) | path | branch

instance:
  image: images:ubuntu/24.04   # required
  profiles: [default]          # default [default]. [] means no profile at all
  config:                      # passed straight to the Incus instance config
    limits.cpu: "8"
    limits.memory: 16GiB
  devices:                     # passed straight to Incus devices
    data:
      type: disk
      source: ./assets         # from the project root
      path: /data

workspace:
  source: .                    # defaults to the project root
  target: /workspace           # default /workspace
  idmap: auto                  # auto (default) | raw | shift | none

shell:                         # defaults for idev shell / idev exec
  user: developer
  command: /bin/bash           # default /bin/sh
  cwd: /workspace/src          # defaults to workspace.target

incus:
  project: development         # Incus project. --incus-project on the CLI wins

volumes:                       # data that survives a rebuild
  cache:
    path: /home/dev/.cache     # required
    size: 10GiB
    pool: default

secrets:                       # injected from the host. never write the value
  API_TOKEN:
    env: HOST_TOKEN
  DEPLOY_KEY:
    file: ~/.config/key
    optional: true

bootstrap:                     # runs before provision. a default runs if omitted
  - run: command -v python3 >/dev/null 2>&1 || dnf install -y python3

provision:                     # the main body
  - name: base
    run: |
      apt-get update
      apt-get install -y --no-install-recommends git make
```

## Steps

Elements of `bootstrap` and `provision` have the same shape. Each holds
**exactly one** of `run`, `ansible` or `galaxy`.

```yaml
- name: setup            # optional. appears in logs and errors; worth setting
  run: |                 # the script to run inside the container
    make deps
  shell: /bin/bash       # default /bin/sh
  cwd: /workspace        # working directory
  user: developer        # a numeric uid goes to Incus; a name is switched to with su
  env:                   # extra environment variables. values are masked when displayed
    CGO_ENABLED: "0"
```

```yaml
- name: playbook
  ansible:
    playbook: .incus-dev/ansible/site.yml
    vars: .incus-dev/ansible/vars.yml
    inventory: .incus-dev/ansible/inventory.ini   # generated automatically if omitted
    tags: [setup]
    skip_tags: [slow]
    extra_args: ["--diff"]
```

```yaml
- name: collections
  galaxy:
    requirements: .incus-dev/ansible/requirements.yml
    extra_args: ["--force"]
```

An `ansible` step uses the host's `ansible-playbook` and enters the container
through the `community.general.incus` connection plugin — SSH is not involved.
The host is named `dev` in the inventory, so write `hosts: dev` in the
playbook.

## Environment variables passed to `run` steps

```text
DEVKIT_PROJECT_NAME       project name
DEVKIT_INSTANCE           instance name
DEVKIT_WORKSPACE          workspace path inside the container
DEVKIT_WORKSPACE_SOURCE   project root on the host
DEVKIT_INCUS_PROJECT      Incus project
```

Setting the same name in `env:` overrides it. Values from `secrets:` arrive as
environment variables too.

## What the default bootstrap does

Only when `bootstrap` is omitted *and* `provision` contains an `ansible` step
does the default bootstrap run, installing python3 on the assumption of a
Debian-family image. On anything else it fails, so declare `bootstrap`
explicitly in that case.

Writing `bootstrap: []` runs nothing.

## `workspace.idmap`

How uids and gids are mapped when sharing a host directory into the container.

| Value | Extra host setup | Host-side owner of container-created files |
| --- | --- | --- |
| `auto` (default) | none | the invoking user if `raw` works, otherwise root |
| `raw` | `root:<uid>:1` in `/etc/subuid` and `/etc/subgid` | the invoking user |
| `shift` | none | root |
| `none` | none | (not writable) |

The same mapping is applied to disks added through `instance.devices`.

## `project.scope`

Change this only when you clone the same repository into several places, or
want a separate environment per branch. Changing the default turns existing
environments into different ones.

| Value | Instance name |
| --- | --- |
| `name` (default) | `dev-my-project` |
| `path` | `dev-my-project-cb958c73` |
| `branch` | `dev-my-project-feature-x` |

## Things to watch when writing it

- Values in `instance.config` may be written as numbers or booleans; they are
  converted to strings
- `user.incus-devkit.*` is used by devkit for bookkeeping — do not write it
- Keys starting with `-`, and keys containing `=`, are not allowed
- With `profiles: []`, declare the root disk and the network yourself
- Relative paths (`workspace.source`, a device's `source`, playbooks, …) are
  all resolved from the project root
