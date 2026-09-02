# 1. Installation

*[日本語版 / Japanese](ja/01-installation.md)*

## 1.1 Requirements

| Where | What you need |
| --- | --- |
| Host | Linux, with Incus (tested against the 6.0 series), and the `idev` binary |
| Host, only for `ansible` steps | `ansible-playbook`, the `community.general` collection |
| Container | Nothing. Do not install an SSH server |

`idev` is a single static binary and needs no language runtime at run time.

It talks to Incus through the Go client library, so the `incus` command is not
required. It does read the same configuration as `incus`
(`~/.config/incus/config.yml`) to resolve remotes and images, which means any
Incus you can reach with `incus` is reachable from `idev` as well.

Whether to use Ansible is **the project's choice**. If you provision with shell
scripts alone, you do not need it.

## 1.2 Preparing Incus

Confirm that Incus is usable. If you have the `incus` command, that is the
easiest way.

```bash
incus info | head -3
incus profile list
```

If the `default` profile exists, you can start right away.

If you have not installed the `incus` command, you can check from a project
directory after installing `idev` (1.3).

```bash
idev status
```

If it can reach Incus, it prints the state even when no instance exists yet.
If it cannot, it says so
(see [troubleshooting](../troubleshooting.md#6-incus-api-calls-fail)).

## 1.3 Installing idev

### From a release (recommended)

Every release ships a Linux archive for amd64 and arm64, plus a
`checksums.txt`. Download the one for your architecture, check it, and put the
binary on your `PATH`.

```bash
sha256sum --check --ignore-missing checksums.txt
tar -xzf incus-dev_<version>_linux_amd64.tar.gz
sudo install -m 0755 incus-dev_<version>_linux_amd64/idev /usr/local/bin/idev
```

**idev is for Linux.** It operates the Incus on the machine it runs on, and the
Incus client refuses a local connection on any other platform, so nothing that
reaches Incus works on macOS or Windows. No binaries are released for them.

### From source

```bash
git clone https://github.com/lambdasakura/incus-dev.git
cd incus-dev

make build          # produces ./bin/idev
sudo install -m 0755 bin/idev /usr/local/bin/idev
```

With a Go toolchain, this works too.

```bash
make install        # into $GOBIN
```

Or without a checkout at all:

```bash
go install github.com/lambdasakura/incus-dev/cmd/idev@latest
```

A binary built this way reports `dev` for `idev --version`, because the version
is stamped in at release time.

Check:

```bash
idev --version
idev --help
```

## 1.4 If you use Ansible

```bash
ansible-galaxy collection install community.general

# check the connection plugin is available
ansible-doc -t connection community.general.incus
```

idev does not use SSH; it connects to the container through this connection
plugin. You do not need to install an SSH server in the container.

## 1.5 Workspace ownership (optional, but recommended)

To own files created inside the container as yourself on the host, add the
following to `/etc/subuid` and `/etc/subgid`.

```text
root:1000:1        # 1000 is your uid (id -u)
```

```bash
id -u; id -g                        # your uid/gid
grep '^root:' /etc/subuid /etc/subgid
```

No Incus restart is needed afterwards — the file is read when the container
starts.

`idev` works without this. It then falls back to an idmapped mount, and files
the container creates are owned by root on the host.

For details see `workspace.idmap` in [04-dev-yml.md](04-dev-yml.md) and
[troubleshooting](../troubleshooting.md#2-workspace-is-owned-by-the-wrong-user--not-writable).

## 1.6 If you also run Docker

With Docker installed on the host, containers may lose outbound network access.
Docker sets the kernel's forwarding policy to DROP, and no amount of Incus
configuration fixes that.

If package installation fails during `idev up`, see
[troubleshooting 1](../troubleshooting.md#1-no-network-access-from-the-container).

## 1.7 Smoke test

Create a minimal project anywhere and check it works.

```bash
mkdir -p /tmp/idev-check/.incus-dev && cd /tmp/idev-check
cat > .incus-dev/dev.yml <<'YAML'
schema: 1
project:
  name: idev-check
instance:
  image: images:alpine/3.21
YAML

idev validate
idev up
idev shell -- cat /etc/os-release
idev destroy --force
```

`idev validate` changes nothing in Incus, so it is a good first thing to get
passing.
