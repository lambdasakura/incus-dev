# 3. コマンドリファレンス

*[English version](../03-commands.md)*

## 3.1 共通事項

### プロジェクトの探索

`idev` はカレントディレクトリから親方向へ `.incus-dev/dev.yml` を探す。
サブディレクトリからでも実行できる。

```bash
cd src/foo/bar
idev status        # プロジェクトrootが見つかる
```

Gitリポジトリである必要はない。

### 共通フラグ

| フラグ | 既定 | 説明 |
| --- | --- | --- |
| `-v`, `--verbose` | | 実行した外部コマンドなど詳細を出力する |
| `-C`, `--directory <dir>` | カレントディレクトリ | 探索を開始するディレクトリ |
| `--incus-project <name>` | `dev.yml` の `incus.project`、無ければ `default` | Incus project |

### 終了コード

| コード | 意味 |
| --- | --- |
| `0` | 成功 |
| 非0 | 失敗 |

例外として `idev shell -- <command>` は、**コンテナ内コマンドの終了コードをそのまま返す**。

```bash
idev shell -- sh -c 'exit 42'; echo $?   # 42
```

### instance名

`dev-<プロジェクト名>-<チェックアウト先パスのハッシュ>` となる。
チェックアウトごとに別の環境になる。英数字とハイフン以外は正規化される。

```text
project.name: my.project_1   →   instance: dev-my-project-1-cb958c73
```

接尾辞は `dev.yml` の `project.scope` で決まる。`path`（既定）のほか、
場所によらずプロジェクトに1つなら `name`、ブランチごとなら `branch`（4.4）。

- 63文字を超える場合は切り詰める
- 正規化した結果が空になる名前（記号のみなど）は、
  元の名前のハッシュで区別する

---

## 3.2 `idev validate`

`dev.yml` を検証する。**Incusへ一切変更を加えない**ため、CIやIncusの無い環境でも実行できる。

```bash
idev validate
```

検査する内容：

- YAMLの構文、スキーマ、必須項目、未知のフィールド
- `runtime.version` の互換性
- provisionステップの構造（`run` と `ansible` の排他など）
- 参照先ファイルの存在（playbook、vars、inventory、workspace source）
- `profiles: []` の場合に root disk device が宣言されているか
- 予約キー（`user.incus-dev.*`）を使っていないか

Profileがホストに実在するかどうかは確認しない（Incusへ問い合わせないため）。

---

## 3.3 `idev up`

instanceを用意し、bootstrapとprovisionを実行する。

```bash
idev up
```

処理の流れ：

1. `dev.yml` の読み込みと検証
2. workspaceのidmap方式の決定（ホストの設定を確認）
3. 参照Profileの存在確認（無ければ失敗）
4. instanceが無ければ作成、あれば **作り直さずに設定を再適用**
5. workspaceとdeviceの適用
6. 起動と、コマンドを実行できるようになるまでの待機
7. bootstrap → provision の実行

**既にあるinstanceを破壊することはない。** `dev.yml` を変更したあとに
再実行すれば、リソースやdeviceの変更が反映される。

`dev.yml` から設定やdeviceを削除した場合も追従する。ただし
**`idev` が適用したものだけ** が対象で、`incus config set` などで
手動追加した設定には触れない。

| フラグ | 説明 |
| --- | --- |
| `--restart` | 反映に再起動が必要な変更があれば、instanceを再起動する |
| `--dry-run` | 実行予定の操作を表示するだけで、Incusへ変更を加えない |

```bash
idev up --restart
```

反映に再起動が必要な変更（`raw.idmap` / `security.nesting` /
`security.privileged`）があるとき、instanceを再起動する。
既定では警告のみを表示する。

```bash
idev up --dry-run
```

作成・再適用・削除の予定を一覧するだけで、Incusへは一切変更を加えない。
`dev.yml` を書き換えたあと、何が起きるかを先に確認したいときに使う。

`up` と同じホスト側の検査（imageが解決できるか、storage poolがあるか、
Profileが実在するか、`ansible-playbook` があるか）も行い、
いずれかが満たされない場合は予定を表示せずに失敗する。
`idev validate` にホスト側検査のフラグが無いのはこのためである。
`validate` はオフラインで `dev.yml` を読むだけで、ホストを見るのはこちらが担う。
表示された予定は実行可能な予定なので、
`idev validate && idev up --dry-run` はCIのゲートとして使える。

idev が作ったものでないinstanceが同名で存在する場合は、
何もせずに失敗する（誤って壊さないため）。

---

## 3.4 `idev provision`

instanceを作り直さず、bootstrapとprovisionのみ再実行する。

```bash
idev provision
```

主な用途：

- provisionステップを書き換えたとき
- playbookやスクリプトを変更したとき

instanceが存在しない場合は明示的に失敗する。暗黙的に `idev up` へ切り替えない。

`instance.config` や `devices` の変更は反映しない（それは `idev up` の役割）。

### 一部だけ実行する

| フラグ | 説明 |
| --- | --- |
| `--list` | ステップの一覧を表示する（Incusへは触れない） |
| `--step <名前または番号>` | 指定したステップのみ実行する（複数指定可） |
| `--from <名前または番号>` | 指定したステップ以降を実行する |

```bash
idev provision --list
idev provision --step 3
idev provision --step setup-go --step install-tools
idev provision --from 2
```

`--list` は1ステップにつきタブ区切りの1行を出し、0件のときは何も出さない。
スクリプトから行数を数えられるようにするためである。
0件であることは、idevの他の出力と同じく標準エラー出力へ出る。

`--step` を複数指定しても、実行順は **宣言順** である（指定順ではない）。
失敗時のステップ番号は、一部だけ実行した場合も全体の中での位置で示す。

`--step` と `--from` は同時に指定できない。

---

## 3.5 `idev shell`

コンテナ内でシェル、または指定したコマンドを実行する。

```bash
idev shell                      # 対話シェル（作業ディレクトリは /workspace）
idev shell -- make test         # コマンドを実行して終了
idev shell -- sh -c 'cd /workspace && go build ./...'
```

- 標準入出力が端末に接続されている場合のみ擬似端末を割り当てる。
  パイプやリダイレクト経由でも出力が壊れない

```bash
idev shell -- cat /etc/os-release > os.txt
idev shell -- go test ./... | tee test.log
```

- コンテナが停止していれば起動してから実行する
- 実行ユーザー・シェル・作業ディレクトリは `dev.yml` の `shell` で変えられる
  （既定はroot、`/bin/sh`、`workspace.target`。[4. dev.yml](04-dev-yml.md) 参照）

コマンドの終了コードはそのまま `idev` の終了コードになる。

### `--user`

その実行に限り `shell.user` を上書きする。`idev exec` にも同じフラグがある。

```bash
idev shell --user root
idev exec --user root -- apt-get install -y tcpdump
```

`shell.user` は「普段どのユーザーで作業するか」というプロジェクト全員の宣言であり、
一度rootが要るだけで `dev.yml` を書き換えるのは釣り合わない。
値は名前かuidで、解釈は `shell.user` と同じである。
`shell.user` を設定した状態でinstanceの既定ユーザーへ戻すには、
`--user root` のように名前を指定する。

`examples/dev-user/` がこの構成の実物である。
provisioningでアカウントを作り、`shell.user` でそこへ入り、
rootが要るときは `--user root` を使う。

---

## 3.5.1 `idev exec`

コンテナ内でコマンドを実行する。**擬似端末は割り当てない。**

```bash
idev exec -- make test
idev exec -- go test ./... | tee test.log
```

`idev shell` との違いは、端末に接続されていても擬似端末を割り当てない点だけである。
CIやスクリプトから呼ぶ場合は、環境によって挙動が変わらない `exec` を使う。

**そのため `idev exec -- /bin/sh` は対話シェルにならない。**
シェルから見て端末が無いので、プロンプトが出ず、職制御も効かない。
入力したコマンドは実行され、出力も返る。

```console
$ idev exec -- /bin/sh
ls
bin  etc  workspace
```

それは `idev shell` の役割である。
名前を指定して実行するコマンドには `exec`、中に入って作業するシェルには `shell`。

コマンドの指定は必須である（省略した場合は `idev shell` を使うよう促して失敗する）。

---

## 3.6 `idev status`

対象instanceの状態を表示する。

```bash
idev status
```

instanceが未作成の場合は `Status: NOT CREATED` となるが、
コマンド自体は成功する（`0` を返す）。

機械可読な出力：

```bash
idev status --json
```

```json
{
  "project": "my-project",
  "instance": "dev-my-project-cb958c73",
  "status": "Running",
  "image": "images:ubuntu/24.04",
  "workspace": "/workspace",
  "workspace_source": "/home/you/other-checkout",
  "workspace_source_declared": "/home/you/src/my-project",
  "exists": true,
  "managed": true,
  "profiles": ["default"],
  "config": { "limits.cpu": "4" },
  "devices": ["workspace(disk)"],
  "addresses": [
    { "interface": "eth0", "family": "inet", "address": "10.216.13.42" }
  ],
  "provision_steps": 1,
  "runtime": "1.0",
  "incus_project": "default"
}
```

`profiles` / `config` / `devices` / `addresses` は、空になる場合は出力されない。
instanceが存在しない場合もこれに含まれる。
`config` に出るのは `limits.*` のキーのみであり、それを1つも設定していない
instanceでは項目ごと現れない。それ以外はIncusが報告すべきもので、
`incus config show` が全て表示する。
`image` と `workspace_source` はinstanceが実際に持っている値である。
dev.yml が別のものを求めている場合に限り、`image_declared` と
`workspace_source_declared` が併記される。
`runtime` は dev.yml の `runtime.version` の値であり、宣言が無ければ出力されない。

`addresses` はinstanceが持つglobalアドレスの一覧で、順序は `idev ip` が
選ぶ順である（3.6.1）。止まっているinstanceにアドレスは無いため、
この項目も `Addresses:` の行も現れない。「なし」と書くと、
動いていないのか、動いていてアドレスがまだ付いていないのかの区別がつかない。

```bash
# 実行中かどうかで分岐する例
[ "$(idev status --json | jq -r .status)" = "Running" ] || idev up
```

---

## 3.6.1 `idev ip`

instanceへ到達するためのアドレスを1つ、それだけを標準出力へ出す。
他のコマンドへ埋め込むための出力である。

```bash
ssh user@$(idev ip)
```

IPv4をIPv6より優先する。interfaceが複数あるinstanceでも、
毎回同じものを返す（優先するfamilyのうち、interface名が辞書順で最初のもの）。

返せるアドレスが無い場合、標準出力へは何も書かず、理由を標準エラー出力へ出して
非0で終了する。上の書き方では、これが重要になる。
空文字を返すと `ssh user@` がローカルユーザーへ繋ごうとする。

| 状況 | 言うこと |
| --- | --- |
| instanceが無い | `idev up` を実行する |
| 動いていない | アドレスは動いている間だけ存在する |
| 動いているがアドレスが無い | NICが無いか、アドレスがまだ割り当てられていない |

待たない。`idev up` が起動時にアドレスを待つ。
また `$(...)` の中では、待つ読み取りは固まったのと区別がつかない。

全アドレスが要る場合は `idev status --json` を使う。

---

### 古いidevが作ったinstance

instanceの命名やマーカーの付け方が変わる前に作られたinstanceは引き継がれない。
`idev up` は隣に新しいinstanceを作り、その際に古い方の名前と
削除用の `incus delete` を表示する。

移行は行わない。idevが必要とする情報の多くが、そのinstanceには記録されていないためである。
具体的に失われるものは以下。

| 欠けている記録 | idevができないこと |
| --- | --- |
| `user.incus-dev.image` | 何のimageから作られたかを答える。`status` は推測せずその旨を表示する |
| `user.incus-dev.managed` | `dev.yml` から消したconfigキーの削除に追従する |
| `user.incus-dev.devices` | `dev.yml` から消したdeviceの削除に追従する |

こうしたinstanceに対する最初の `idev up` は、追従できなくなるconfigキーとdeviceを
名前で挙げる。それを言えるのはその実行が最後だからである
（記録を書いた時点で、記録の外にあるものへは手が届かなくなる）。
古い `dev.yml` が設定したものと利用者が手で設定したものをidevは区別できないため、
どちらも削除しない。

永続ボリュームは例外で、disk deviceとして接続されたままなので
記録が無くても `idev destroy --volumes` が見つけて削除する。

**確実なのは作り直すことである。** 古いinstanceから必要なものを取り出し、
`incus delete` して `idev up` を実行する。
workspaceはbind mountなので、ホスト側のソースツリーは影響を受けない。

---

## 3.7 `idev destroy`

instanceを削除する。

```bash
idev destroy
idev destroy --force        # 確認を省略（CI・スクリプト用）
```

- **ホスト側のソースツリーは削除しない。** workspaceはbind mountであり、
  削除されるのはコンテナだけである
- idev が作ったものでないinstanceは削除しない

| フラグ | 説明 |
| --- | --- |
| `-f`, `--force` | 確認せずに実行する |
| `--volumes` | idevがこのinstance向けに作成した永続ボリュームをすべて削除する（宣言から消したものも含む） |

既定では永続ボリュームを残す。instanceを作り直してもキャッシュを保ちたい、
という用途で使われるためである。残した場合はその旨を表示する。

---

## 3.8 `idev rebuild`

instanceを破棄して作り直す。

```bash
idev rebuild
idev rebuild --force
```

コンテナ内の状態はすべて失われる。`idev up` も削除に追従するが、
それは `idev` が適用した設定・deviceに限られる。手動で加えた設定ごと
きれいな状態へ戻したいときに使う。

---

## 3.8.1 `idev snapshot`

環境を壊す前に退避しておき、あとで戻せる。

```bash
idev snapshot create before-upgrade
idev snapshot list
idev snapshot restore before-upgrade
idev snapshot delete before-upgrade
```

名前を省略すると日時（`20260831-142530`）が付く。
`restore` と `delete` は確認を求める（`--force` で省略）。
確認は標準エラー出力へ出るため、標準出力を取り込んでも混ざらない。

`list` は1スナップショットにつき1行を出し、0件のときは何も出さない。

`create` は `/` と空白を含む名前を拒否する（Incus自身が拒否するため）。
`.` と `..` も拒否する。ストレージドライバによっては、これらの名前で作った
snapshotが残り、instanceごと削除できなくなるため。
`restore` と `delete` は名前を検査しないので、idev以外が作ったsnapshotにも到達できる。

**ホスト側のworkspaceには影響しない。** bind mount であり、
instanceの状態ではないためである。

---

## 3.9 シェル補完

```bash
idev completion bash > /etc/bash_completion.d/idev
idev completion zsh  > "${fpath[1]}/_idev"
idev completion fish > ~/.config/fish/completions/idev.fish
```
