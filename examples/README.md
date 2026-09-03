# examples

Worked examples of a project-side `.incus-dev/`.

*[日本語版 / Japanese](README.ja.md)*

These exist **as documentation only**. `idev` never reads them at run time, and
they are not embedded in the binary.

| Example | Contents |
| --- | --- |
| [minimal](minimal/) | A single `dev.yml`, nothing else |
| [shell-based](shell-based/) | Provisioned with shell scripts; no Ansible |
| [ansible-based](ansible-based/) | Provisioned with Ansible |
| [dev-user](dev-user/) | Provisioning creates an ordinary account; `idev shell` lands in it (`idmap: shift`) |
| [dev-user-raw](dev-user-raw/) | The same, mapped with `raw` and `workspace.owner` -- the way that works on WSL |
| [volumes](volumes/) | Several persistent volumes that survive `idev rebuild` |
| [multi-workspace](multi-workspace/) | Several host directories mounted, one of them read-only |

You can run the following inside any of these directories.

```bash
idev validate
idev up
```

Each example is explained in [docs/spec/10-examples.md](../docs/spec/10-examples.md)
(Japanese).
