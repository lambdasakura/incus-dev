# incus-dev

Incusを利用して、プロジェクト単位の開発環境を再現可能な形で構築・管理するCLIツール `idev`。

*[English version](README.md)*

[![CI](https://github.com/lambdasakura/incus-dev/actions/workflows/ci.yml/badge.svg)](https://github.com/lambdasakura/incus-dev/actions/workflows/ci.yml)

## クイックスタート

必要なのは [Incus](https://linuxcontainers.org/incus/docs/main/installing/) を入れた
Linuxホストだけ。`idev` は単一の静的バイナリで、実行時の依存を持たない。

### 1. idevを導入する

**安定版はまだ無い。** 公開しているのは
[`edge`](../../releases/tag/edge) で、`main` が動くたびに作り直される
現在の `main` である。安定性も、`dev.yml` の形式が今のままであることも保証しない。

```bash
curl -LO https://github.com/lambdasakura/incus-dev/releases/download/edge/incus-dev_edge_linux_amd64.tar.gz
curl -LO https://github.com/lambdasakura/incus-dev/releases/download/edge/checksums.txt
sha256sum --check --ignore-missing checksums.txt

tar -xzf incus-dev_edge_linux_amd64.tar.gz
sudo install -m 0755 incus-dev_edge_linux_amd64/idev /usr/local/bin/idev

idev --version        # edge-<commit>。ビルド元のコミット
```

URLは変わらないので、同じコマンドを再実行すれば更新できる。
`arm64` も同じ場所にある。

`go install` を含む他の導入方法は[後述](#導入)。

### 2. 環境を宣言する

環境を用意したいプロジェクトで以下を行う。

```bash
cd ~/your-project
mkdir -p .incus-dev

cat > .incus-dev/dev.yml <<'YAML'
schema: 1

project:
  name: your-project

instance:
  image: images:ubuntu/24.04

provision:
  - name: tools
    run: apt-get update && apt-get install -y build-essential git
YAML
```

### 3. 立ち上げる

```bash
idev validate   # dev.ymlの検査。Incusへは一切変更を加えない
idev up         # instanceを作り、bootstrapしてprovisionを実行する
idev shell      # コンテナ内のシェル。プロジェクトは /workspace にある
```

`.incus-dev/` はコードと一緒にコミットする。
以後、このプロジェクトをcloneした人は `idev up` だけで同じ環境を再現できる。

次は[チュートリアル](docs/manual/ja/02-getting-started.md)、
または構成例の [examples/](examples/README.ja.md) へ。

## 設計方針

`idev` は以下に特化する。

- Incus instanceのライフサイクル管理
- workspace（プロジェクトのworking tree）のマウント
- コンテナのbootstrap
- `.incus-dev/` に宣言された手順の実行

**`idev` は環境固有の内容を持たない。**
Ansible Role・Incus Profile・言語ランタイムの導入手順は同梱しない。
環境を再現するために必要なものは、すべてプロジェクトの `.incus-dev/` に置く。

同梱すると、使う側にとって不要なものまで入り込み、
「この環境が何でできているか」が2箇所に分かれてしまうためである。

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
idev ip            # アドレスを表示する: ssh user@$(idev ip)
idev shell         # コンテナ内でshellを開く
idev exec -- make test   # コンテナ内でコマンドを実行する（端末は割り当てない）
idev provision     # instanceを作り直さずprovisionのみ再実行する
idev snapshot create before-upgrade   # 退避しておく（restore で戻せる）
idev rebuild       # 破棄して作り直す
idev destroy       # instanceを削除する（ホスト側のソースは削除しない）
```

コンテナ内コマンドの終了コードはそのまま返る（`idev exec -- make test || exit 1`）。
スクリプトやCIからは、端末の有無で挙動が変わらない `idev exec` を使う。

詳しい使い方は **[マニュアル](docs/manual/ja/README.md)** を参照。
構成例は [examples/](examples/README.ja.md) にもある。

## 前提

| 対象 | 必要なもの |
| --- | --- |
| ホスト | Linux、Incus、`idev` バイナリ |
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

## 導入

[`edge`](../../releases/tag/edge) に Linux の amd64・arm64 バイナリと、
検証用の `checksums.txt` を添付している。`main` が動くたびに置き換わる。

```bash
sha256sum --check --ignore-missing checksums.txt
tar -xzf incus-dev_edge_linux_amd64.tar.gz
sudo install -m 0755 incus-dev_edge_linux_amd64/idev /usr/local/bin/idev
```

バージョンを打った[リリース](../../releases)はまだ無い。
出したときは同じ場所に、ファイル名がバージョン入りの形で並ぶ。

Goツールチェインがあればダウンロードは要らない。
ただしこの方法で入れたバイナリは `idev --version` が `dev` を返す。
バージョンはビルド時に埋め込まれるためである。

```bash
go install github.com/lambdasakura/incus-dev/cmd/idev@latest
```

checkoutから入れる場合：

```bash
make build     # ./bin/idev
make install   # $GOBIN へインストール
```

## 開発

```bash
make check              # lint + test（Incus不要）
make test-integration   # Incus実機に対する統合テスト
```

`make lint` は golangci-lint を使い、無ければ gofmt / go vet で代替する
（`make tools` で導入できる）。

変更を加える場合は、開発方針を [CLAUDE.md](CLAUDE.md) に、
設計の判断基準を [docs/spec/](docs/spec/README.md) にまとめてある。

## ドキュメント

| | |
| --- | --- |
| [マニュアル](docs/manual/ja/README.md) | 使い方。導入、チュートリアル、リファレンス、構成例 |
| [トラブルシューティング](docs/troubleshooting.ja.md) | ホスト環境に起因する問題への対処 |
| [skills/](skills/) | AIエージェント向けAgent Skill |
| [設計仕様](docs/spec/README.md) | 内部設計。変更を加えるときの判断基準 |

## 困ったときは

ホスト環境に起因する典型的な問題（Dockerとのネットワーク競合、workspaceの
所有者、Profile不足など）は [docs/troubleshooting.ja.md](docs/troubleshooting.ja.md) を参照。

## 実装状況

以下はすべて実装済みである。中核（ライフサイクル、provisioning、
workspaceのマウント、idmap）はIncus実機に対する統合テストで確認している。

- `validate` / `up` / `status` / `shell` / `exec` / `provision` / `rebuild` /
  `destroy` / `snapshot`
- run / ansible / galaxy ステップ、bootstrap（既定・上書き・無効化）
- `provision --step` / `--from` / `--list`（部分実行）
- `up --dry-run` / `up --restart`、`status --json`
- instance config / devices の素通しと削除追従
- workspace mount と idmap（`auto` / `raw` / `shift` / `none`）
- `volumes`（永続ボリューム）、`secrets`（ホストからの注入）
- `shell`（user / command / cwd）、`incus.project`
- `project.scope`（複数checkout / ブランチ別instance）
- Incus API（Go client library）での操作。`incus` コマンドを必要としない

## 対応しないもの

remoteのIncusと仮想マシンは恒久的に対象外である。どちらも workspace の
共有方式を別に設計する必要があり、それは「手元のマシンにプロジェクト単位の
開発環境を作る」というこのツールの目的から外れるためである。

| | |
| --- | --- |
| remoteのIncus | 常にローカルのIncusを操作する。`incus remote switch` の既定にも従わない |
| 仮想マシン | instanceは常にコンテナであり、`instance.type` という設定は無い |
| macOS / Windows | ローカルのIncusを操作するのが `idev` であり、client libraryはLinux以外でローカル接続を拒否する。リリースするのはLinuxバイナリのみ |

## ライセンス

MIT。[LICENSE](LICENSE) を参照。
