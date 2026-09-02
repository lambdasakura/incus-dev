# 2. Building your first project

A walk through adding a development environment definition to an existing
project.

*[日本語版 / Japanese](ja/02-getting-started.md)*

## 2.1 Add the definition file

At the root of your project:

```bash
mkdir -p .incus-dev
```

```yaml
# .incus-dev/dev.yml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
```

Only three things are required: `schema`, `project.name` and `instance.image`.

```bash
idev validate
```

```text
configuration is valid
Project:    my-project
Instance:   dev-my-project-cb958c73
Provision:  0 step(s)
```

`validate` never touches Incus, so it is safe to run in CI.

## 2.2 Bring it up

```bash
idev up
```

```text
[idev] Project: my-project
[idev] Creating instance dev-my-project-cb958c73
[idev] Mounting workspace /home/you/src/my-project -> /workspace
[idev] Starting instance dev-my-project-cb958c73
[idev] Development environment is ready
```

Your working tree is now visible at `/workspace` inside the container.

```bash
idev shell
```

```text
# cd /workspace && ls
README.md  src  .incus-dev
```

Edit a file on the host and the container sees it immediately. It is not a
copy.

## 2.3 Install what you need

Provisioning steps go in `provision`, and run top to bottom.

```yaml
# .incus-dev/dev.yml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
  config:
    limits.cpu: "4"
    limits.memory: 8GiB

provision:
  - name: base packages
    run: |
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends git make jq
```

```bash
idev up
```

The instance already exists, so it is not recreated. The configuration changes
(CPU, memory) are applied and provisioning runs again.

To run only provisioning, without recreating the container:

```bash
idev provision
```

## 2.4 Write steps so they can be re-run

`idev provision` is meant to be run any number of times, so **making each step
re-runnable is the project's responsibility**.

```yaml
provision:
  # Good: does nothing if it is already installed
  - run: command -v jq >/dev/null 2>&1 || apt-get install -y jq

  # Good: apt install is re-runnable to begin with
  - run: apt-get install -y --no-install-recommends make

  # Bad: appends another line every single run
  - run: echo 'export PATH=$PATH:/opt/bin' >> /root/.bashrc
```

Checking this is simple: run it twice in a row and see that it succeeds.

```bash
idev provision && idev provision
```

## 2.5 Check the state

```bash
idev status
```

```text
Project:    my-project
Instance:   dev-my-project-cb958c73
Status:     Running
Image:      images:ubuntu/24.04
Workspace:  /home/you/src/my-project -> /workspace
Profiles:   default
Managed:    yes
limits.cpu: 4
Provision:  1 step(s)
```

## 2.6 Commit it

```bash
git add .incus-dev
git commit -m "Add development environment definition"
```

From now on, anyone who clones the repository reproduces the same environment
with `idev up` alone.

It is worth putting this in your README:

```markdown
## Development environment

    idev up
    idev shell
```

## 2.7 Clean up

```bash
idev destroy          # asks for confirmation
idev destroy --force  # does not
```

Only the instance is deleted. **The source tree on the host is left completely
alone.**

To start over — discarding everything inside the container:

```bash
idev rebuild --force
```

## What to read next

- Every setting in detail: [04-dev-yml.md](04-dev-yml.md)
- Using Ansible: [05-provisioning.md](05-provisioning.md)
- Examples by language and use case: [06-recipes.md](06-recipes.md)
