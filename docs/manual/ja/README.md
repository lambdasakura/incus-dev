# idev マニュアル

Incusでプロジェクト単位の開発環境を構築・管理するCLIツール `idev` の使い方。

*[English version](../README.md)*

## これは何をするツールか

プロジェクトのリポジトリに `.incus-dev/dev.yml` を1つ置いておくと、
`git clone` した人が以下だけで同じ開発環境を再現できる。

```bash
git clone <repository>
cd <repository>

idev up
idev shell
```

- ソースコードはコピーされず、ホストのworking treeがコンテナへマウントされる。
  IDE・Git・AIコーディングツールはホスト側から、ビルド・テスト・実行はコンテナ内で行える
- コンテナの中身をどう構成するかは **プロジェクトが決める**。
  idev はAnsible Roleも言語ランタイムの導入手順も持たない

## 目次

| # | ドキュメント | 内容 |
| --- | --- | --- |
| 1 | [01-installation.md](01-installation.md) | 前提と導入、動作確認 |
| 2 | [02-getting-started.md](02-getting-started.md) | 最初のプロジェクトを作る（チュートリアル） |
| 3 | [03-commands.md](03-commands.md) | コマンドリファレンス |
| 4 | [04-dev-yml.md](04-dev-yml.md) | `dev.yml` リファレンス |
| 5 | [05-provisioning.md](05-provisioning.md) | 環境構築手順の書き方 |
| 6 | [06-recipes.md](06-recipes.md) | 用途別の構成例 |
| 7 | [07-ai-agents.md](07-ai-agents.md) | AIコーディングツールからの利用 |

うまく動かないときは [トラブルシューティング](../../troubleshooting.ja.md) を参照。

設計の意図や「なぜそうなっているか」は [仕様書](../../spec/README.md) にある。

## 用語

| 用語 | 意味 |
| --- | --- |
| `idev` | コマンド名 |
| `.incus-dev/` | プロジェクト側の設定ディレクトリ |
| `dev.yml` | 開発環境定義ファイル（`.incus-dev/dev.yml`） |
| workspace | コンテナへマウントされるプロジェクトのworking tree |
| instance | idevが作るIncusコンテナ。既定の名前は `dev-<プロジェクト名>-<チェックアウトのハッシュ>`（`project.scope`） |
| provision | コンテナ内部を構成する手順。`dev.yml` に宣言する |
| bootstrap | provisionを動かすための最小限の準備 |
