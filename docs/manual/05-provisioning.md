# 5. Writing the provisioning steps

All `idev` provides is the machinery to "run the declared steps, in the
declared order". **What to run is the project's decision.**

*[日本語版 / Japanese](ja/05-provisioning.md)*

## 5.1 Order of execution

```text
create / start the instance
        ↓
wait until commands can run
        ↓
bootstrap
        ↓
provision[0] → provision[1] → ...
```

Both `idev up` and `idev provision` go through this every time. On the first
failure it stops, and no later step runs.

A failed step is identified by its position and its name.

```text
[idev] error: provision step 2/3: install deps: exec in dev-my-project: ... (exit code 1)
```

---

## 5.2 `run` steps

Runs a script inside the container.

### Short form

```yaml
provision:
  - run: apt-get update
```

### Full form

```yaml
provision:
  - name: install packages
    run: |
      apt-get update
      apt-get install -y --no-install-recommends jq make
    shell: /bin/sh                 # default /bin/sh
    cwd: /workspace                # working directory, inside the container
    user: root                     # user to run as
    env:
      DEBIAN_FRONTEND: noninteractive
```

| Field | Description |
| --- | --- |
| `run` | The script to run (required) |
| `name` | Display name, used in logs and errors |
| `shell` | The shell that interprets the script |
| `cwd` | Working directory |
| `user` | User to run as; a numeric uid or a name |
| `env` | Extra environment variables |

### Running a script file

`.incus-dev/` is part of the workspace and therefore visible from the
container, so your scripts can live in files.

```yaml
provision:
  - name: setup
    run: sh /workspace/.incus-dev/scripts/setup.sh
```

```sh
#!/bin/sh
# .incus-dev/scripts/setup.sh
set -eu

echo "setting up ${IDEV_PROJECT_NAME}"
command -v jq >/dev/null 2>&1 || apt-get install -y jq
```

Once the logic runs past a few lines, a file is the better home for it — you
get shell syntax checking and editor support.

---

## 5.3 `ansible` steps

Runs `ansible-playbook` on the host. SSH is not involved.

```yaml
provision:
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml    # required, from the project root
      vars: .incus-dev/ansible/vars.yml        # optional
      inventory: .incus-dev/ansible/hosts.yml  # optional, an additional inventory
      tags: [setup]                            # optional
      skip_tags: [slow]                        # optional
      extra_args: ["--diff"]                   # optional
```

### Writing the playbook

The target host is always named `dev`. idev generates a temporary inventory and
passes it in.

```yaml
# .incus-dev/ansible/site.yml
---
- name: Provision development environment
  hosts: dev
  gather_facts: true

  roles:
    - role: base

  tasks:
    - name: Install packages
      ansible.builtin.apt:
        name:
          - protobuf-compiler
          - libssl-dev
        state: present
```

The generated inventory is equivalent to this.

```yaml
all:
  children:
    idev:
      hosts:
        dev:
          ansible_host: dev-my-project
          ansible_connection: community.general.incus
          ansible_incus_remote: local
          ansible_incus_project: default
```

### Roles and collections

Roles belong to the project. idev does not inject a role path, so set it in
`ansible.cfg`.

```ini
# .incus-dev/ansible/ansible.cfg
[defaults]
roles_path = .incus-dev/ansible/roles
stdout_callback = yaml
```

If that file exists, idev passes it as `ANSIBLE_CONFIG`.

For external collections, keep a `requirements.yml` and install it with a
`galaxy` step (below), so nothing outside `.incus-dev/` has to be arranged
first.

### Python inside the container

Ansible modules need Python in the container.

If you omit `bootstrap` and have an `ansible` step, idev tries to install
Python with its default bootstrap, which assumes a Debian-family image.

```sh
command -v python3 >/dev/null 2>&1 || (apt-get update && apt-get install -y python3)
```

On anything else that fails, so declare `bootstrap` explicitly.

```yaml
instance:
  image: images:fedora/41

bootstrap:
  - run: command -v python3 >/dev/null 2>&1 || dnf install -y python3
```

On an image that already has Python (`images:ubuntu/noble`, for instance), the
default bootstrap does nothing but confirm it.

---

## 5.4 `galaxy` steps

Runs `ansible-galaxy install` on the host, so the roles and collections a
playbook needs are installed from the project itself.

```yaml
provision:
  - name: collections
    galaxy:
      requirements: .incus-dev/ansible/requirements.yml

  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
```

| Field | Required | Description |
| --- | --- | --- |
| `requirements` | yes | path to requirements.yml, relative to the project root |
| `extra_args` | | passed straight to `ansible-galaxy` |

Where they land is Ansible's own default; idev does not choose. It needs
Ansible on the host, exactly as an `ansible` step does.

## 5.5 Variables idev passes in

So you do not have to hard-code instance names and paths, run-time information
is passed to every step.

### `run` steps (environment variables)

```text
IDEV_PROJECT_NAME       project name
IDEV_INSTANCE           instance name
IDEV_WORKSPACE          workspace path inside the container
IDEV_WORKSPACE_SOURCE   project root path on the host
IDEV_INCUS_PROJECT      Incus project
```

```yaml
provision:
  - run: |
      cd "$IDEV_WORKSPACE"
      make setup
```

A step's own `env` wins if it sets the same name.

### `ansible` steps (variables)

```yaml
idev_project_name: my-project
idev_instance: dev-my-project
idev_workspace: /workspace
idev_workspace_source: /home/you/src/my-project
idev_incus_project: default
```

```yaml
- name: Configure shell to start in the workspace
  ansible.builtin.copy:
    content: "cd {{ idev_workspace }}\n"
    dest: /etc/profile.d/workspace.sh
    mode: "0644"
```

These are passed before `vars`, so the project can override them.

---

## 5.6 Write steps so they can be re-run

`idev provision` runs repeatedly. Idempotence is the project's responsibility.

| Approach | How |
| --- | --- |
| Ansible | Prefer modules over `shell` / `command` |
| Shell | Check the state before changing it |

```sh
# do nothing if it is already installed
command -v jq >/dev/null 2>&1 || apt-get install -y jq

# generate, do not append
cat > /etc/profile.d/workspace.sh <<EOS
cd ${IDEV_WORKSPACE}
EOS

# check before creating
[ -d /opt/tools ] || mkdir -p /opt/tools
```

Checking this only takes running it twice.

```bash
idev provision && idev provision
```

---

## 5.7 Which to use

| | `run` | `ansible` |
| --- | --- | --- |
| Extra host requirements | none | ansible-playbook, community.general |
| Container requirements | a shell | Python |
| Idempotence | you write it | modules take care of it |
| Suits | up to a few dozen lines | large enough to want roles |

Small projects are usually fine with `run` alone. You can mix the two.

```yaml
provision:
  - name: apt update
    run: apt-get update

  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml

  - name: project setup
    run: |
      cd /workspace
      make deps
```
