# 3. 開発環境定義（dev.yml）

## 3.1 ファイル配置

開発環境に関するすべてのファイルは `.incus-dev/` 配下に置く。

```text
my-project/
├── .incus-dev/
│   ├── dev.yml              # 唯一の必須ファイル
│   │
│   ├── ansible/             # 任意（プロジェクト所有）
│   │   ├── site.yml
│   │   ├── vars.yml
│   │   ├── requirements.yml
│   │   └── roles/
│   │
│   └── scripts/             # 任意（プロジェクト所有）
│       └── prepare.sh
│
├── src/
└── ...
```

`dev.yml` 以外の構成は自由であり、idevは特定のディレクトリ名を要求しない。
`dev.yml` から参照されたパスだけを使用する。

---

## 3.2 基本例

### 3.2.1 最小構成

```yaml
schema: 1

project:
  name: example-project

instance:
  image: images:ubuntu/24.04
```

これだけで、workspaceがマウントされた素のコンテナが起動する。

### 3.2.2 シェルスクリプトで構成する例

```yaml
schema: 1

project:
  name: example-project

instance:
  image: images:ubuntu/24.04
  config:
    limits.cpu: "8"
    limits.memory: 16GiB

provision:
  - name: setup
    run: sh /workspace/.incus-dev/scripts/prepare.sh
```

### 3.2.3 Ansibleで構成する例

```yaml
schema: 1

runtime:
  version: "1.0"

project:
  name: example-project

instance:
  image: images:ubuntu/24.04

  profiles:
    - default

  config:
    limits.cpu: "8"
    limits.memory: 16GiB
    security.nesting: "true"

  devices:
    gpu0:
      type: gpu

workspace:
  source: .
  target: /workspace

provision:
  - name: base packages
    run: |
      apt-get update
      apt-get install -y --no-install-recommends jq cmake ninja-build

  - name: main playbook
    ansible:
      playbook: .incus-dev/ansible/site.yml
      vars: .incus-dev/ansible/vars.yml
```

すべてのフィールドを必須にはしない。

---

## 3.3 schema

```yaml
schema: 1
```

必須。

設定フォーマットのバージョンを示す。

`idev` は未知のschema versionを検出した場合、処理を継続してはならない。

---

## 3.4 runtime

```yaml
runtime:
  version: "1.0"
```

任意。

このプロジェクトが要求する `idev` の互換バージョンを指定する。

`idev` が要求を満たさない場合は明示的なエラーとする。

idevはAnsible RoleやProfileを同梱しないため（REQ-007）、
このバージョンが示すのは **CLIの挙動と設定フォーマットの互換性のみ** である。

provisioning内容のバージョン管理はプロジェクト側のGit履歴が担う。

将来的にはSemVer形式を使用する。

---

## 3.5 project

```yaml
project:
  name: example-project
```

`name` は原則必須。

Incus instance名生成などに利用する（[05-incus.md](05-incus.md) 参照）。

### scope

同一マシンで複数のチェックアウトを扱う場合の、instance名の区別の仕方。

```yaml
project:
  name: my-project
  scope: path        # name（既定） | path | branch
```

| 値 | instance名 | 用途 |
| --- | --- | --- |
| `name`（既定） | `dev-my-project` | 従来どおり |
| `path` | `dev-my-project-cb958c73` | チェックアウト先ごとに分ける |
| `branch` | `dev-my-project-feature-x` | ブランチごとに分ける（Gitが必要） |

`branch` はコミットが無いリポジトリでもブランチ名を解決する。
detached HEADの場合はコミットの短縮ハッシュを使う。

既定を変えると既存の環境が別物になってしまうため、
明示的に指定した場合のみ名前が変わる。

---

## 3.6 instance

```yaml
instance:
  image: images:ubuntu/24.04
  type: container

  profiles:
    - default

  config:
    limits.cpu: "8"
    limits.memory: 16GiB
    security.nesting: "true"

  devices:
    gpu0:
      type: gpu
```

### 3.6.1 image

必須。Incus image referenceを指定する。

```yaml
image: images:ubuntu/24.04
```

idevは特定のOSを前提としない。
image選択と、それに適合するprovisioning手順の整合はプロジェクトの責務とする。

### 3.6.2 instance.type は無い

**instanceは常にコンテナである。`type` という設定は持たない。**

virtual-machine では workspace の bind mount が使えず virtiofs / 9p となり、
`raw.idmap` や disk の `shift` もコンテナ固有の仕組みで意味を持たない。
つまり workspace の共有方式を別に設計しないと成立しないが、対応する予定は無い
（[09-roadmap.md](09-roadmap.md)）。

「渡せるが検証していない」という状態は、動くと期待して書いた利用者が
分かりにくい形で失敗するだけなので、設定項目ごと持たない。
`type` を書いた場合は未知のキーとして validate が失敗する。

image aliasの解決でもコンテナ用のimageのみを要求する
（[05-incus.md](05-incus.md) 5.7.1）。

### 3.6.3 profiles

任意。**ホスト側に既に存在するIncus Profileを名前で参照するだけ** のフィールド。

```yaml
profiles:
  - default
```

- idevはProfileを同梱しない（REQ-007）
- idevはProfileを作成・更新しない
- 指定されたProfileが存在しない場合は明示的に失敗する

省略時は `["default"]` として扱う（Incusの既定挙動と一致させるため）。

明示的に空リストを指定した場合は、Profileを一切適用しない。

```yaml
profiles: []
```

この場合、Incusが必要とする **root disk device も継承されない** ため、
`devices` で明示しなければならない。ネットワークも同様である。

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

`profiles: []` かつ root disk（`type: disk` かつ `path: /`）が
宣言されていない場合、`idev validate` および `idev up` は失敗する。

**推奨：** 環境依存を避けたい場合はProfileに依存せず、
`config` と `devices` にすべて記述する。

### 3.6.4 config

任意。Incus instance configへそのまま渡すkey-value。

```yaml
config:
  limits.cpu: "8"
  limits.memory: 16GiB
  security.nesting: "true"
  environment.TZ: Asia/Tokyo
```

idevはキーの意味を解釈しない。未知のキーを拒否してはならない。

Incusのconfig値は文字列であるため、YAML上のスカラ値（数値・真偽値）は
文字列へ変換して渡す。

```yaml
limits.cpu: 8         # "8" として渡される
```

変換されるのはYAMLが解釈した**後の値**である。YAML 1.1 では `0660` は8進数、
`no` は真偽値として解釈されるため、それぞれ `"432"` `"false"` になる。
元の表記はJSONへの変換時点で失われており、実装側で区別できない。

意図した文字列をそのまま渡したい場合は引用符で囲む。

```yaml
mode: "0660"          # 引用符が無いと "432" になる
environment.FLAG: "no"
```

`user.incus-dev.*` 名前空間はidevが管理用に予約する。
プロジェクトから指定してはならない。

### 3.6.5 devices

任意。Incus deviceをそのまま渡す。

```yaml
devices:
  gpu0:
    type: gpu

  extra-data:
    type: disk
    source: /srv/dataset      # ホスト側パス
    path: /data

  http:
    type: proxy
    listen: tcp:127.0.0.1:8080
    connect: tcp:127.0.0.1:8080
```

- deviceの `source` に相対パスを指定した場合、project rootを基準に解決する
- ただし `pool` を伴う `disk` の `source` はストレージボリューム名であり、
  パスとして解決も検査もしない
- ホストのディレクトリをマウントする `disk` には、idevが `workspace` と
  同じidmap方式を適用する（3.7.3参照）。`shift` を明示した場合はそちらを尊重する
- `workspace` という名前のdeviceはidevが予約する（3.7参照）
- device名およびキーは `-` で始められない（incusのフラグと衝突するため）

---

## 3.7 workspace

```yaml
workspace:
  source: .
  target: /workspace
  idmap: auto
```

省略時は上記の既定値が使用される。

### 3.7.1 source

project rootを基準に解決する。

```yaml
source: .
```

の場合、project root（`.incus-dev/` の親）を意味する。

### 3.7.2 target

コンテナ内部のmount point。既定は `/workspace`。

### 3.7.3 idmap

非特権コンテナでbind mountを行う場合、ホスト側uid/gidとコンテナ内uid/gidの
対応付けが必要になる。方式によってホスト側の前提と結果が異なる。

| 値 | ホスト側の追加設定 | コンテナが作ったファイルのホスト側所有者 |
| --- | --- | --- |
| `auto`（既定） | 不要 | 環境依存（下記） |
| `raw` | **必要** | 実行ユーザー |
| `shift` | 不要 | root |
| `none` | 不要 | （書き込み不可） |

#### `raw`

instance configに `raw.idmap` を設定し、実行ユーザーのuid/gidを
コンテナのrootへ対応付ける。

```text
raw.idmap: uid <uid> 0
            gid <gid> 0
```

uidとgidは異なりうるため、個別に写像する。

開発用途では最も望ましい。コンテナ内でrootとして作ったファイルが、
ホスト側では実行ユーザーの所有になる。

ただしIncus daemon（root）に当該IDの使用許可が必要である。

```text
/etc/subuid: root:<uid>:1
/etc/subgid: root:<gid>:1
```

多くのディストリビューションの標準的なIncusセットアップ手順には、
コンテナ内uidを退避するための範囲（例 `root:1000000:1000000000`）しか
含まれない。上記は **それとは別に追加する必要がある**。

許可されていない場合、`raw` を明示していれば追加すべき行を示して失敗する。

#### `shift`

workspaceのdisk deviceに `shift=true` を設定し、idmapped mountを使う。

ホスト側の追加設定を要さず、ホストのファイルをコンテナから読み書きできる。

ただしコンテナ内でrootとして作ったファイルは、ホスト側でもrootの所有となる。
ビルド成果物の削除にsudoが必要になる点に注意する。

#### `auto`（既定）

`raw` が利用可能であれば `raw`、そうでなければ `shift` へ退避する。

退避した場合、その旨と `raw` を有効化する方法を警告として表示する。

これにより、ホストへ手を入れなくても `git clone` 後に `idev up` だけで
環境が構築できる状態を保つ（REQ-002）。

#### `none`

何も設定しない。プロジェクトが `instance.config` で自前に対応付ける場合や、
workspaceへの書き込みが不要な場合に使う。

#### 適用範囲

ここで決まった方式は、workspaceだけでなく
**ホストのディレクトリをマウントする `instance.devices` の `disk` にも適用する**。

workspaceだけを対象にすると、`shift` 方式のホストで
「workspaceは書けるが追加マウントは書けない」という不整合が生じるためである。

適用対象は `type: disk` かつ `source` を持ち、`pool` を伴わないdeviceに限る
（storage volumeやroot diskは対象外）。

プロジェクトがdeviceに `shift` を明示した場合は、そちらを尊重する。
ただし最適な値はホストに依存するため、**通常は書かないほうがよい**。

なお `instance.config` に `raw.idmap` が明示されている場合、
idevは対応付けに一切介入しない。

### 3.7.4 mount方式

Incus disk deviceを使用する。

概念例：

```yaml
devices:
  workspace:
    type: disk
    source: <project-root>
    path: /workspace
```

実際にはinstanceの `devices` として設定する（[05-incus.md](05-incus.md) 5.4）。

---

## 3.8 bootstrap

```yaml
bootstrap:
  - run: |
      command -v python3 >/dev/null 2>&1 ||
        (apt-get update && apt-get install -y python3)
```

任意。provision実行前に、コンテナ内で実行する準備処理。

- ステップは `run` のみ使用できる（`ansible` は使用できない）
- 指定した場合、idevの既定bootstrapを **完全に置き換える**
- 空リスト `bootstrap: []` を指定すると、bootstrapを行わない

既定動作および詳細は [06-provisioning.md](06-provisioning.md) を参照。

---

## 3.9 provision

```yaml
provision:
  - name: prepare
    run: sh /workspace/.incus-dev/scripts/prepare.sh

  - name: main playbook
    ansible:
      playbook: .incus-dev/ansible/site.yml

  - name: project setup
    run: |
      cd /workspace
      make setup
```

任意。順序付きのステップ配列。

各ステップは `run` / `ansible` / `galaxy` のいずれか一つを持つ。
二つ以上を持つステップ、一つも持たないステップは不正とする。

ステップは記述順に実行する。いずれかが失敗した時点で全体を失敗とする。

### 3.9.1 `run` ステップ

コンテナ内でコマンドを実行する。

短縮形：

```yaml
- run: apt-get update
```

完全形：

```yaml
- name: install packages
  run: |
    apt-get update
    apt-get install -y jq
  shell: /bin/sh          # 既定: /bin/sh
  cwd: /workspace         # 既定: コンテナの既定作業ディレクトリ
  user: root              # 既定: root
  env:
    DEBIAN_FRONTEND: noninteractive
```

| フィールド | 必須 | 説明 |
| --- | --- | --- |
| `run` | ○ | 実行するスクリプト本文 |
| `name` | | 表示名。ログとエラーに使用する |
| `shell` | | スクリプトを解釈するシェル |
| `cwd` | | 作業ディレクトリ（コンテナ内パス） |
| `user` | | 実行ユーザー。数値uidまたはユーザー名 |
| `env` | | 追加の環境変数 |

`.incus-dev/` はworkspaceの一部としてコンテナ内に見えるため、
スクリプトファイルは `<workspace.target>/.incus-dev/...` として参照できる。

`user` に数値uidを指定した場合はIncusのexecへそのまま渡す。
ユーザー名を指定した場合は、コンテナ内で `su` を用いて切り替える
（Incusのexecはuidしか受け付けないため）。

冪等性はプロジェクトの責務とする（REQ-005）。

### 3.9.2 `ansible` ステップ

ホスト側で `ansible-playbook` を実行する。

```yaml
- name: main playbook
  ansible:
    playbook: .incus-dev/ansible/site.yml
    vars: .incus-dev/ansible/vars.yml
    inventory: .incus-dev/ansible/extra-inventory.yml
    tags:
      - setup
    extra_args:
      - --diff
```

| フィールド | 必須 | 説明 |
| --- | --- | --- |
| `playbook` | ○ | playbookのパス（project root相対） |
| `vars` | | 追加varsファイル。`--extra-vars @<file>` として渡す |
| `inventory` | | 追加inventory。idev生成のinventoryに加えて渡す |
| `tags` / `skip_tags` | | `--tags` / `--skip-tags` |
| `extra_args` | | `ansible-playbook` へそのまま渡す引数 |

idevは接続用の一時inventoryを生成する。
Role解決はプロジェクトの責務とし、collectionの導入は `galaxy` ステップで行える。

詳細は [06-provisioning.md](06-provisioning.md) を参照。

### 3.9.3 `galaxy` ステップ

ホスト側で `ansible-galaxy install` を実行する。
Roleやcollectionの導入を `.incus-dev/` の中で完結させるためのものである
（REQ-007の方向）。

```yaml
- name: collections
  galaxy:
    requirements: .incus-dev/ansible/requirements.yml
    extra_args:
      - --force
```

| フィールド | 必須 | 説明 |
| --- | --- | --- |
| `requirements` | ○ | requirements.ymlのパス（project root相対） |
| `extra_args` | | `ansible-galaxy` へそのまま渡す引数 |

導入先はAnsibleの既定に従う。idevは場所を指定しない。

---

## 3.10 idevが提供する変数

idevはprovisioningの実行対象を各ステップへ通知する。
プロジェクトはこれによりinstance名等をハードコードせずに済む。

### 3.10.1 `run` ステップの環境変数

```text
IDEV_PROJECT_NAME
IDEV_INSTANCE
IDEV_WORKSPACE            コンテナ内のworkspaceパス
IDEV_WORKSPACE_SOURCE     ホスト側のproject rootパス
IDEV_INCUS_PROJECT
```

### 3.10.2 `ansible` ステップの変数

同じ情報を `--extra-vars` として渡す。

```yaml
idev_project_name: example-project
idev_instance: dev-example-project
idev_workspace: /workspace
idev_workspace_source: /home/user/src/example-project
idev_incus_project: default
```

`idev_` および `IDEV_` プレフィックスはidevが予約する。

---

## 3.11 パス解決規則

| 対象 | 基準 |
| --- | --- |
| `workspace.source` | project root |
| `devices.*.source`（相対パスの場合） | project root |
| `provision[].ansible.playbook` / `vars` / `inventory` | project root |
| `provision[].run` 内のパス | コンテナ内の絶対パス（利用者が記述） |

project rootの外を指すパスは警告してよいが、禁止はしない。

---

## 3.12 secrets

`dev.yml` はGitへcommitされる前提のため、**値そのものを書いてはならない**。

- API key
- Access token
- Password
- Private key

これらはホスト側から注入する。

```yaml
secrets:
  API_TOKEN:
    env: HOST_TOKEN          # ホストの環境変数から
  DEPLOY_KEY:
    file: ~/.config/key      # ホストのファイルから（前後の空白は除く）
  OPTIONAL_ONE:
    env: MAYBE
    optional: true           # 取得できなくてもよい
```

- `env` と `file` は排他。どちらか一方を指定する
- `IDEV_` で始まる名前は使えない（idevの予約）
- 取得できないものがあれば、**instanceへ触れる前に** どれが足りないかを
  まとめて報告する

注入先：

| ステップ | 渡し方 |
| --- | --- |
| `run` | 環境変数として渡す。同名の `env` を書いた場合はそちらが優先 |
| `ansible` | `--extra-vars` として渡す（0600の一時ファイル、実行後に削除） |

値はログとエラーの表示でマスクされる（[04-cli.md](04-cli.md) 4.10）。
ただし **ステップ自身が出力した内容はマスクできない**点に注意する。

---

## 3.13 volumes

instanceを作り直しても残るデータ領域。

```yaml
volumes:
  cache:
    path: /home/dev/.cache   # 必須。コンテナ内のマウント先
    pool: default            # 任意。既定 default
    size: 10GiB              # 任意。省略時はpoolの既定
```

- Incus上の名前は `<instance名>-<キー>` とする。
  複数チェックアウトが同じボリュームを共有しないようにするため
- 存在しなければ作成し、あればそのまま使う
- `idev destroy` では **削除しない**。作り直しても残すためのものであり、
  削除は `idev destroy --volumes` で明示的に指示する
- `idev rebuild` でも残す
- キー名は device 名と衝突してはならない

ビルドキャッシュやデータベースの実体など、
「作り直したいが消したくないもの」を置く。

---

## 3.14 shell

`idev shell` と `idev exec` の既定を指定する。

```yaml
shell:
  user: developer      # 実行ユーザー。省略時はinstanceの既定（root）
  command: /bin/bash   # 起動するシェル。既定 /bin/sh
  cwd: /workspace/src  # 作業ディレクトリ。既定は workspace.target
```

`user` に数値uidを指定した場合はIncusのexecへそのまま渡す。
ユーザー名の場合はコンテナ内で `su` を用いて切り替える
（Incusのexecはuidしか受け付けないため）。

---

## 3.15 incus

Incus側の操作対象を指定する。

```yaml
incus:
  project: development
```

CLIの `--incus-project` が指定された場合はそちらが優先される。
どちらも無ければ `default` を使う。

remoteの指定は持たない。**対応する予定もない。**
workspaceはホスト側パスのbind mountであり、remoteの向こう側には存在しないため
成立しない（[05-incus.md](05-incus.md) 5.6）。

---

