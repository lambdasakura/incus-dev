# 4. `dev.yml` リファレンス

ファイルの位置は `.incus-dev/dev.yml` で固定である。

*[English version](../04-dev-yml.md)*

## 4.1 全体像

```yaml
schema: 1                          # 必須

runtime:
  version: "1.0"                   # 任意。要求するruntime契約のバージョン

project:
  name: my-project                 # 必須。instance名の元になる
  scope: name                      # 任意。path（既定）| name | branch

instance:
  image: images:ubuntu/24.04       # 必須
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

このプロジェクトが要求する **runtime契約** のバージョン。現在は `1.0` で、
`idev` のリリース番号とは別物である。dev.ymlの意味が、古いidevでは
満たせない形で変わったときに上がる。満たさない `idev` は実行を拒否する。

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

この正規化は小文字化し、`.` と `_` を `-` にする。そのため
`My.Project` / `my_project` / `my-project` はいずれも同じinstance名を要求する。
既定の `scope: path` では、それが衝突するのは同じディレクトリにある場合だけである。
`scope: name` では必ず衝突し、先に起動したものだけがそれを得て残りは拒否される。
2つのプロジェクトが1つのinstanceを共有することはできないためである。
最初から正規化後の形で書いておくと迷わない。

同じマシンで複数のプロジェクトを扱う場合、名前が衝突しないようにする。

### `scope`

instance名の区別の仕方。2つのディレクトリが環境を共有するか、
それぞれ持つかがこれで決まる。

```yaml
project:
  name: my-project
  scope: name        # path（既定） | name | branch
```

| 値 | instance名 | 用途 |
| --- | --- | --- |
| `path`（既定） | `dev-my-project-cb958c73` | チェックアウト先ごとに環境を分ける |
| `name` | `dev-my-project` | 場所によらずプロジェクトに1つ |
| `branch` | `dev-my-project-feature-x` | ブランチごとに分ける（Gitが必要） |

既定が `path` なのは、1つのリポジトリを2箇所へcloneするのが普通のことであり、
`name` ではその2つが1つのinstanceを共有するためである。共有すると、
コンテナ内の `/workspace` は最後に `up` したチェックアウトを指すので、
もう一方から `idev shell` で触るファイルは自分のtreeではない。
idevは気づいた時点で警告するが、警告されるより共有しないほうがよい。

プロジェクトに環境が1つで足り、instance名を短く保ちたい場合は `name` を選ぶ。
`path` はプロジェクトではなくディレクトリに追従する点に注意する。
チェックアウトを移動すると別の名前になり、それまでの環境は置き去りになる
（`idev up` がそれを報告し、`incus delete` のコマンドを示す）。

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

**既存instanceに対して変更しても効果は無い。** instanceは作成時のimageを
持ち続けるため、`idev up` は警告するだけで作り直さない。
作り直すには `idev rebuild` を使う。

idev は特定のOSを前提としない。imageと構成手順の整合はプロジェクトの責任である。

### `type` は無い

instanceは常にコンテナである。`type:` と書くと未知のキーとして
`idev validate` が失敗する。

virtual-machine では workspace を bind mount できず、`raw.idmap` や
disk の `shift` もコンテナ固有の仕組みである。workspaceの共有方式を
別に設計する必要があり、対応する予定は無い。

### `profiles`

**ホストに既に存在するProfileを名前で参照するだけ** のフィールド。

`image` と同様、instance作成時にのみ設定される。後から変更した場合、
`idev up` は付け替えずに警告する。どのProfileをidevが付けたのかという記録が
無いため、利用者が自分で付けたものを外しかねないからである。
新しい一覧で作り直すには `idev rebuild` を使う。

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

**YAMLが別の意味に取る値は引用符で囲むこと。** 変換されるのは書いた文字列では
なくYAMLが解釈した後の値であり、`0660` は8進数として `"432"` に、`no` は真偽値
として `"false"` になる。そのまま渡したい場合は `"0660"` `"no"` と書く。

64bitに収まらない整数も同様で、浮動小数点数として読まれるため
`99999999999999999999999999999999999` は `1e+35` としてIncusへ渡る。
引用符で囲むこと。

`limits.cpu` / `limits.memory` はコンテナでも有効で、コンテナ内から見える
CPU数（`nproc`）とメモリ量（`/proc/meminfo`）にも反映される。
`make -j$(nproc)` のような並列度の自動判定を、ホスト全体ではなく
この環境の割り当てに合わせられる。

`limits.*` の変更は実行中のコンテナにもそのまま反映されるため、
再起動は要らない。

`user.incus-dev.*` は idev の管理用に予約されているため使用できない。

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

`pool` を伴う `disk` は別物である。その場合の `source` はそのpool上の
storage volume名であってパスではなく、idevはパスとして解決も検査もしない。
idevに作成・維持させたいvolumeは、ここではなく `volumes`（4.10）で宣言する。

ここでの名前とキーは、適用前に `idev validate` が検査する。

| | 規則 | 理由 |
| --- | --- | --- |
| device名 | `-` で始められない | incusが自分のフラグとして読んでしまう |
| device名 | `,` を含められない | idevは自分が設定したdeviceをカンマ区切りの一覧で記録している |
| キー | `-` で始められず、`=` と `,` を含められない | 上と同じ2つの理由による |

同じ規則が `instance.config` のキーにも適用される。

---

## 4.6 `workspace`

ホストのディレクトリをコンテナへマウントする設定。省略時は既定値が使われる。

```yaml
workspace:
  source: .            # project rootが基準。既定 "."
  target: /workspace   # コンテナ内のパス。既定 /workspace
  idmap: auto          # 既定 auto
```

コピーではなくbind mountなので、ホスト側の編集が即座に反映される。

### 複数のディレクトリをマウントする

マップとして書く。キーがそのままdevice名になる。

```yaml
workspace:
  idmap: auto          # instance全体の設定なので、この位置のまま
  main:
    source: .
    target: /workspace
  other-repo:
    source: ../other-repo
    target: /other-repo
  dataset:
    source: /srv/dataset
    target: /data
    readonly: true
```

`main` がプロジェクト自身のtreeである。shellの作業ディレクトリ、
provisioningの実行場所、`idev status` の表示は、すべてこれを指す。
省略すれば上と同じ既定で補われるので、
ディレクトリを1つ足すために自分のtreeを書き直す必要はない。

```yaml
workspace:
  other-repo:
    source: ../other-repo
    target: /other-repo
```

注意点：

- `target` に既定があるのは `main` だけである。
  2つのmountが `/workspace` を共有すると同じ場所を奪い合う
- `idmap` はmountの中に書けない。Incusの `raw.idmap` はinstanceに1つであり、
  diskごとに変えられない
- `main` は `workspace` という名前のdeviceとして適用される。
  そのため `workspace` はmount名に使えない。他のキーはそのままdevice名になる
- mount名は `instance.devices` や `volumes` のキーと衝突できず、
  `-` で始められず、`,` を含められない
- この形式を使うプロジェクトは `runtime.version: "1.1"` を指定する（4.3）。
  古いidevはこのdev.ymlを読めず、それを伝えるのがこの指定である

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

上の表は、コンテナが **root として** 作ったファイルについてのものである。
どちらも「何もしない状態」との差で読むほうが分かりやすいので、
実行ユーザーが uid 1000 のホストで測った3通りを並べる。

| | コンテナのidマップ | root が書いたもの | uid 1000 が書いたもの |
| --- | --- | --- | --- |
| `none` | uid 0 → host 1000000 | host 1000000 | host 1001000 |
| `raw` | uid 0 → host **1000**、他はそのまま | host **1000** | host 1001000 |
| `shift` | `none` と同じ | host **0** | host **1000** |

`raw` はマウントを翻訳しない。「ホストの1000はコンテナの0」という幅1の
エントリをidマップへ挿すだけなので、変わるのはコンテナのrootだけである
（右列が `none` と一致する）。`/etc/subuid` に要るのが1個分なのもこのためで、
効果はworkspaceに限らずコンテナ全体に及ぶ。

`shift` はidマップを変えず、そのマウントで打ち消す。通過する全てのidから
名前空間が足した 1000000 を引く。コンテナ uid N がホスト uid N に見えるのは、
2つの翻訳が打ち消し合った結果であって、翻訳が無いからではない。

そのため一般アカウントで作業できるのは `shift` の側である。`raw` では
workspaceがコンテナ内からrootの所有に見え、書き込めない。
`examples/dev-user/` がその構成であり、`idmap: shift` にしているのはこのためである。

`auto` は `raw` が使えるならそれを、使えなければ `shift` へ退避し、警告を表示する。
どちらでもworkspaceは読み書きできる。違いは生成物の所有者だけである。

ここで決まった方式は、上の全mountにも、`instance.devices` でマウントした
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
- `run` のみ使用できる（`ansible` と `galaxy` は使えない）

詳細は [05-provisioning.md](05-provisioning.md) を参照。

---

## 4.8 `provision`

コンテナ内部の構成手順。順序付きの配列で、上から順に実行される。

各ステップは `run` / `ansible` / `galaxy` のいずれか一つを持つ。

```yaml
provision:
  - name: prepare
    run: apt-get update

  - name: main
    ansible:
      playbook: .incus-dev/ansible/site.yml
```

書き方は [05-provisioning.md](05-provisioning.md) を参照。

ステップの `name` に制御文字は使えない。タブや改行が入ると
`idev provision --list` の行（1行1ステップ）が割れたり列がずれたりするためである。

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

`size` はvolume作成時にのみ読まれる。後から変更しても効果は無く、
idevがリサイズすることもない。`incus storage volume delete` で消してから
`idev up` を実行すると作り直される。

`pool` は違い、毎回読まれる。変更してもデータは移動しない。
次の `idev up` は新しいpool上に空のvolumeを作り、同じコンテナ内パスにそれをmountする。
データの入ったvolumeは元のpoolに元の名前のまま残る。
データごと移したい場合は、`pool` を書き換える前に
`incus storage volume copy` でコピーしておくこと。

`idev destroy --volumes` はinstanceと一緒にデータも消す。
実行前に両方について確認を求める。

**宣言から消したり名前を変えたりしてもデータは消えない。**
idevは作成したvolumeを覚えているので、宣言から消えたものは `idev up` が警告し、
`idev destroy --volumes` の削除対象にも残る。

ビルドキャッシュやデータベースの実体など、
「環境は作り直したいが消したくないもの」に向く。

### 一般ユーザーで使う場合

volumeは空でroot所有として現れるため、root以外のアカウントは
provisioningが手当てするまで書き込めない。mount pointを `chown` する。
ホストのディレクトリではないので、コンテナの外は何も変わらない。

homeの下にvolumeをmountする場合、そのhomeはmountによって作られる。
mountはprovisioningより前なので、`useradd --create-home` は既にある
ディレクトリを見つけ、所有者をrootのままにする。homeもchownする。

`examples/volumes/` がその両方を行っている。

---

## 4.11 secrets

`dev.yml` はGitにコミットされる前提なので、**値そのものは書かない**。
ホスト側から注入する。

```yaml
secrets:
  API_TOKEN:
    env: HOST_TOKEN          # ホストの環境変数から
  DEPLOY_KEY:
    file: ~/.config/key      # ホストのファイルから（~ を展開する）
  OPTIONAL_ONE:
    env: MAYBE
    optional: true           # 無くてもよい
```

- `run` ステップには環境変数として渡る
- `ansible` ステップには `--extra-vars` として渡る（0600の一時ファイル）。
  `{{ ... }}` を含む値がテンプレートとして評価されることも、
  型を読み直されることも無い形で渡す。`0123456` は数値にならず文字列のまま届く
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

`user` は名前でもuidでもよい。Incusのexecは数値しか受け付けないため、
idevはコンテナ内で1度だけ `getent passwd` を実行し、得られたuid/gidで実行する。
`HOME` / `USER` / `LOGNAME` / `SHELL` もログイン時と同じように設定する。
指定したユーザーはコンテナ内に存在している必要があるので、
provisionで作成しておくこと。

`su` では包まない。`su` はシェルを自分の子として起動するため、
シェルは端末のセッションリーダーにならずフォアグラウンドプロセスグループを
取れない。bashは `cannot set terminal process group` を出し、
職制御の無いシェルになる。

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
