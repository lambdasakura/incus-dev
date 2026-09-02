# Troubleshooting

The usual ways `idev` fails because of the *host* environment, and what to do
about them.

*[日本語版 / Japanese](troubleshooting.ja.md)*

None of these are about how you wrote `dev.yml`. They are all about
**assumptions the host has to satisfy**.

| Symptom | Section |
| --- | --- |
| `apt-get` / `apk` fails during a provisioning step; the container cannot resolve names or download anything | [1](#1-no-network-access-from-the-container) |
| Cannot write to the workspace; files end up owned by root on the host | [2](#2-workspace-is-owned-by-the-wrong-user--not-writable) |
| `incus profile(s) not found on this host` | [3](#3-profile-not-found) |
| `is not managed by idev`, `already belongs to project` | [4](#4-instance-is-said-to-be-unmanaged) |
| An `ansible` step fails | [5](#5-an-ansible-step-fails) |
| Cannot reach Incus; API calls fail | [6](#6-incus-api-calls-fail) |

---

## 1. No network access from the container

### Symptom

Package installation fails during a provisioning step of `idev up`.

```text
WARNING: fetching https://dl-cdn.alpinelinux.org/alpine/v3.21/main: temporary error (try again later)
```

The container has an IP address and DNS resolves, but nothing reaches the
outside.

### Cause 1: the network is not up yet

When an instance has just started and can already run commands, it may not have
an IPv4 address or a default route yet.

`idev` waits for the IPv4 address before it starts provisioning, so this
normally does not happen. If `idev up` printed a `network address not assigned`
warning, suspect the network configuration instead.

### Cause 2: Docker

**If Docker is installed on the host, Docker's and Incus's firewall rules
conflict.**

Incus allows its own bridge in its own nftables table.

```text
table inet incus / chain fwd.incusbr0 (policy accept)
  ip version 4 iifname "incusbr0" accept
  ip version 4 oifname "incusbr0" accept
```

Docker, meanwhile, **sets the FORWARD chain policy in the `ip filter` table to
DROP**.

netfilter evaluates every chain registered on the same hook (forward), and
**a DROP anywhere discards the packet immediately**. Incus's accept cannot
override Docker's DROP, so only forwarding through incusbr0 breaks.

### Checking

```bash
# Is the FORWARD policy DROP?
sudo iptables -S FORWARD

# Confirm nothing allows incusbr0
sudo iptables -L DOCKER-CT -v -n       # only Docker's own bridges get return traffic
sudo iptables -L DOCKER-USER -v -n     # empty here means nothing has been done
```

### Fix

The `DOCKER-USER` chain is evaluated at the head of FORWARD, so allow the
traffic there.

```bash
sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT
sudo iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
```

**Both lines are required.**

- Line 1: outbound, container to the outside
- Line 2: the return path. Docker's own return rule (`DOCKER-CT`) only applies
  to Docker's bridges (`docker0`, `br-*`), so without this you get "packets go
  out, nothing comes back"

The packet counters tell you whether traffic is actually taking these rules.

```bash
sudo iptables -L DOCKER-USER -v -n
```

### Making it persist (important)

`iptables -I` only changes the in-memory ruleset, and is **lost on reboot**. If
the symptom comes back after a reboot, this is why.

Where `netfilter-persistent` and friends are not installed, re-applying the
rules from a systemd unit is reliable and narrow in scope.

```ini
# /etc/systemd/system/incus-docker-forward.service
[Unit]
Description=Allow incusbr0 traffic through Docker's FORWARD chain
After=docker.service
Requires=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'iptables -C DOCKER-USER -i incusbr0 -j ACCEPT 2>/dev/null || iptables -I DOCKER-USER -i incusbr0 -j ACCEPT'
ExecStart=/bin/sh -c 'iptables -C DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT'

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now incus-docker-forward.service
```

Alternatively, on Docker 27 and later the following stops Docker from setting
the FORWARD policy to DROP at all. It is a config file, so it persists — but it
affects every bridge.

```json
// /etc/docker/daemon.json
{ "ip-forward-no-drop": true }
```

### If you use ufw

When ufw is **enabled**, you need the following as well as the rules above.

```bash
sudo ufw allow in on incusbr0          # container -> host (DHCP / DNS)
sudo ufw route allow in on incusbr0    # forwarding, outbound
sudo ufw route allow out on incusbr0   # forwarding, return
```

When ufw is **disabled** (`ufw status` says `inactive`), these are merely
written to `/etc/ufw/user.rules` and have no effect. They will be needed the
moment you enable ufw, so adding them ahead of time does no harm.

Note that enabling ufw does not remove the need for the two `DOCKER-USER`
lines: Docker's chains are evaluated before ufw's.

---

## 2. Workspace is owned by the wrong user / not writable

### Symptom

- You cannot write to `/workspace`, or to a host directory mounted through
  `instance.devices`, from inside the container
- Files created in the container are owned by root on the host, and you cannot
  delete them without sudo
- `idev up` stops with `workspace idmap (raw.idmap) is not permitted on this host`

### Cause and fix

Sharing a host directory into an unprivileged container needs a uid/gid
mapping. `workspace.idmap` chooses how ([manual 4.6](manual/04-dev-yml.md#46-workspace)).

| Value | Extra host setup | Host-side owner of container-created files |
| --- | --- | --- |
| `auto` (default) | none | depends on the host: the invoking user if `raw` works, otherwise root |
| `raw` | required | the invoking user |
| `shift` | none | root |
| `none` | none | (not writable) |

`raw` — the behaviour you usually want — requires the Incus daemon (running as
root) to be permitted to map your uid/gid.

```text
/etc/subuid: root:1000:1
/etc/subgid: root:1000:1
```

**This is not the same as the `root:1000000:1000000000` line that typical Incus
setup instructions have you add.** That range is for shifting container uids up
into the host's 1000000+ space; it does not grant permission to bring host uid
1000 *into* a container.

Without it, the container fails to start:

```text
newuidmap: uid range [0-1) -> [1000-1001) not allowed
```

No Incus restart is needed after adding it — `newuidmap` reads the file when
the container starts.

```bash
grep '^root:' /etc/subuid /etc/subgid
idev up
incus config get dev-<project> raw.idmap    # "uid <uid> 0 / gid <gid> 0" means it applied
```

If you do not add it, the default `auto` falls back to `shift` (an idmapped
mount) and keeps going. The workspace is still readable and writable, but files
the container creates are owned by root on the host.

### On WSL, `shift` is not available

```text
Failed to setup device mount "workspace": idmapping abilities are required
but aren't supported on system
```

`shift` needs a kernel that can shift ids on a mount, and WSL's cannot. Check
what your daemon reports:

```bash
incus info | grep idmapped_mounts
```

`false`, or no line at all, means `shift` will never work on that host. **Add
the `/etc/subuid` and `/etc/subgid` entries above and use `raw`** -- it needs
no kernel feature. `none` also works if you do not need to write to the
workspace from inside.

idev refuses `shift` up front on such a host rather than letting the mount
fail, so a current version tells you this instead of the message above. The
message above comes from Incus, when something else asked for `shift`.

---

## 3. Profile not found

```text
incus profile(s) not found on this host: gpu-nvidia
idev does not create profiles; create them or remove them from instance.profiles
```

`idev` neither ships nor creates Incus profiles. `instance.profiles` is a
**reference by name to profiles that already exist on the host**.

Either:

- create the profile on the host, or
- drop it from `instance.profiles` and write what you need directly into
  `instance.config` / `instance.devices`

If you go as far as `profiles: []` (no profiles at all), remember that the root
disk and the network also come from a profile, so you have to declare them
yourself.

```yaml
instance:
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

---

## 4. Instance is said to be unmanaged

```text
instance dev-example exists but is not managed by idev for project "example"
instance dev-example already belongs to project "Example"
```

`idev` marks the instances it creates, and refuses to touch unmarked ones so it
cannot destroy something it did not make.

The second wording means the instance **is** idev's, for a different project.
The instance name is lower-cased and turns `.` and `_` into `-`, so several
project names ask for one instance. Do not delete that one -- it is another
checkout's environment. Rename one of the projects.

```bash
incus config get dev-<project> user.incus-dev.project
```

If you created an instance of the same name by hand, either delete it or change
`project.name` so the instance name no longer collides.

---

## 5. An `ansible` step fails

### On the host

You need `ansible-playbook` and the `community.general` collection. `idev` does
not ship either.

```bash
ansible-galaxy collection install community.general
ansible-doc -t connection community.general.incus   # check it is installed
```

### In the container

Ansible modules need Python inside the container. If you omitted `bootstrap`
and your `provision` has an `ansible` step, `idev` tries to install Python with
its default bootstrap, which assumes a Debian-family image.

On anything else that fails, so declare `bootstrap` explicitly.

```yaml
bootstrap:
  - run: command -v python3 >/dev/null 2>&1 || dnf install -y python3
```

Because it installs a package, this path depends on **outbound network access
from the container**. If it fails, check
[1](#1-no-network-access-from-the-container) first.

---

## 6. Incus API calls fail

`idev` calls the Incus API directly through the Go client library. It reads the
same configuration as the `incus` command (`~/.config/incus/config.yml`) to
resolve the endpoint and certificates.

### Check this first

```bash
incus info | head -3        # does the same configuration connect?
```

If the `incus` command cannot connect either, the problem is Incus, not `idev`.

### Check the configuration

Where the `incus` command is installed, you can compare against it.

```bash
incus remote list           # remotes, and which is the default
incus project list          # projects
```

idev always talks to the `local` remote, and never follows the default
`incus remote switch` sets. Confirm that the name you passed to
`--incus-project` appears in that list.
