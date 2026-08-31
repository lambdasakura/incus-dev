# incus-devkit

Incusを利用して、プロジェクト単位の開発環境を再現可能な形で構築・管理するCLIツール `idev`。

```bash
git clone <repository>
cd <repository>

idev up
idev shell
```

## 設計方針

devkitは以下に特化する。

- Incus instanceのライフサイクル管理
- workspace（プロジェクトのworking tree）のマウント
- コンテナのbootstrap
- `.incus-dev/` に宣言された手順の実行

**devkitは環境固有の内容を持たない。**
Ansible Role・Incus Profile・言語ランタイムの導入手順は同梱せず、
すべてプロジェクトの `.incus-dev/` が所有する（REQ-007）。

## 使い方

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
idev validate      # dev.ymlを検証する（Incusへは変更を加えない）
idev up            # instanceを用意し、bootstrapとprovisionを実行する
idev status        # 状態を表示する（--json でmachine-readable）
idev shell         # コンテナ内でshellを開く
idev shell -- make test
idev provision     # instanceを作り直さずprovisionのみ再実行する
idev rebuild       # 破棄して作り直す
idev destroy       # instanceを削除する（ホスト側のソースは削除しない）
```

詳しい使い方は **[マニュアル](docs/manual/README.md)** を参照。
構成例は [examples/](examples/) にもある。

## 前提

| 対象 | 必要なもの |
| --- | --- |
| ホスト | Incus、`idev` バイナリ |
| ホスト（ansibleステップを使う場合） | `ansible-playbook`、`community.general` collection |
| コンテナ | なし（SSH Serverは不要） |

ホスト側の追加設定は不要。既定（`workspace.idmap: auto`）では、
利用可能ならホストの実行ユーザーをコンテナのrootへ対応付け（`raw.idmap`）、
利用できなければidmapped mount（`shift`）へ退避する。

コンテナ内で作られたファイルをホスト側でも自分の所有にしたい場合は、
`/etc/subuid`・`/etc/subgid` へ以下を追加する（incusの再起動は不要）。
（一般的なIncusセットアップ手順に含まれる `root:1000000:1000000000` とは
別に必要になる。）

```text
root:<uid>:1
root:<gid>:1
```

## ビルド

```bash
make build     # ./bin/idev
make install   # $GOBIN へインストール
```

## 開発

```bash
make check              # gofmt / go vet / go test（Incus不要）
make test-integration   # Incus実機に対する統合テスト
```

開発方針は [CLAUDE.md](CLAUDE.md)、仕様は [docs/spec/](docs/spec/README.md) を参照。

## ドキュメント

| | |
| --- | --- |
| [マニュアル](docs/manual/README.md) | 使い方。導入、チュートリアル、リファレンス、構成例 |
| [トラブルシューティング](docs/troubleshooting.md) | ホスト環境に起因する問題への対処 |
| [仕様書](docs/spec/README.md) | 設計仕様 |

## 困ったときは

ホスト環境に起因する典型的な問題（Dockerとのネットワーク競合、workspaceの
所有者、Profile不足など）は [docs/troubleshooting.md](docs/troubleshooting.md) を参照。

## 実装状況

MVP（仕様 [09-roadmap.md](docs/spec/09-roadmap.md)）は実装済み。

| 機能 | 状態 |
| --- | --- |
| `validate` / `up` / `status` / `shell` / `provision` / `rebuild` / `destroy` | 実装済み |
| run / ansible ステップ、bootstrap（既定・上書き・無効化） | 実装済み |
| instance config / devices の素通し、workspace mount | 実装済み |
| idmap（`auto` / `raw` / `shift` / `none`） | 実装済み |
| `status --json` | 実装済み |
| `provision --step` / `--from` / `--list`（部分実行） | 実装済み |
| `--incus-remote` / `--incus-project` | 実装済み（remoteはworkspaceのマウントが成立しないため未検証） |
| `instance.type: virtual-machine` | Incusへ素通しするが未検証 |
| `up --dry-run` | 実装済み |
| `validate --check-host` | 未実装 |
| snapshot / persistent volume / secrets | 未実装 |
