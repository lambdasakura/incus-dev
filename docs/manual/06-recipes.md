# 6. Recipes

Examples you can paste straight into `.incus-dev/dev.yml`.

*[日本語版 / Japanese](ja/06-recipes.md)*

Complete, working samples also live in [examples/](../../examples/).

## 6.1 Minimal

```yaml
schema: 1
project:
  name: my-project
instance:
  image: images:ubuntu/24.04
```

Starts a bare container with the workspace mounted.

---

## 6.2 Go

```yaml
schema: 1

project:
  name: my-go-project

instance:
  image: images:ubuntu/24.04
  config:
    limits.cpu: "8"
    limits.memory: 8GiB

provision:
  - name: toolchain
    run: sh /workspace/.incus-dev/scripts/setup-go.sh
    env:
      GO_VERSION: "1.25.0"
```

```sh
#!/bin/sh
# .incus-dev/scripts/setup-go.sh
set -eu
: "${GO_VERSION:?}"

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl git make

if [ ! -x /usr/local/go/bin/go ] ||
   ! /usr/local/go/bin/go version | grep -q "go${GO_VERSION}"; then
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tgz
    rm -f /tmp/go.tgz
fi

cat > /etc/profile.d/go.sh <<'EOS'
export PATH=$PATH:/usr/local/go/bin
export GOPATH=/workspace/.go
EOS
```

---

## 6.3 Python

```yaml
schema: 1

project:
  name: my-python-project

instance:
  image: images:ubuntu/24.04

provision:
  - name: python
    run: |
      set -eu
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends \
        python3 python3-venv python3-pip build-essential

  - name: dependencies
    run: |
      set -eu
      cd /workspace
      [ -d .venv ] || python3 -m venv .venv
      ./.venv/bin/pip install --upgrade pip
      [ -f requirements.txt ] && ./.venv/bin/pip install -r requirements.txt || true
```

Putting the virtualenv at `/workspace/.venv` makes it visible from the host
too. Do not forget to add it to `.gitignore`.

---

## 6.4 Node.js

```yaml
schema: 1

project:
  name: my-node-project

instance:
  image: images:ubuntu/24.04

provision:
  - name: nodejs
    run: |
      set -eu
      command -v node >/dev/null 2>&1 && exit 0
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends ca-certificates curl
      curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
      apt-get install -y nodejs

  - name: dependencies
    run: |
      cd /workspace
      [ -f package-lock.json ] && npm ci || true
```

Native modules in `node_modules` are OS-specific, so sharing that directory
between the host and the container can break it. If that happens, consider
putting it outside the workspace.

---

## 6.5 Docker inside the container

```yaml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
  config:
    security.nesting: "true"     # this is what makes it work

provision:
  - name: docker
    run: |
      set -eu
      command -v docker >/dev/null 2>&1 && exit 0
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends ca-certificates curl
      curl -fsSL https://get.docker.com | sh
```

Because the setting goes directly into `instance.config`, no dedicated profile
has to exist on the host.

---

## 6.6 Running a database alongside

Run the service inside the container and reach it from the host over a port.

```yaml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
  devices:
    postgres:
      type: proxy
      listen: tcp:127.0.0.1:15432      # host side
      connect: tcp:127.0.0.1:5432      # container side

provision:
  - name: postgres
    run: |
      set -eu
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends postgresql
      systemctl enable --now postgresql || service postgresql start
```

From the host: `psql -h 127.0.0.1 -p 15432`.

---

## 6.7 GPU

```yaml
schema: 1

project:
  name: my-ml-project

instance:
  image: images:ubuntu/24.04
  devices:
    gpu0:
      type: gpu
      gputype: physical
```

This depends on host specifics — whether there is a GPU, and from which vendor
— so referring to a profile prepared by the host's administrator is sometimes
the more portable choice.

```yaml
instance:
  profiles:
    - default
    - host-gpu
```

On a host without that profile, `idev up` fails explicitly.

---

## 6.8 Depending on no host profile at all

```yaml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
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

Note that the storage pool and network names then become host-specific.

---

## 6.9 Mounting several repositories

Add them to `instance.devices` and directories other than the workspace are
shared too. They get the same uid/gid mapping as the workspace, automatically.

```yaml
instance:
  devices:
    other-repo:
      type: disk
      source: ../other-repo      # from the project root
      path: /other-repo
```

---

## 6.10 Mounting extra data

```yaml
instance:
  devices:
    dataset:
      type: disk
      source: /srv/dataset        # absolute path on the host
      path: /data
      readonly: "true"

    assets:
      type: disk
      source: ./assets            # from the project root
      path: /assets
```

---

## 6.11 Using it from CI

```bash
idev validate                     # no Incus needed; only checks the configuration
```

On a runner with Incus available, you can build the environment for real.

```bash
idev up
idev exec -- make test
idev destroy --force
```

`idev exec -- <command>` passes the command's exit code straight through, so it
works as a CI pass/fail check as it is.

---

## 6.12 An Ansible-based layout

```text
my-project/
└── .incus-dev/
    ├── dev.yml
    └── ansible/
        ├── ansible.cfg
        ├── site.yml
        ├── vars.yml
        ├── requirements.yml
        └── roles/
            └── base/
```

```yaml
# .incus-dev/dev.yml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04

provision:
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
      vars: .incus-dev/ansible/vars.yml
```

See [05-provisioning.md](05-provisioning.md) 5.3 for how to write it.
