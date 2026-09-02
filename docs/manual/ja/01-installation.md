# 1. 導入

*[English version](../01-installation.md)*

## 1.1 前提

| 対象 | 必要なもの |
| --- | --- |
| ホスト | Linux、Incus（動作確認は6.0系）、`idev` バイナリ |
| ホスト（ansibleステップを使う場合のみ） | `ansible-playbook`、`community.general` collection |
| コンテナ | なし。SSH Serverは導入しない |

`idev` は単一の静的バイナリで、実行時に言語ランタイムを必要としない。

Incusの操作はGoのclient libraryからAPIで行うため、`incus` コマンドは要らない。
ただしremoteやimageの解決には `incus` コマンドと同じ設定
（`~/.config/incus/config.yml`）を読むため、
`incus` コマンドで接続できるIncusはそのまま `idev` からも使える。

Ansibleを使うかどうかは **プロジェクトごとの選択** であり、
シェルスクリプトだけで構成するなら不要。

## 1.2 Incusの準備

Incusが利用できることを確認する。`incus` コマンドがあるなら、それが手軽である。

```bash
incus info | head -3
incus profile list
```

`default` profile が存在していれば、そのまま使い始められる。

`incus` コマンドを入れていない場合は、`idev` の導入後（1.3）に
プロジェクトのディレクトリで確認できる。

```bash
idev status
```

Incusへ接続できていれば、instanceがまだ無くても状態が表示される。
接続できない場合はその旨のエラーになる
（[トラブルシューティング](../../troubleshooting.ja.md#6-incus-apiとの通信で失敗する) 参照）。

## 1.3 idevの導入

### edgeビルドから導入する（推奨）

バージョンを打ったリリースはまだ無い。`edge` は現在の `main` であり、
`main` が動くたびに作り直される。Linux の amd64・arm64 向けアーカイブと
`checksums.txt` が付く。URLは変わらないので、同じコマンドで更新できる。

```bash
curl -LO https://github.com/lambdasakura/incus-dev/releases/download/edge/incus-dev_edge_linux_amd64.tar.gz
curl -LO https://github.com/lambdasakura/incus-dev/releases/download/edge/checksums.txt

sha256sum --check --ignore-missing checksums.txt
tar -xzf incus-dev_edge_linux_amd64.tar.gz
sudo install -m 0755 incus-dev_edge_linux_amd64/idev /usr/local/bin/idev

idev --version        # edge-<commit>。ビルド元のコミット
```

転がり続けるビルドなので、安定性は保証しない。`dev.yml` の形式が
変わることもある。変わった場合は `idev validate` が知らせる。

バージョンを打ったリリースは同じ場所に、ファイル名がバージョン入りの形で並ぶ。
`idev --version` もそれを返すようになる。

**idev はLinux専用である。** 動作しているマシンのIncusを操作するものであり、
Incusのclient libraryはLinux以外でローカル接続を拒否する。
そのため macOS / Windows ではIncusに触れるコマンドが1つも動かない。
これらのプラットフォーム向けのバイナリは配布しない。

### ソースから導入する

```bash
git clone https://github.com/lambdasakura/incus-dev.git
cd incus-dev

make build          # ./bin/idev を生成
sudo install -m 0755 bin/idev /usr/local/bin/idev
```

Goツールチェインがある場合は以下でもよい。

```bash
make install        # $GOBIN へインストール
```

checkoutせずに導入することもできる。

```bash
go install github.com/lambdasakura/incus-dev/cmd/idev@latest
```

この方法で入れたバイナリは `idev --version` が `dev` を返す。
バージョンはリリース時に埋め込まれるためである。

確認：

```bash
idev --version
idev --help
```

## 1.4 Ansibleを使う場合

```bash
ansible-galaxy collection install community.general

# 接続プラグインが利用可能か確認する
ansible-doc -t connection community.general.incus
```

idev はSSHを使わず、このconnection pluginでコンテナへ接続する。
対象コンテナへSSH Serverを導入する必要はない。

## 1.5 workspaceの所有者について（任意だが推奨）

コンテナ内で作ったファイルを、ホスト側でも自分の所有にしたい場合は
`/etc/subuid` と `/etc/subgid` へ以下を追加する。

```text
root:1000:1        # 1000 は自分のuid（id -u）
```

```bash
id -u; id -g                        # 自分のuid/gidを確認
grep '^root:' /etc/subuid /etc/subgid
```

追加後、Incusの再起動は不要（コンテナ起動時に読まれる）。

未設定でも `idev` は動作する。その場合はidmapped mountへ自動的に退避し、
コンテナが作ったファイルはホスト側でrootの所有になる。

詳細は [04-dev-yml.md](04-dev-yml.md) の `workspace.idmap` と
[トラブルシューティング](../../troubleshooting.ja.md#2-workspaceの所有者がおかしい--書き込めない) を参照。

## 1.6 Dockerを併用している場合

ホストにDockerが入っていると、コンテナから外部へ通信できなくなることがある。
Dockerがカーネルの転送ポリシーをDROPに設定するためで、Incus側の設定では解決しない。

`idev up` のprovisionでパッケージ導入が失敗する場合は
[トラブルシューティング 1](../../troubleshooting.ja.md#1-コンテナから外部へ通信できない) を参照。

## 1.7 動作確認

任意のディレクトリで最小のプロジェクトを作って確認する。

```bash
mkdir -p /tmp/idev-check/.incus-dev && cd /tmp/idev-check
cat > .incus-dev/dev.yml <<'YAML'
schema: 1
project:
  name: idev-check
instance:
  image: images:alpine/3.21
YAML

idev validate
idev up
idev shell -- cat /etc/os-release
idev destroy --force
```

`idev validate` はIncusへ一切変更を加えないため、
まずこれが通ることを確認するとよい。
