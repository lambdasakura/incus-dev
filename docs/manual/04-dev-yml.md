# 4. `dev.yml` リファレンス

ファイルの位置は `.incus-dev/dev.yml` で固定である。

## 4.1 全体像

```yaml
schema: 1                          # 必須

runtime:
  version: "1.0"                   # 任意。要求するidevの互換バージョン

project:
  name: my-project                 # 必須。instance名の元になる

instance:
  image: images:ubuntu/24.04       # 必須
  type: container                  # 任意（既定 container）
  profiles:                        # 任意（既定 [default]）
    - default
  config:                          # 任意。Incusのinstance configへ素通し
    limits.cpu: "8"
    limits.memory: 16GiB
  devices:                         # 任意。Incusのdeviceへ素通し
    gpu0:
      type: gpu

workspace:                         # 任意
  source: .
  target: /workspace
  idmap: auto

bootstrap:                         # 任意
  - run: command -v python3 || apk add python3

provision:                         # 任意。上から順に実行
  - name: setup
    run: sh /workspace/.incus-dev/scripts/setup.sh
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
```

必須は `schema` `project.name` `instance.image` のみ。

---

## 4.2 `schema`

```yaml
schema: 1
```

設定フォーマットのバージョン。現在は `1` のみ。

---

## 4.3 `runtime`

```yaml
runtime:
  version: "1.0"
```

このプロジェクトが要求する `idev` の互換バージョン。
満たさない `idev` で実行するとエラーになる。

`MAJOR`、`MAJOR.MINOR`、`MAJOR.MINOR.PATCH` の形式を受け付ける。

---

## 4.4 `project`

```yaml
project:
  name: my-project
```

instance名 `dev-<name>` の元になる。英数字・ドット・ハイフン・アンダースコアが使える
（instance名としては英数字とハイフンへ正規化される）。

同じマシンで複数のプロジェクトを扱う場合、名前が衝突しないようにする。

---

## 4.5 `instance`

### `image`

Incusのimage参照。

```yaml
image: images:ubuntu/24.04
image: images:alpine/3.21
image: images:debian/12
```

利用可能なものは `incus image list images: <keyword>` で確認できる。

idev は特定のOSを前提としない。imageと構成手順の整合はプロジェクトの責任である。

### `type`

`container`（既定）または `virtual-machine`。現時点では `container` のみ検証されている。

### `profiles`

**ホストに既に存在するProfileを名前で参照するだけ** のフィールド。

```yaml
profiles:
  - default
```

- idev はProfileを同梱も作成もしない
- 指定したProfileが無ければ `idev up` は明示的に失敗する
- 省略時は `[default]`

Profileに依存したくない場合は空リストにできるが、
その場合はroot diskとネットワークも自分で宣言する必要がある。

```yaml
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

storage pool名やnetwork名はホスト依存になるため、
可搬性を優先するなら `default` profile を参照する方がよい。

### `config`

Incusのinstance configへそのまま渡される。idev は内容を解釈しない。

```yaml
config:
  limits.cpu: "8"
  limits.memory: 16GiB
  security.nesting: "true"        # コンテナ内でDocker等を動かす場合
  environment.TZ: Asia/Tokyo
```

数値や真偽値で書いても文字列へ変換される（`limits.cpu: 8` でもよい）。

`user.incus-devkit.*` は idev の管理用に予約されているため使用できない。

### `devices`

Incusのdeviceへそのまま渡される。

```yaml
devices:
  gpu0:
    type: gpu

  dataset:
    type: disk
    source: /srv/dataset          # ホスト側の絶対パス
    path: /data

  assets:
    type: disk
    source: ./assets              # 相対パスはproject rootが基準
    path: /assets

  http:
    type: proxy
    listen: tcp:127.0.0.1:8080
    connect: tcp:127.0.0.1:8080
```

`workspace` という名前は予約されている（4.6を参照）。

---

## 4.6 `workspace`

プロジェクトのworking treeをコンテナへマウントする設定。省略時は既定値が使われる。

```yaml
workspace:
  source: .            # project rootが基準。既定 "."
  target: /workspace   # コンテナ内のパス。既定 /workspace
  idmap: auto          # 既定 auto
```

コピーではなくbind mountなので、ホスト側の編集が即座に反映される。

### `idmap`

非特権コンテナでホストのディレクトリを共有するためのuid/gid対応付け方式。

| 値 | ホスト側の追加設定 | コンテナが作ったファイルのホスト側所有者 |
| --- | --- | --- |
| `auto`（既定） | 不要 | `raw` が使えれば自分、でなければroot |
| `raw` | 必要 | 自分 |
| `shift` | 不要 | root |
| `none` | 不要 | （書き込み不可） |

`raw` を使うには `/etc/subuid` と `/etc/subgid` に `root:<uid>:1` が必要である
（[01-installation.md](01-installation.md) 1.5）。

`auto` は `raw` が使えるならそれを、使えなければ `shift` へ退避し、警告を表示する。
どちらでもworkspaceは読み書きできる。違いは生成物の所有者だけである。

チーム内でホスト設定を揃えられない場合は `auto` のままにしておくとよい。

---

## 4.7 `bootstrap`

provisionを動かすための最小限の準備。省略できる。

```yaml
bootstrap:
  - run: command -v python3 >/dev/null 2>&1 || dnf install -y python3
```

- 省略時、`provision` に `ansible` ステップがあれば、
  Debian系を前提とした既定bootstrap（python3の導入）が実行される
- 記述した場合、既定bootstrapは実行されない
- `bootstrap: []` で無効化できる
- `run` のみ使用できる（`ansible` は使えない）

詳細は [05-provisioning.md](05-provisioning.md) を参照。

---

## 4.8 `provision`

コンテナ内部の構成手順。順序付きの配列で、上から順に実行される。

各ステップは `run` か `ansible` のどちらか一方を持つ。

```yaml
provision:
  - name: prepare
    run: apt-get update

  - name: main
    ansible:
      playbook: .incus-dev/ansible/site.yml
```

書き方は [05-provisioning.md](05-provisioning.md) を参照。

---

## 4.9 パスの解決規則

| 対象 | 基準 |
| --- | --- |
| `workspace.source` | project root |
| `devices.*.source`（相対パスの場合） | project root |
| `ansible.playbook` / `vars` / `inventory` | project root |
| `run` 内に書くパス | コンテナ内の絶対パス |

`run` ステップからプロジェクトのファイルを参照する場合は、
workspace越しのパスになる。

```yaml
provision:
  - run: sh /workspace/.incus-dev/scripts/setup.sh
```

---

## 4.10 書いてはいけないもの

以下を `dev.yml` へ直接書かない。Gitにコミットされる前提のファイルである。

- APIキー、アクセストークン、パスワード、秘密鍵

必要な場合はホスト側の環境変数やファイルから注入する。
（idev による注入機構は現時点では提供していない。）
