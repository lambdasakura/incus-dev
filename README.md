# incus-devkit

`idev` — a CLI that builds and manages reproducible, per-project development
environments on [Incus](https://linuxcontainers.org/incus/).

*[日本語版 / Japanese](README.ja.md)*

[![CI](https://github.com/lambdasakura/incus-devkit/actions/workflows/ci.yml/badge.svg)](https://github.com/lambdasakura/incus-devkit/actions/workflows/ci.yml)

```bash
git clone <repository>
cd <repository>

idev up
idev shell
```

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
| Host | Incus, and the `idev` binary |
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

Prebuilt binaries for Linux, macOS and Windows (amd64 and arm64) are attached
to each [release](../../releases). Download the archive for your platform,
verify it against `checksums.txt`, and put `idev` on your `PATH`.

```bash
sha256sum --check --ignore-missing checksums.txt
tar -xzf incus-devkit_<version>_linux_amd64.tar.gz
sudo install -m 0755 idev /usr/local/bin/idev
```

`idev` is an API client, so it also runs on macOS and Windows against a remote
Incus. The Incus daemon itself runs on Linux.

## Building

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

Everything below is implemented, and verified by integration tests against a
real Incus daemon.

| Feature | State |
| --- | --- |
| `validate` / `up` / `status` / `shell` / `exec` / `provision` / `rebuild` / `destroy` / `snapshot` | Done |
| `run` / `ansible` / `galaxy` steps; bootstrap (default, overridden, disabled) | Done |
| `provision --step` / `--from` / `--list` (partial runs) | Done |
| `up --dry-run` / `up --restart` | Done |
| `status --json` | Done |
| Pass-through of instance config and devices, including removals | Done |
| Workspace mount and idmap (`auto` / `raw` / `shift` / `none`) | Done |
| `volumes` (persistent volumes) | Done |
| `secrets` (injected from host environment variables and files) | Done |
| `shell` (user / command / cwd), `incus.project` | Done |
| Incus operations via the Go client library | Done — the `incus` command is not required |
| `project.scope` (multiple checkouts / per-branch instances) | Done |
| `--incus-remote` | Flag is wired up but unverified; how to share the workspace is undecided |
| `instance.type: virtual-machine` | Unverified; workspace sharing assumes a container |
| `validate --check-host` | Not implemented |

## License

MIT. See [LICENSE](LICENSE).
