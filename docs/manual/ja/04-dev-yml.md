# 4. `dev.yml` リファレンス

ファイルの位置は `.incus-dev/dev.yml` で固定である。

*[English version](../04-dev-yml.md)*

## 4.1 全体像

```yaml
schema: 1                          # 必須

runtime:
  version: "1.0"                   # 任意。要求するidevの互換バージョン

project:
  name: my-project                 # 必須。instance名の元になる
  scope: name                      # 任意。name（既定）| path | branch

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

volumes:                           # 任意。作り直しても残るデータ領域
  cache:
    path: /home/dev/.cache
    size: 10GiB

secrets:                           # 任意。ホストから注入する秘密情報
  API_TOKEN:
    env: HOST_TOKEN

shell:                             # 任意。idev shell / idev exec の既定
  user: developer
  command: /bin/bash
  cwd: /workspace

incus:                             # 任意
  project: development

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

instance名 `dev-<name>` の元になる。英数字で始まり、以降は
英数字・ドット・ハイフン・アンダースコアが使える（55文字まで）。
instance名としては英数字とハイフンへ正規化される。

同じマシンで複数のプロジェクトを扱う場合、名前が衝突しないようにする。

### `scope`

同じプロジェクトを複数の場所に clone する、あるいはブランチごとに
環境を分けたい場合、instance名の区別の仕方を選べる。

```yaml
project:
  name: my-project
  scope: path        # name（既定） | path | branch
```

| 値 | instance名 | 用途 |
| --- | --- | --- |
| `name`（既定） | `dev-my-project` | 1つの環境を共有する |
| `path` | `dev-my-project-cb958c73` | チェックアウト先ごとに分ける |
| `branch` | `dev-my-project-feature-x` | ブランチごとに分ける（Gitが必要） |

既定を変えると既存の環境が別物になるため、明示した場合のみ名前が変わる。

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

### `type` は無い

instanceは常にコンテナである。`type:` と書くと未知のキーとして
`idev validate` が失敗する。

virtual-machine では workspace を bind mount できず、`raw.idmap` や
disk の `shift` もコンテナ固有の仕組みである。workspaceの共有方式を
別に設計する必要があり、対応する予定は無い。

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

`limits.cpu` / `limits.memory` はコンテナでも有効で、コンテナ内から見える
CPU数（`nproc`）とメモリ量（`/proc/meminfo`）にも反映される。
`make -j$(nproc)` のような並列度の自動判定を、ホスト全体ではなく
この環境の割り当てに合わせられる。

`limits.*` の変更は実行中のコンテナにもそのまま反映されるため、
再起動は要らない。

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

ホストのディレクトリをマウントする `disk` には、workspaceと同じ
uid/gid対応付けが自動的に適用される（4.6の `idmap` を参照）。
`shift` を自分で書く必要はない。最適な値はホストに依存するため、
むしろ書かないほうが可搬性が高い。

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

ここで決まった方式は、`instance.devices` でマウントした
ホストのディレクトリにも同じように適用される。

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

## 4.10 volumes

instanceを作り直しても残したいデータを置く。

```yaml
volumes:
  cache:
    path: /home/dev/.cache   # コンテナ内のマウント先
    size: 10GiB              # 任意
    pool: default            # 任意
```

`idev rebuild` しても中身は残る。`idev destroy` でも残るため、
消したい場合は `idev destroy --volumes` を使う。

ビルドキャッシュやデータベースの実体など、
「環境は作り直したいが消したくないもの」に向く。

---

## 4.11 secrets

`dev.yml` はGitにコミットされる前提なので、**値そのものは書かない**。
ホスト側から注入する。

```yaml
secrets:
  API_TOKEN:
    env: HOST_TOKEN          # ホストの環境変数から
  DEPLOY_KEY:
    file: ~/.config/key      # ホストのファイルから
  OPTIONAL_ONE:
    env: MAYBE
    optional: true           # 無くてもよい
```

- `run` ステップには環境変数として渡る
- `ansible` ステップには `--extra-vars` として渡る（0600の一時ファイル）
- 取得できないものがあると、**instanceに触れる前に** どれが足りないか表示して止まる
- ログやエラーの表示では値がマスクされる

```console
$ idev up
[idev] error: cannot resolve secret(s):
  API_TOKEN (environment variable HOST_TOKEN): not set
```

ただし **ステップ自身が出力した内容まではマスクできない**。
`echo $API_TOKEN` のような書き方は避けること。

---

## 4.12 `shell`

`idev shell` / `idev exec` の既定を指定する。

```yaml
shell:
  user: developer      # 実行ユーザー。省略時はinstanceの既定（root）
  command: /bin/bash   # 起動するシェル。既定 /bin/sh
  cwd: /workspace/src  # 作業ディレクトリ。既定は workspace.target
```

`user` に数値uidを指定した場合はそのままIncusへ渡す。ユーザー名の場合は
コンテナ内で `su` を使って切り替える（Incusのexecはuidしか受け付けないため）。
指定したユーザーはコンテナ内に存在している必要があるので、
provisionで作成しておくこと。

---

## 4.13 `incus`

操作対象のIncus projectを指定する。

```yaml
incus:
  project: development
```

CLIの `--incus-project` を指定した場合はそちらが優先される。
どちらも無ければ `default` を使う。

指定したprojectはIncus側に存在している必要がある（`idev` は作成しない）。
