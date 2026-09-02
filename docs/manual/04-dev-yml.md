# 4. `dev.yml` reference

The file always lives at `.incus-dev/dev.yml`.

*[日本語版 / Japanese](ja/04-dev-yml.md)*

## 4.1 The whole picture

```yaml
schema: 1                          # required

runtime:
  version: "1.0"                   # optional. the runtime contract this needs

project:
  name: my-project                 # required. the instance name is derived from it
  scope: name                      # optional. path (default) | name | branch

instance:
  image: images:ubuntu/24.04       # required
  profiles:                        # optional (default: [default])
    - default
  config:                          # optional. passed through to the Incus instance config
    limits.cpu: "8"
    limits.memory: 16GiB
  devices:                         # optional. passed through to Incus devices
    gpu0:
      type: gpu

workspace:                         # optional
  source: .
  target: /workspace
  idmap: auto

volumes:                           # optional. data that survives a rebuild
  cache:
    path: /home/dev/.cache
    size: 10GiB

secrets:                           # optional. secrets injected from the host
  API_TOKEN:
    env: HOST_TOKEN

shell:                             # optional. defaults for idev shell / idev exec
  user: developer
  command: /bin/bash
  cwd: /workspace

incus:                             # optional
  project: development

bootstrap:                         # optional
  - run: command -v python3 || apk add python3

provision:                         # optional. run top to bottom
  - name: setup
    run: sh /workspace/.incus-dev/scripts/setup.sh
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
```

Only `schema`, `project.name` and `instance.image` are required.

---

## 4.2 `schema`

```yaml
schema: 1
```

The version of the configuration format. Currently `1` is the only value.

---

## 4.3 `runtime`

```yaml
runtime:
  version: "1.0"
```

The **runtime contract** this project needs, currently `1.0` — not an `idev`
release number. It goes up when the meaning of dev.yml changes in a way an
older idev could not honour, and an idev that does not satisfy the constraint
refuses to run.

`MAJOR`, `MAJOR.MINOR` and `MAJOR.MINOR.PATCH` are all accepted.

---

## 4.4 `project`

```yaml
project:
  name: my-project
```

The instance is named `dev-<name>`. The name must start with a letter or digit,
and may then contain letters, digits, dots, hyphens and underscores (up to 55
characters). It is normalised to letters, digits and hyphens for the instance
name.

That normalisation lower-cases the name and turns `.` and `_` into `-`, so
`My.Project`, `my_project` and `my-project` all ask for the same instance name.
Under the default `scope: path` that only collides when they sit in one
directory; under `scope: name` it always does, and then only the first to run
gets the instance while the others are refused, because two projects cannot
share one. Writing the name in the form it will take avoids the surprise.

If you work on several projects on one machine, keep the names distinct.

### `scope`

How instance names are distinguished, which decides when two directories share
an environment and when they get one each.

```yaml
project:
  name: my-project
  scope: name        # path (default) | name | branch
```

| Value | Instance name | Use |
| --- | --- | --- |
| `path` (default) | `dev-my-project-cb958c73` | one environment per checkout |
| `name` | `dev-my-project` | one environment for the project, wherever it is |
| `branch` | `dev-my-project-feature-x` | one per branch (requires Git) |

The default is `path` because cloning one repository into two places is
ordinary, and under `name` the two share a single instance: `/workspace` in
the container is whichever checkout ran `up` last, so `idev shell` from the
other one edits files that are not its own. idev warns when it notices, but
not sharing is better than being warned about sharing.

Pick `name` when one environment per project is what you want and you would
rather have the shorter instance name. Note that `path` follows the directory,
not the project: moving the checkout gives a different name, and the old
environment is left behind (`idev up` says so and names the `incus delete`).

---

## 4.5 `instance`

### `image`

An Incus image reference.

```yaml
image: images:ubuntu/24.04
image: images:alpine/3.21
image: images:debian/12
```

`incus image list images: <keyword>` shows what is available.

**Changing this on an existing instance does nothing.** An instance keeps the
image it was created from, so `idev up` warns and leaves it alone; use
`idev rebuild` to recreate it.

idev assumes no particular OS. Keeping the image and the provisioning steps
consistent is the project's responsibility.

### There is no `type`

An instance is always a container. Writing `type:` fails `idev validate` as an
unknown key.

A virtual machine cannot bind-mount the workspace, and `raw.idmap` and a disk's
`shift` are container-only mechanisms, so sharing the workspace would have to
be designed differently. That is not planned.

### `profiles`

A field that does nothing but **reference profiles that already exist on the
host, by name**.

Like `image`, these are set when the instance is created. Changing the list
later makes `idev up` warn rather than reassign them, because idev has no
record of which profiles it attached and which you did; `idev rebuild`
recreates the instance with the new list.

```yaml
profiles:
  - default
```

- idev neither ships nor creates profiles
- If a listed profile is missing, `idev up` fails explicitly
- Omitted, it defaults to `[default]`

You can pass an empty list to depend on no profile at all, but then you have to
declare the root disk and the network yourself.

```yaml
profiles: []
devices:
  root:
    type: disk
    pool: default
    path: /
  eth0:
    type: nic
    network: incusbr0
```

Storage pool and network names are host-specific, so referring to the `default`
profile is the more portable choice.

### `config`

Passed straight through to the Incus instance config. idev does not interpret
it.

```yaml
config:
  limits.cpu: "8"
  limits.memory: 16GiB
  security.nesting: "true"        # to run Docker and the like inside
  environment.TZ: Asia/Tokyo
```

Numbers and booleans are accepted and converted to strings, so `limits.cpu: 8`
is fine too.

**Quote anything YAML would read as something else.** It is the value YAML
parsed that gets converted, not what you typed: `0660` is octal and becomes
`"432"`, and `no` is a boolean and becomes `"false"`. Write `"0660"` and `"no"`
to pass them through as they are.

A whole number too large for 64 bits goes the same way: it is read as a
floating-point value, so `99999999999999999999999999999999999` reaches Incus
as `1e+35`. Quote it.

`limits.cpu` and `limits.memory` do take effect in containers, and are
reflected in what the container sees as its CPU count (`nproc`) and memory
(`/proc/meminfo`). That lets `make -j$(nproc)` and friends size themselves to
this environment's allocation rather than to the whole host.

Changes to `limits.*` apply to a running container as they are, so no restart is
needed.

`user.incus-dev.*` is reserved for idev's own bookkeeping and cannot be
used.

### `devices`

Passed straight through to Incus devices.

```yaml
devices:
  gpu0:
    type: gpu

  dataset:
    type: disk
    source: /srv/dataset          # absolute path on the host
    path: /data

  assets:
    type: disk
    source: ./assets              # relative paths are resolved from the project root
    path: /assets

  http:
    type: proxy
    listen: tcp:127.0.0.1:8080
    connect: tcp:127.0.0.1:8080
```

The name `workspace` is reserved (see 4.6).

A `disk` that mounts a host directory automatically gets the same uid/gid
mapping as the workspace (see `idmap` in 4.6). You do not need to write `shift`
yourself — and since the right value depends on the host, not writing it is the
more portable choice.

A `disk` with a `pool` is a different thing: there `source` names a storage
volume on that pool, not a path, and idev neither resolves nor checks it as
one. To have idev create and keep a volume for you, declare it under `volumes`
(4.10) rather than here.

Names and keys here are checked by `idev validate` before anything is applied:

| | Rule | Why |
| --- | --- | --- |
| Device name | must not start with `-` | incus reads it as one of its own flags |
| Device name | must not contain `,` | idev records which devices are its own as a comma-separated list |
| Key | must not start with `-`, contain `=` or `,` | the same two reasons |

The same rules apply to the keys of `instance.config`.

---

## 4.6 `workspace`

How host directories are mounted into the container. Omit it and the defaults
are used.

```yaml
workspace:
  source: .            # resolved from the project root. default "."
  target: /workspace   # path inside the container. default /workspace
  idmap: auto          # default auto
```

It is a bind mount, not a copy, so edits on the host are visible immediately.

### Mounting more than one directory

Write the mounts as a map instead. Each key becomes a device name.

```yaml
workspace:
  idmap: auto          # belongs to the instance, so it stays at this level
  main:
    source: .
    target: /workspace
  other-repo:
    source: ../other-repo
    target: /other-repo
  dataset:
    source: /srv/dataset
    target: /data
    readonly: true
```

`main` is the project's own tree: the shell's working directory, where
provisioning runs, and what `idev status` shows all mean that one. Omit it and
it is filled in with the same defaults as above, so adding a second directory
does not mean restating your own:

```yaml
workspace:
  other-repo:
    source: ../other-repo
    target: /other-repo
```

Notes:

- `target` has no default except on `main`. Two mounts sharing `/workspace`
  would fight over one directory
- `idmap` cannot be written inside a mount. Incus keeps one `raw.idmap` per
  instance, so it cannot differ per disk
- `main` is applied as the device named `workspace`, which is why `workspace`
  cannot be a mount name. Every other key is its own device name
- A mount name cannot collide with a key of `instance.devices` or `volumes`,
  start with `-`, or contain `,`
- A project using this form should set `runtime.version: "1.1"` (4.3). An
  older idev cannot read it, and the pin is what says so

### `idmap`

How uids and gids are mapped so an unprivileged container can share a host
directory.

| Value | Extra host setup | Host-side owner of container-created files |
| --- | --- | --- |
| `auto` (default) | none | you, if `raw` works; otherwise root |
| `raw` | required | you |
| `shift` | none | root |
| `none` | none | (not writable) |

`raw` requires `root:<uid>:1` in `/etc/subuid` and `/etc/subgid`
([01-installation.md](01-installation.md), 1.5).

`auto` uses `raw` when it can, falls back to `shift` when it cannot, and warns.
Either way the workspace is readable and writable; the only difference is who
owns what the container creates.

Whatever is chosen here is applied the same way to every mount above, and to
host directories mounted through `instance.devices`.

If your team cannot standardise host configuration, leaving it at `auto` is the
right call.

---

## 4.7 `bootstrap`

The minimum preparation needed before provisioning can run. Optional.

```yaml
bootstrap:
  - run: command -v python3 >/dev/null 2>&1 || dnf install -y python3
```

- Omitted, and with an `ansible` step in `provision`, the default bootstrap
  runs and installs python3, assuming a Debian-family image
- Written out, the default bootstrap does not run
- `bootstrap: []` disables it
- Only `run` is allowed here (not `ansible`, not `galaxy`)

See [05-provisioning.md](05-provisioning.md) for details.

---

## 4.8 `provision`

The steps that configure the inside of the container. An ordered list, run top
to bottom.

Each step has exactly one of `run`, `ansible` or `galaxy`.

```yaml
provision:
  - name: prepare
    run: apt-get update

  - name: main
    ansible:
      playbook: .incus-dev/ansible/site.yml
```

See [05-provisioning.md](05-provisioning.md) for how to write them.

A step `name` must not contain a control character -- a tab or a newline in it
would split or shift the rows of `idev provision --list`, which is one line per
step.

---

## 4.9 How paths are resolved

| What | Resolved from |
| --- | --- |
| `workspace.source` | the project root |
| `devices.*.source` (when relative) | the project root |
| `ansible.playbook` / `vars` / `inventory` | the project root |
| Paths written inside `run` | absolute paths inside the container |

To reach a project file from a `run` step, go through the workspace.

```yaml
provision:
  - run: sh /workspace/.incus-dev/scripts/setup.sh
```

---

## 4.10 `volumes`

Data you want to keep across a recreated instance.

```yaml
volumes:
  cache:
    path: /home/dev/.cache   # mount point inside the container
    size: 10GiB              # optional
    pool: default            # optional
```

The contents survive `idev rebuild`. They also survive `idev destroy`; use
`idev destroy --volumes` when you do want them gone.

Good for build caches, database files — anything you want to keep while the
environment around it is thrown away.

`size` is read when the volume is created and never again: changing it later
has no effect, and idev does not resize. Remove the volume with `incus storage
volume delete` and let the next `idev up` create it anew.

`pool` is different: it is read on every run. Changing it does not move
anything — the next `idev up` creates a new, empty volume on the new pool and
mounts that at the same container path, and the volume holding your data stays
on the old pool under its old name. If you meant to move the data, copy it with
`incus storage volume copy` before you edit `pool`.

`idev destroy --volumes` deletes the data along with the instance, and asks
about both before it does.

**Removing or renaming an entry does not delete the data.** idev remembers the
volumes it created, so `idev up` says which ones have left the declaration and
`idev destroy --volumes` still removes them.

---

## 4.11 `secrets`

`dev.yml` is meant to be committed, so **never write the value itself**. Inject
it from the host.

```yaml
secrets:
  API_TOKEN:
    env: HOST_TOKEN          # from a host environment variable
  DEPLOY_KEY:
    file: ~/.config/key      # from a host file; ~ is expanded here
  OPTIONAL_ONE:
    env: MAYBE
    optional: true           # may be absent
```

- `run` steps receive them as environment variables
- `ansible` steps receive them as `--extra-vars` (through a mode 0600 temporary
  file), marked so Ansible neither evaluates a value containing `{{ ... }}` as
  a template nor re-reads its type: `0123456` stays that string rather than
  becoming a number
- If any of them cannot be resolved, idev stops **before touching the
  instance** and tells you which are missing
- Values are masked in logs and error messages

```console
$ idev up
[idev] error: cannot resolve secret(s):
  API_TOKEN (environment variable HOST_TOKEN): not set
```

What a step prints itself cannot be masked, though. Avoid writing things like
`echo $API_TOKEN`.

---

## 4.12 `shell`

Defaults for `idev shell` and `idev exec`.

```yaml
shell:
  user: developer      # the user to run as. defaults to the instance default (root)
  command: /bin/bash   # the shell to start. default /bin/sh
  cwd: /workspace/src  # working directory. defaults to workspace.target
```

A numeric uid in `user` is passed to Incus as it is. A user *name* is switched
to with `su` inside the container, because the Incus exec API only accepts
uids. The user has to exist in the container, so create it during provisioning.

---

## 4.13 `incus`

Selects the Incus project to operate in.

```yaml
incus:
  project: development
```

The `--incus-project` flag wins if given. With neither, `default` is used.

The project has to exist in Incus already — `idev` does not create it.
