# incus-devkit

Incusを利用して、プロジェクト単位の開発環境を再現可能な形で構築・管理するCLIツール。

```bash
git clone <repository>
cd <repository>

idk up
idk shell
```

## 設計方針

devkitは以下に特化する。

- Incus instanceのライフサイクル管理
- workspace（プロジェクトのworking tree）のマウント
- コンテナのbootstrap
- `.incus-dev/` に宣言された手順の実行

**devkitは環境固有の内容を持たない。**
Ansible Role・Incus Profile・言語ランタイムの導入手順は同梱せず、
すべてプロジェクトの `.incus-dev/` が所有する。

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

`idk` は Go で実装し、単一バイナリとして配布する。

## ドキュメント

仕様書は [docs/spec/](docs/spec/README.md) を参照。
