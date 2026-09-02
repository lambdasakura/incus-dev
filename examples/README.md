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

You can run the following inside any of these directories.

```bash
idev validate
idev up
```

Each example is explained in [docs/spec/10-examples.md](../docs/spec/10-examples.md)
(Japanese).
