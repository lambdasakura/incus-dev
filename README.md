# incus-devkit

Incusを利用して、プロジェクト単位の開発環境を再現可能な形で構築・管理するCLIツール。

```bash
git clone <repository>
cd <repository>

dev up
dev shell
```

- インフラ（コンテナ・リソース・マウント）は **Incus** が管理する
- コンテナ内部（OS設定・パッケージ・ランタイム）は **Ansible** が管理する
- `dev` CLIは **Go** で実装し、単一バイナリとして配布する

## ドキュメント

仕様書は [docs/spec/](docs/spec/README.md) を参照。
