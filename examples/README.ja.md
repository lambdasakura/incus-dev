# examples

*[English version](README.md)*

プロジェクト側 `.incus-dev/` の構成例。

これらは **ドキュメントとしてのみ** 存在し、`idev` の実行時には参照されない。
バイナリにも同梱しない。

| 例 | 内容 |
| --- | --- |
| [minimal](minimal/) | `dev.yml` 1ファイルのみ |
| [shell-based](shell-based/) | シェルスクリプトで構成（Ansible不要） |
| [ansible-based](ansible-based/) | Ansibleで構成 |

各例のディレクトリで以下を実行できる。

```bash
idev validate
idev up
```

構成の説明は [docs/spec/10-examples.md](../docs/spec/10-examples.md) を参照。
