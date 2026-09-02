# 3. Command reference

*[日本語版 / Japanese](ja/03-commands.md)*

## 3.1 Common behaviour

### Finding the project

`idev` looks for `.incus-dev/dev.yml` in the current directory and then
upwards, so you can run it from a subdirectory.

```bash
cd src/foo/bar
idev status        # finds the project root
```

It does not have to be a Git repository.

### Global flags

| Flag | Default | Description |
| --- | --- | --- |
| `-v`, `--verbose` | | print detail, including the external commands that were run |
| `-C`, `--directory <dir>` | current directory | directory to start looking from |
| `--incus-project <name>` | `incus.project` in `dev.yml`, else `default` | Incus project |

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success |
| non-zero | Failure |

`idev shell -- <command>` and `idev exec -- <command>` are the exception: they
**pass the container command's exit code straight through**.

```bash
idev shell -- sh -c 'exit 42'; echo $?   # 42
```

### Instance names

The instance is named `dev-<project name>`. Anything but letters, digits and
hyphens is normalised away.

If you clone the same project into several places, tell them apart with
`project.scope` in `dev.yml` (`path` or `branch`).

```text
project.name: my.project_1   →   instance: dev-my-project-1
```

- Names longer than 63 characters are truncated
- A name that normalises to nothing (all punctuation, say) is distinguished by
  a hash of the original

---

## 3.2 `idev validate`

Validates `dev.yml`. It **makes no change to Incus at all**, so it runs in CI
and on machines without Incus.

```bash
idev validate
```

What it checks:

- YAML syntax, schema, required fields, unknown fields
- `runtime.version` compatibility
- The structure of the provisioning steps (`run` and `ansible` are mutually
  exclusive, and so on)
- That referenced files exist (playbook, vars, inventory, workspace source)
- That a root disk device is declared when `profiles: []`
- That no reserved key (`user.incus-devkit.*`) is used

It does not check whether the profiles exist on the host, because it does not
talk to Incus.

---

## 3.3 `idev up`

Prepares the instance, then runs bootstrap and provisioning.

```bash
idev up
```

What happens:

1. `dev.yml` is loaded and validated
2. The workspace idmap strategy is chosen (by inspecting the host)
3. The referenced profiles are checked for existence (failing if any is
   missing)
4. The instance is created if absent, or **has its configuration re-applied
   without being recreated** if present
5. The workspace and devices are applied
6. It is started, and waited on until it can run commands
7. bootstrap, then provisioning, are run

**An existing instance is never destroyed.** Edit `dev.yml`, run again, and the
resource and device changes are applied.

Removing a setting or a device from `dev.yml` is also followed. Only what
**`idev` itself applied** is in scope, though: settings you added by hand with
`incus config set` are left alone.

| Flag | Description |
| --- | --- |
| `--restart` | restart the instance if a change needs it to take effect |
| `--dry-run` | show the planned operations without changing anything in Incus |

```bash
idev up --restart
```

Restarts the instance when a change needs it to take effect (`raw.idmap`,
`security.nesting`, `security.privileged`). By default it only warns.

```bash
idev up --dry-run
```

Lists what would be created, re-applied and removed, and changes nothing in
Incus. Use it after editing `dev.yml`, to see what is about to happen.

If an instance of the same name exists that `idev` did not create, it fails
without doing anything, so it cannot destroy something it does not own.

---

## 3.4 `idev provision`

Re-runs bootstrap and provisioning without recreating the instance.

```bash
idev provision
```

Mainly for:

- when you have edited the provisioning steps
- when you have changed a playbook or a script

If the instance does not exist, it fails explicitly. It never silently falls
back to `idev up`.

It does not apply `instance.config` or `devices` changes — that is what
`idev up` is for.

### Running part of it

| Flag | Description |
| --- | --- |
| `--list` | list the steps (does not touch Incus) |
| `--step <name or number>` | run only the given steps (may be repeated) |
| `--from <name or number>` | run from the given step onwards |

```bash
idev provision --list
idev provision --step 3
idev provision --step setup-go --step install-tools
idev provision --from 2
```

Repeating `--step` still runs the steps in **declaration order**, not in the
order you listed them. Step numbers in failure messages are always positions in
the whole list, even for a partial run.

`--step` and `--from` cannot be combined.

---

## 3.5 `idev shell`

Runs a shell, or a given command, inside the container.

```bash
idev shell                      # interactive shell, starting in /workspace
idev shell -- make test         # run a command and exit
idev shell -- sh -c 'cd /workspace && go build ./...'
```

- A pseudo-terminal is allocated only when stdin and stdout are attached to a
  terminal, so output is not mangled through a pipe or a redirect

```bash
idev shell -- cat /etc/os-release > os.txt
idev shell -- go test ./... | tee test.log
```

- If the container is stopped, it is started first
- The user, shell and working directory come from `shell` in `dev.yml`
  (root, `/bin/sh` and `workspace.target` by default — see
  [4. dev.yml](04-dev-yml.md))

The command's exit code becomes `idev`'s exit code.

---

## 3.5.1 `idev exec`

Runs a command inside the container. **No pseudo-terminal is allocated.**

```bash
idev exec -- make test
idev exec -- go test ./... | tee test.log
```

The only difference from `idev shell` is that it never allocates a
pseudo-terminal, even when attached to one. In CI and in scripts, use `exec`:
its behaviour does not vary with the environment.

A command is required. Without one it fails, pointing you at `idev shell`.

---

## 3.6 `idev status`

Shows the state of the instance.

```bash
idev status
```

If the instance has not been created, `Status: NOT CREATED` is shown and the
command still succeeds (exit code `0`).

Machine-readable output:

```bash
idev status --json
```

```json
{
  "project": "my-project",
  "instance": "dev-my-project",
  "status": "Running",
  "image": "images:ubuntu/24.04",
  "workspace": "/workspace",
  "workspace_source": "/home/you/src/my-project",
  "exists": true,
  "managed": true,
  "profiles": ["default"],
  "config": { "limits.cpu": "4" },
  "devices": ["workspace(disk)"],
  "provision_steps": 1,
  "runtime": "container",
  "incus_project": "default"
}
```

`profiles`, `config`, `devices` and `runtime` are omitted when the instance
does not exist.

```bash
# branch on whether it is running
[ "$(idev status --json | jq -r .status)" = "Running" ] || idev up
```

---

## 3.7 `idev destroy`

Deletes the instance.

```bash
idev destroy
idev destroy --force        # skip the confirmation, for CI and scripts
```

- **The source tree on the host is not deleted.** The workspace is a bind
  mount; only the container goes away
- An instance `idev` did not create is not deleted

| Flag | Description |
| --- | --- |
| `-f`, `--force` | skip the confirmation prompt |
| `--volumes` | also delete the persistent volumes declared in `volumes` |

Persistent volumes are kept by default, since the point of them is usually to
survive a recreated instance and keep a cache warm. When they are kept, it says
so.

---

## 3.8 `idev rebuild`

Destroys the instance and creates it again.

```bash
idev rebuild
idev rebuild --force
```

Everything inside the container is lost. `idev up` follows removals too, but
only for the settings and devices `idev` applied; use `rebuild` when you want a
clean state including anything added by hand.

---

## 3.8.1 `idev snapshot`

Save a state before you break something, and go back to it later.

```bash
idev snapshot create before-upgrade
idev snapshot list
idev snapshot restore before-upgrade
idev snapshot delete before-upgrade
```

Without a name, the current date and time are used (`20260831-142530`).
`restore` and `delete` ask for confirmation (`--force` skips it).

**The workspace on the host is unaffected**: it is a bind mount, not part of
the instance's state.

---

## 3.9 Shell completion

```bash
idev completion bash > /etc/bash_completion.d/idev
idev completion zsh  > "${fpath[1]}/_idev"
idev completion fish > ~/.config/fish/completions/idev.fish
```
