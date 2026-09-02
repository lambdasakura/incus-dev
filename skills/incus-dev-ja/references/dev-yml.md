# `.incus-dev/dev.yml` リファレンス

プロジェクトのルート（`.incus-dev/` を置いたディレクトリ）が
すべての相対パスの基準になる。`idev` は上位ディレクトリを辿って
`.incus-dev/dev.yml` を探すので、リポジトリ内のどこからでも実行できる。

## 全体像

```yaml
schema: 1                      # 必須。現在は 1 のみ

runtime:
  version: "1.0"               # 任意。idevの想定バージョン

project:
  name: my-project             # 必須。instance名 dev-<name> の元
  scope: name                  # name（既定） | path | branch

instance:
  image: images:ubuntu/24.04   # 必須
  profiles: [default]          # 既定は [default]。[] はProfileを使わない
  config:                      # Incusのinstance configをそのまま渡す
    limits.cpu: "8"
    limits.memory: 16GiB
  devices:                     # Incusのdeviceをそのまま渡す
    data:
      type: disk
      source: ./assets         # プロジェクトルート基準
      path: /data

workspace:
  source: .                    # 既定はプロジェクトルート
  target: /workspace           # 既定 /workspace
  idmap: auto                  # auto（既定） | raw | shift | none

shell:                         # idev shell / idev exec の既定
  user: developer
  command: /bin/bash           # 既定 /bin/sh
  cwd: /workspace/src          # 既定は workspace.target

incus:
  project: development         # Incus project。CLIの --incus-project が優先

volumes:                       # 作り直しても残るデータ
  cache:
    path: /home/dev/.cache     # 必須
    size: 10GiB
    pool: default

secrets:                       # ホストから注入する。値は書かない
  API_TOKEN:
    env: HOST_TOKEN
  DEPLOY_KEY:
    file: ~/.config/key
    optional: true

bootstrap:                     # provisionの前段。省略時は既定が動く
  - run: command -v python3 >/dev/null 2>&1 || dnf install -y python3

provision:                     # 本体
  - name: base
    run: |
      apt-get update
      apt-get install -y --no-install-recommends git make
```

## ステップ

`bootstrap` と `provision` の要素は同じ形。`run` / `ansible` / `galaxy` の
**いずれか1つ**を持つ。

```yaml
- name: setup            # 任意。ログとエラーに出る。付けておくと追いやすい
  run: |                 # コンテナ内で実行するスクリプト
    make deps
  shell: /bin/bash       # 既定 /bin/sh
  cwd: /workspace        # 実行時の作業ディレクトリ
  user: developer        # 数値uidはIncusへ、名前は su で切り替える
  env:                   # 追加の環境変数。値は表示時に隠される
    CGO_ENABLED: "0"
```

```yaml
- name: playbook
  ansible:
    playbook: .incus-dev/ansible/site.yml
    vars: .incus-dev/ansible/vars.yml
    inventory: .incus-dev/ansible/inventory.ini   # 省略時は自動生成
    tags: [setup]
    skip_tags: [slow]
    extra_args: ["--diff"]
```

```yaml
- name: collections
  galaxy:
    requirements: .incus-dev/ansible/requirements.yml
    extra_args: ["--force"]
```

ansibleステップはホスト側の `ansible-playbook` を使い、
`community.general.incus` connection pluginでコンテナへ入る（SSHは使わない）。
inventory上のホスト名は `dev` なので、playbookは `hosts: dev` と書く。

## run ステップへ渡る環境変数

```text
IDEV_PROJECT_NAME       プロジェクト名
IDEV_INSTANCE           instance名
IDEV_WORKSPACE          コンテナ内のworkspaceパス
IDEV_WORKSPACE_SOURCE   ホスト側のプロジェクトルート
IDEV_INCUS_PROJECT      Incus project
```

`env:` で同名を指定すると上書きされる。`secrets:` の値も環境変数として渡る。

## bootstrap の既定動作

`bootstrap` を省略し、かつ `provision` に ansible ステップがある場合のみ、
Debian系を前提にpython3を導入する既定bootstrapが動く。
Debian系以外のimageでは失敗するので、その場合は `bootstrap` を明示する。

`bootstrap: []` と書けば何も実行しない。

## workspace.idmap

ホストのディレクトリをコンテナへ共有するときのuid/gid対応付け。

| 値 | ホスト側の追加設定 | コンテナが作ったファイルのホスト側所有者 |
| --- | --- | --- |
| `auto`（既定） | 不要 | `raw` が使えれば実行ユーザー、でなければroot |
| `raw` | `/etc/subuid`・`/etc/subgid` に `root:<uid>:1` | 実行ユーザー |
| `shift` | 不要 | root |
| `none` | 不要 | （書き込み不可） |

`instance.devices` で追加したdiskにも同じ対応付けが適用される。

## project.scope

同じリポジトリを複数の場所にcloneする、あるいはブランチごとに
環境を分けたいときだけ変える。既定を変えると既存の環境が別物になる。

| 値 | instance名 |
| --- | --- |
| `name`（既定） | `dev-my-project` |
| `path` | `dev-my-project-cb958c73` |
| `branch` | `dev-my-project-feature-x` |

## 書くときの注意

- `instance.config` の値は数値・真偽値で書いてもよい（文字列へ変換される）
- `user.incus-dev.*` は idev が管理に使うので書かない
- `-` で始まるキー、`=` を含むキーは使えない
- `profiles: []` にする場合、root diskとネットワークも自分で宣言する
- 相対パス（`workspace.source`、device の `source`、playbook など）は
  すべてプロジェクトルート基準
