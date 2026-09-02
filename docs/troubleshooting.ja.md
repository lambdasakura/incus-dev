# トラブルシューティング

ホスト側の環境に起因して `idev` が失敗する典型的なケースと対処。

*[English version](troubleshooting.md)*

いずれも `dev.yml` の書き方ではなく **ホスト側の前提** に関する問題である。

| 症状 | 節 |
| --- | --- |
| provisionステップ中の `apt-get` / `apk` が失敗する、コンテナから名前解決やダウンロードができない | [1](#1-コンテナから外部へ通信できない) |
| workspaceへ書き込めない、生成物がホスト側でrootの所有になる | [2](#2-workspaceの所有者がおかしい--書き込めない) |
| `incus profile(s) not found on this host` | [3](#3-profileが見つからない) |
| `is not managed by devkit` | [4](#4-instanceがdevkit管理外と言われる) |
| ansibleステップが失敗する | [5](#5-ansibleステップが失敗する) |
| Incusへ接続できない、API呼び出しが失敗する | [6](#6-incus-apiとの通信で失敗する) |

---

## 1. コンテナから外部へ通信できない

### 症状

`idev up` のprovisionステップでパッケージ導入が失敗する。

```text
WARNING: fetching https://dl-cdn.alpinelinux.org/alpine/v3.21/main: temporary error (try again later)
```

コンテナ内でIPアドレスは割り当てられており、DNSも引けるのに外へ出られない。

### 原因（1）: 起動直後でネットワークがまだ使えない

instanceが起動してコマンドを実行できるようになった時点では、
まだIPv4が割り当てられておらずデフォルトルートも入っていない。

`idev` はIPv4の割り当てを待ってからprovisionを開始するため、
通常この問題は起きない。`idev up` が
`network address not assigned` の警告を出していた場合は、
ネットワーク構成側の問題を疑う。

### 原因（2）: Dockerとの競合

**ホストにDockerが入っている場合、DockerとIncusのファイアウォール設定が競合する。**

Incusは自前のnftablesテーブルで自分のブリッジを許可する。

```text
table inet incus / chain fwd.incusbr0 (policy accept)
  ip version 4 iifname "incusbr0" accept
  ip version 4 oifname "incusbr0" accept
```

一方Dockerは `ip filter` テーブルの **FORWARDチェーンのポリシーをDROPに設定する**。

netfilterは同一フック（forward）に登録された全チェーンを評価し、
**どこか一つでもDROPすればその時点で破棄する**。
Incus側のacceptはDocker側のDROPを覆せないため、
incusbr0を経由する転送だけが落ちる。

### 確認

```bash
# FORWARDのポリシーがDROPになっているか
sudo iptables -S FORWARD

# incusbr0向けの許可がどこにも無いことを確認する
sudo iptables -L DOCKER-CT -v -n       # Docker自身のブリッジ用の戻り許可しか無い
sudo iptables -L DOCKER-USER -v -n     # ここが空なら未対処
```

### 対処

`DOCKER-USER` チェーンはFORWARDの先頭で評価されるため、ここで許可する。

```bash
sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT
sudo iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
```

**2行とも必要である。**

- 1行目: コンテナから外への送信
- 2行目: 外からコンテナへの戻り。Dockerの戻り許可（`DOCKER-CT`）は
  Docker自身のブリッジ (`docker0`, `br-*`) にしか適用されないため、
  これが無いと「送信はできるが応答が返らない」状態になる

適用後、実際に通っているかはパケットカウンタで確認できる。

```bash
sudo iptables -L DOCKER-USER -v -n
```

### 永続化（重要）

`iptables -I` はメモリ上のルールであり、**再起動で失われる**。
再起動後に同じ症状が再発する場合はこれが原因である。

`netfilter-persistent` などが入っていない環境では、
systemd unitで再適用するのが確実で影響範囲も小さい。

```ini
# /etc/systemd/system/incus-docker-forward.service
[Unit]
Description=Allow incusbr0 traffic through Docker's FORWARD chain
After=docker.service
Requires=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'iptables -C DOCKER-USER -i incusbr0 -j ACCEPT 2>/dev/null || iptables -I DOCKER-USER -i incusbr0 -j ACCEPT'
ExecStart=/bin/sh -c 'iptables -C DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT'

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now incus-docker-forward.service
```

代替として、Docker 27以降であれば以下でDockerがFORWARDポリシーを
DROPにしなくなる。設定ファイルなので永続するが、効果が全ブリッジに及ぶ。

```json
// /etc/docker/daemon.json
{ "ip-forward-no-drop": true }
```

### ufwを使っている場合

ufwが **有効** な場合は、上記に加えて以下も必要になる。

```bash
sudo ufw allow in on incusbr0          # コンテナ → ホスト（DHCP / DNS）
sudo ufw route allow in on incusbr0    # 転送（送信）
sudo ufw route allow out on incusbr0   # 転送（戻り）
```

ufwが **無効**（`ufw status` が `inactive`）の場合、これらは
`/etc/ufw/user.rules` に保存されるだけで一切適用されない。
ufwを有効化した時点で必要になるため、先に入れておくこと自体は無害である。

なおufwを有効化しても `DOCKER-USER` の2行は依然として必要である。
Dockerのチェーンがufwのチェーンより先に評価されるため。

---

## 2. workspaceの所有者がおかしい / 書き込めない

### 症状

- コンテナ内から `/workspace` や、`instance.devices` でマウントした
  ホストのディレクトリへ書き込めない
- コンテナ内で作ったファイルがホスト側でrootの所有になり、sudoなしで消せない
- `idev up` が `workspace idmap (raw.idmap) is not permitted on this host` で停止する

### 原因と対処

非特権コンテナでホストのディレクトリを共有するには、uid/gidの対応付けが要る。
`workspace.idmap` で方式を選べる（[マニュアル 4.6](manual/ja/04-dev-yml.md#46-workspace)）。

| 値 | ホスト側の追加設定 | コンテナが作ったファイルのホスト側所有者 |
| --- | --- | --- |
| `auto`（既定） | 不要 | 環境依存（`raw` が使えれば実行ユーザー、でなければroot） |
| `raw` | 必要 | 実行ユーザー |
| `shift` | 不要 | root |
| `none` | 不要 | （書き込み不可） |

`raw`（最も望ましい挙動）を使うには、Incus daemon（root）が
実行ユーザーのuid/gidを対応付ける許可が必要になる。

```text
/etc/subuid: root:1000:1
/etc/subgid: root:1000:1
```

**一般的なIncusのセットアップ手順に含まれる `root:1000000:1000000000` とは別物である。**
そちらは「コンテナ内のuidをホストの1000000以降へ退避する」ための範囲であり、
「ホストのuid 1000をコンテナへ持ち込む」許可は含まない。

不足していると、コンテナ起動時に以下で失敗する。

```text
newuidmap: uid range [0-1) -> [1000-1001) not allowed
```

追加後、incusの再起動は不要（`newuidmap` はコンテナ起動時に読む）。

```bash
grep '^root:' /etc/subuid /etc/subgid
idev up
incus config get dev-<project> raw.idmap    # "uid <uid> 0 / gid <gid> 0" が出れば適用済み
```

追加しない場合、既定の `auto` は `shift`（idmapped mount）へ退避して動作を継続する。
この場合もworkspaceは読み書きできるが、コンテナが作ったファイルはホスト側でrootの所有になる。

---

## 3. Profileが見つからない

```text
incus profile(s) not found on this host: gpu-nvidia
devkit does not create profiles; create them or remove them from instance.profiles
```

`idev` はIncus Profileを同梱も作成もしない。
`instance.profiles` は **ホストに既に存在するProfileの名前参照** である。

対処のいずれか。

- ホスト側でProfileを作成する
- `instance.profiles` から外し、必要な設定を `instance.config` / `instance.devices` に直接書く

`profiles: []`（Profileを一切使わない）とする場合、
root diskとネットワークもProfile由来であるため自分で宣言する必要がある。

```yaml
instance:
  profiles: []
  devices:
    root:
      type: disk
      pool: default
      path: /
    eth0:
      type: nic
      network: incusbr0
```

storage pool名やnetwork名はホストに依存するため、
可搬性を優先するなら `default` profile を参照する方がよい。

---

## 4. instanceがdevkit管理外と言われる

```text
instance dev-example exists but is not managed by devkit for project "example"
```

`idev` は自分が作成したinstanceに印を付けており、印が無いinstanceは
誤って壊さないよう触れない。

```bash
incus config get dev-<project> user.incus-devkit.project
```

同名のinstanceを手動で作っていた場合は、削除するか
`project.name` を変えてinstance名をずらす。

---

## 5. ansibleステップが失敗する

### ホスト側

`ansible-playbook` と `community.general` collection が必要である。
`idev` は同梱しない。

```bash
ansible-galaxy collection install community.general
ansible-doc -t connection community.general.incus   # 導入確認
```

### コンテナ側

Ansible Moduleの実行にはコンテナ内のPythonが必要である。
`bootstrap` を省略していて `provision` に ansible ステップがある場合、
`idev` はDebian系を前提とした既定bootstrapでPythonの導入を試みる。

Debian系以外のイメージではこれが失敗するため、`bootstrap` を明示する。

```yaml
bootstrap:
  - run: command -v python3 >/dev/null 2>&1 || dnf install -y python3
```

パッケージ導入を伴うため、この経路は **コンテナからの外部通信** に依存する。
失敗する場合はまず [1](#1-コンテナから外部へ通信できない) を確認すること。

---

## 6. Incus APIとの通信で失敗する

`idev` はIncusのGo client libraryからAPIを直接呼ぶ。
接続先や証明書の解決には `incus` コマンドと同じ設定
（`~/.config/incus/config.yml`）を読む。

### まず確認する

```bash
incus info | head -3        # 同じ設定で接続できるか
```

`incus` コマンドでも接続できない場合は、`idev` ではなくIncus側の問題である。

### 設定を確認する

`incus` コマンドが入っている環境では、同じ設定で接続できるかを比べられる。

```bash
incus remote list           # remote の一覧と既定
incus project list          # project の一覧
```

idev が操作するのは常に `local` remote であり、`incus remote switch` が
設定した既定には従わない。`--incus-project` で指定した名前が
この一覧に存在するかを確認する。
