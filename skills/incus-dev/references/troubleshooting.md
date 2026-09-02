# Looking up an error

Search by the error text `idev` printed. Problems caused by the host
environment are covered in more depth in the repository's
[docs/troubleshooting.md](../../../docs/troubleshooting.md).

## Configuration and usage

### `instance <name> does not exist; run 'idev up' first`

You called `provision` / `shell` / `exec` / `status` before the environment
existed. Run `idev up` first. It never creates one implicitly, so that nothing
gets built by accident.

### `instance <name> exists but is not managed by devkit for project "<name>"`

An instance of the same name was created by hand. devkit does not destroy an
instance without its mark (`user.incus-devkit.project`). Either delete the
existing one, or change `project.name` so the names no longer collide.

### `incus profile(s) not found on this host: <name>`

`instance.profiles` is a **reference by name to profiles that already exist on
the host**; devkit does not create them. Either:

- create the profile on the host, or
- drop it from `instance.profiles` and write what you need directly into
  `instance.config` / `instance.devices` — the more portable option

### `cannot resolve secret(s): ...`

A host environment variable or file referenced by `secrets:` is missing. It
**stops before touching the instance**, so nothing is broken. Provide the
value, or add `optional: true` if it is genuinely optional.

### `configuration is invalid` / schema errors

The output of `idev validate` names the path inside `dev.yml`. Common causes:

- A referenced file does not exist (playbook, vars, a device's source)
- A step has none of `run` / `ansible` / `galaxy`, or more than one
- A reserved key (`user.incus-devkit.*`) was used

## At run time

### Package installation fails during provisioning (`temporary error`; DNS resolves but nothing gets out)

**Almost always a host-side network problem.** Editing `dev.yml` will not fix
it. With Docker installed on the host, Docker's FORWARD policy (DROP) catches
the Incus bridge in the crossfire.

```bash
sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT
sudo iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
```

Both lines are needed (the first for outbound, the second for the return
path). They are lost on reboot, so make them persistent. See section 1 of
docs/troubleshooting.md.

### `instance <name> did not become ready within <duration>`

It started, but never reached the point of being able to run commands. The
trailing exit code or error points at the cause. Suspect a broken image, or an
`instance.config` setting that stops init from starting.

### A step fails after a `network address not assigned` warning

Provisioning started before an IPv4 address was assigned. devkit waits for
IPv4, so this is unusual; when it appears, suspect the network configuration
(including the Docker conflict above).

### `<step name>: exited with code N`

A script inside the container failed. The error shows the first line of the
script. To reproduce and narrow it down:

```bash
idev provision --list          # find the step number
idev exec -- sh -c '<the failing command>'   # try it by hand
idev provision --step 3        # re-run just that step
```

## Workspace and permissions

### `workspace idmap (raw.idmap) is not permitted on this host`

You asked for `workspace.idmap: raw`, and the host does not permit it. The
error names the line to add (`root:<uid>:1` in `/etc/subuid` and
`/etc/subgid`). No Incus restart is needed afterwards. If you would rather not
change host configuration, use `workspace.idmap: shift` — files the container
creates are then owned by root on the host.

### Cannot write to `/workspace`, or output is owned by root

A `workspace.idmap` question. The default `auto` falls back to `shift` when
`raw` is unavailable, and under `shift` the host-side owner is root.

### A change is not taking effect

- Changed `provision` → `idev provision`
- Changed `instance.config` / `devices` / `volumes` → **`idev up`**
- Changed a setting that needs a restart → `idev up --restart`

## Cannot reach Incus

```text
connect to the local incus: The incus daemon doesn't appear to be started
```

`idev` reads the same configuration as the `incus` command
(`~/.config/incus/config.yml`). Check whether `incus info` works first. `idev`
always talks to the `local` remote, and does not follow the default set by
`incus remote switch`. A remote Incus is out of scope.
