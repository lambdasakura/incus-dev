# 1. 導入

## 1.1 前提

| 対象 | 必要なもの |
| --- | --- |
| ホスト | Incus（動作確認は6.0系）、`idev` バイナリ |
| ホスト（ansibleステップを使う場合のみ） | `ansible-playbook`、`community.general` collection |
| コンテナ | なし。SSH Serverは導入しない |

`idev` は単一の静的バイナリで、実行時に言語ランタイムを必要としない。

Ansibleを使うかどうかは **プロジェクトごとの選択** であり、
シェルスクリプトだけで構成するなら不要。

## 1.2 Incusの準備

Incusが利用できることを確認する。

```bash
incus info | head -3
incus profile list
```

`default` profile が存在していれば、そのまま使い始められる。

## 1.3 idevの導入

```bash
git clone <incus-devkit-repository>
cd incus-devkit

make build          # ./bin/idev を生成
sudo install -m 0755 bin/idev /usr/local/bin/idev
```

Goツールチェインがある場合は以下でもよい。

```bash
make install        # $GOBIN へインストール
```

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
`/etc/subuid` と `/etc/subgid` へ以下を追加してIncusを再起動する。

```text
root:1000:1        # 1000 は自分のuid（id -u）
```

```bash
id -u; id -g                        # 自分のuid/gidを確認
grep '^root:' /etc/subuid /etc/subgid
```

未設定でも `idev` は動作する。その場合はidmapped mountへ自動的に退避し、
コンテナが作ったファイルはホスト側でrootの所有になる。

詳細は [04-dev-yml.md](04-dev-yml.md) の `workspace.idmap` と
[トラブルシューティング](../troubleshooting.md#2-workspaceの所有者がおかしい--書き込めない) を参照。

## 1.6 Dockerを併用している場合

ホストにDockerが入っていると、コンテナから外部へ通信できなくなることがある。
Dockerがカーネルの転送ポリシーをDROPに設定するためで、Incus側の設定では解決しない。

`idev up` のprovisionでパッケージ導入が失敗する場合は
[トラブルシューティング 1](../troubleshooting.md#1-コンテナから外部へ通信できない) を参照。

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
