# incus-dev

`idev` — a CLI that builds and manages reproducible, per-project development
environments on [Incus](https://linuxcontainers.org/incus/).

*[日本語版 / Japanese](README.ja.md)*

[![CI](https://github.com/lambdasakura/incus-dev/actions/workflows/ci.yml/badge.svg)](https://github.com/lambdasakura/incus-dev/actions/workflows/ci.yml)

## Quick start

You need a Linux host with
[Incus](https://linuxcontainers.org/incus/docs/main/installing/) on it. Nothing
else: `idev` is a single static binary with no runtime dependencies.

### 1. Install idev

Take the archive for your platform from the
[latest release](../../releases/latest).

```bash
VERSION=0.1.0        # the release you want
curl -LO https://github.com/lambdasakura/incus-dev/releases/download/v$VERSION/incus-dev_${VERSION}_linux_amd64.tar.gz
tar -xzf incus-dev_${VERSION}_linux_amd64.tar.gz
sudo install -m 0755 incus-dev_${VERSION}_linux_amd64/idev /usr/local/bin/idev

idev --version
```

Other ways to install, including `go install`, are [below](#installing).

### 2. Declare the environment

In the project you want an environment for:

```bash
cd ~/your-project
mkdir -p .incus-dev

cat > .incus-dev/dev.yml <<'YAML'
schema: 1

project:
  name: your-project

instance:
  image: images:ubuntu/24.04

provision:
  - name: tools
    run: apt-get update && apt-get install -y build-essential git
YAML
```

### 3. Bring it up

```bash
idev validate   # check dev.yml; changes nothing in Incus
idev up         # create the instance, bootstrap it, run the provisioning
idev shell      # a shell inside, with the project mounted at /workspace
```

Commit `.incus-dev/` along with your code. From then on, anyone who clones the
project reproduces the same environment with `idev up`.

Next: the [tutorial](docs/manual/02-getting-started.md), or
[examples/](examples/) for worked configurations.

## What it does

`idev` does four things, and nothing else.

- Manages the lifecycle of an Incus instance
- Mounts your workspace (the project's working tree) into it
- Bootstraps the container
- Runs the steps declared in `.incus-dev/`

**`idev` ships nothing environment-specific.** No Ansible roles, no Incus
profiles, no language-runtime installers. Everything needed to reproduce an
environment lives in that project's own `.incus-dev/`.

Bundling those would drag in things most users do not need, and split the
answer to "what is this environment made of?" across two repositories.

## Usage

```yaml
# .incus-dev/dev.yml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
  config:
    limits.cpu: "8"
    limits.memory: 16GiB

provision:
  - name: setup
    run: sh /workspace/.incus-dev/scripts/setup.sh

  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
```

```bash
idev validate      # check dev.yml; changes nothing in Incus
idev up            # create the instance, then bootstrap and provision
idev status        # show the state (--json for machine-readable output)
idev ip            # print its address: ssh user@$(idev ip)
idev shell         # open a shell in the container
idev exec -- make test   # run a command; no terminal is allocated
idev provision     # re-run provisioning without recreating the instance
idev snapshot create before-upgrade   # save a state you can restore later
idev rebuild       # destroy and recreate
idev destroy       # delete the instance; sources on the host are untouched
```

The exit code of a command run inside the container is passed straight through
(`idev exec -- make test || exit 1`). In scripts and CI, use `idev exec`: its
behaviour does not change with the presence of a terminal.

See the **[manual](docs/manual/README.md)** for details, and
[examples/](examples/) for worked configurations.

## Requirements

| Where | What you need |
| --- | --- |
| Host | Linux, with Incus, and the `idev` binary |
| Host, for `ansible` steps | `ansible-playbook` and the `community.general` collection |
| Container | Nothing — no SSH server required |

No extra host configuration is needed. By default (`workspace.idmap: auto`),
`idev` maps the host user running it onto root in the container (`raw.idmap`)
when that is available, and falls back to an idmapped mount (`shift`) when it
is not.

To also own container-created files as yourself on the host, add the following
to `/etc/subuid` and `/etc/subgid` (no Incus restart needed). This is needed in
addition to the `root:1000000:1000000000` line that a typical Incus setup
already has.

```text
root:<uid>:1
root:<gid>:1
```

## Installing

Every [release](../../releases) carries a Linux binary for amd64 and arm64,
plus a `checksums.txt` to check it against.

```bash
sha256sum --check --ignore-missing checksums.txt
tar -xzf incus-dev_<version>_linux_amd64.tar.gz
sudo install -m 0755 incus-dev_<version>_linux_amd64/idev /usr/local/bin/idev
```

With a Go toolchain, no download is needed. A binary built this way reports
`dev` for `idev --version`, because the version is stamped in at release time.

```bash
go install github.com/lambdasakura/incus-dev/cmd/idev@latest
```

From a checkout:

```bash
make build     # ./bin/idev
make install   # into $GOBIN
```

## Development

```bash
make check              # lint + test (no Incus required)
make test-integration   # integration tests against a real Incus
```

`make lint` uses golangci-lint, falling back to gofmt and go vet when it is not
installed (`make tools` installs it).

If you are changing `idev` itself, [CLAUDE.md](CLAUDE.md) describes how the
project is developed and [docs/spec/](docs/spec/README.md) records the design
decisions behind it. Both are written in Japanese.

## Documentation

| | |
| --- | --- |
| [Manual](docs/manual/README.md) | Installation, tutorial, reference, recipes |
| [Troubleshooting](docs/troubleshooting.md) | Problems caused by the host environment |
| [skills/](skills/) | Agent Skills for AI coding tools |
| [Design specification](docs/spec/README.md) | Internal design (Japanese only) |

Japanese versions: [README.ja.md](README.ja.md),
[docs/manual/ja/](docs/manual/ja/README.md),
[docs/troubleshooting.ja.md](docs/troubleshooting.ja.md).

## When something breaks

The common host-side problems — Docker fighting Incus over the network,
workspace ownership, a missing profile — are covered in
[docs/troubleshooting.md](docs/troubleshooting.md).

## Status

All of the following is implemented. The core of it — the lifecycle,
provisioning, the workspace mount and the idmap — is verified by integration
tests against a real Incus daemon.

- `validate` / `up` / `status` / `shell` / `exec` / `provision` / `rebuild` /
  `destroy` / `snapshot`
- `run` / `ansible` / `galaxy` steps; bootstrap (default, overridden, disabled)
- `provision --step` / `--from` / `--list` (partial runs)
- `up --dry-run` / `up --restart`, and `status --json`
- Pass-through of instance config and devices, including removals
- Workspace mount and idmap (`auto` / `raw` / `shift` / `none`)
- `volumes` (persistent volumes) and `secrets` (injected from the host)
- `shell` (user / command / cwd), `incus.project`
- `project.scope` (multiple checkouts / per-branch instances)
- Incus through the Go client library, so the `incus` command is not required

## Not supported

A remote Incus and virtual machines are permanently out of scope, because both
would need a different way to share the workspace, and that is away from what
this tool is for: a per-project environment on the machine in front of you.

| | |
| --- | --- |
| A remote Incus | `idev` always talks to the local one, and does not follow `incus remote switch` |
| Virtual machines | An instance is always a container; there is no `instance.type` |
| macOS and Windows | Talking to the local Incus is what `idev` does, and the client refuses a local connection anywhere but Linux. Only Linux binaries are released |

## License

MIT. See [LICENSE](LICENSE).
