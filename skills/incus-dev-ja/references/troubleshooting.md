# エラーから引く

`idev` が出したエラー文言で探す。ホスト環境が原因のものは
リポジトリの [docs/troubleshooting.ja.md](../../../docs/troubleshooting.ja.md) に詳しい。

## 設定・使い方

### `instance <name> does not exist; run 'idev up' first`

`provision` / `shell` / `exec` / `rebuild` / `snapshot` を、
環境を作る前に呼んでいる。`idev up` を先に実行する。
暗黙に作りにいくことはない（意図しない構築を避けるため）。
（`idev status` は例外で、`NOT CREATED` を表示して正常終了する。）

### `instance <name> does not exist; nothing was deleted`

既に無い環境に対して `idev destroy` を実行している。
実際に削除した場合と区別できるよう終了コードは1になる。
このメッセージが言っていないことに注意する。永続volumeはinstanceより長く残るため、
`--volumes` を付けていた場合は `incus storage volume list <pool>` で確認する。

### `... nothing names these again: <pool>/<volume>`

`destroy` / `rebuild` の待機を中断し、daemon側は削除を続けていた場合に出る。
多くはCtrl-Cで、待機を中断してもdaemon側の削除は止まらないため起きる。
どのvolumeがidevのものかという記録はinstance上にあるので、
`dev.yml` から外れていたvolumeを指すものが何も残らない。
このメッセージが、その名前が現れる最後の場所である。
後で `idev up` に拾わせるなら `dev.yml` に書き戻し、
不要なら表示されたコマンドで削除する。

idevは表示前に実際に確認するため、instanceが残っている場合には出ない。
provisionステップで失敗した `rebuild` は記録を保持しており、
その場合の対処は `idev provision` であって、削除ではない。
`dev.yml` に宣言されたままのvolumeも、次の `idev up` が名前で拾うため列挙しない。

### `idev shell` を中断したあと端末の表示が壊れた

`reset` を実行する。`idev shell` は端末をraw modeにし、セッション終了時に戻す。
2回目のCtrl-Cはプロセスを即座に終了させるためこの復帰処理を飛ばす（`kill -9` と同じ）。
1回目のCtrl-Cは正しく後始末をするので、まず一度だけ送って少し待つほうがよい。

### `no answer on standard input; pass --force to proceed without asking`

破壊操作の確認を行うコマンドを、標準入力が無い状態で実行している
（CI、hook、stdinのリダイレクトなど）。これは拒否ではなく、
答える相手がいなかったという意味である。`--force` で確認を省略するか、
標準入力から答えを渡す（`printf y | idev destroy`）。

### `instance <name> exists but is not managed by idev for project "<name>"`

同名のinstanceが手動で作られている。idevは印（`user.incus-dev.project`）の
無いinstanceを破壊しない。既存を消すか、`project.name` を変えて名前をずらす。

### `the instance changed while idev was working on it`

実行中に別の `idev` が同じ環境へ書き込んだ。多くは端末を2つ開いている場合である。
idevは「どのvolumeが自分のものか」「どのdeviceを設定したか」を
instanceの読み取りから決めるため、読み取り以降に変更されていた場合は書き戻しを拒否する。
そのまま書くと相手が記録した内容を消してしまい、
どのidevコマンドからも名前を得られないvolumeがpool上に残るためである。

もう一方の実行が終わってから `idev up` をやり直す。
やり直した実行はその時点のinstanceを読んで宣言全体を適用するため、
途中まで進んでいた分もそこで揃う。

### `instance <name> already belongs to project "<other>"`

手動で作られたinstanceではなく、**別プロジェクトの環境**である。削除してはならない。
instance名はIncusが扱えない文字を落とす。小文字化され、`.` と `_` は `-` になる。
そのため `My.Project` / `my_project` / `my-project` はいずれも同じ基底名を
要求する。原因がこれである場合、メッセージがそう述べる。どちらかのプロジェクト名を変える。

既定の `project.scope: path` では接尾辞がディレクトリごとに変わるため、
これが起きるのは2つのプロジェクトが**同じディレクトリ**にある場合か、
両方が `scope: name` の場合である。

### `incus profile(s) not found on this host: <name>`

`instance.profiles` はホストに **既にあるProfileの名前参照** であって、
idevはProfileを作らない。対処は次のいずれか。

- ホスト側でProfileを作る
- `instance.profiles` から外し、必要な設定を `instance.config` /
  `instance.devices` へ直接書く（可搬性はこちらが高い）

### `cannot resolve secret(s): ...`

`secrets:` が参照するホストの環境変数・ファイルが無い。
**instanceへ触れる前に止まる**ので、環境は壊れていない。
値を用意するか、無くてよいなら `optional: true` を付ける。

### `configuration is invalid` / スキーマのエラー

`idev validate` の出力に `dev.yml` 内のパスが出る。よくある原因:

- 参照先のファイルが無い（playbook、vars、device の source）
- ステップが `run` / `ansible` / `galaxy` のどれも持たない、または複数持つ
- 予約キー（`user.incus-dev.*`）を使っている

## 実行時

### provisionでパッケージ導入が失敗する（`temporary error`、名前解決はできるのに外へ出られない）

**ほぼホスト側のネットワーク問題**。`dev.yml` を書き換えても直らない。
ホストにDockerが入っていると、DockerのFORWARDポリシー（DROP）が
Incusのブリッジを巻き添えにする。

```bash
sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT
sudo iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
```

2行とも要る（1行目が送信、2行目が戻り）。再起動で消えるので永続化が要る。
詳細は docs/troubleshooting.ja.md の 1。

### `instance <name> did not become ready within <duration>`

起動はしたが、コンテナ内でコマンドを実行できる状態にならない。
末尾の終了コードやエラーが原因を示す。imageが壊れている、
`instance.config` の指定でinit が起動できていない、などを疑う。

### `network address not assigned` の警告のあとステップが失敗する

IPv4が付く前にprovisionが始まっている。idevはIPv4を待つので通常は出ない。
出る場合はネットワーク構成側（上のDocker競合を含む）を疑う。

### `<ステップ名>: exited with code N`

コンテナ内のスクリプトが失敗した。エラーにはスクリプトの先頭行が出る。
再現と切り分けはこう:

```bash
idev provision --list          # ステップ番号を確認
idev exec -- sh -c '<失敗したコマンド>'   # 手で叩いてみる
idev provision --step 3        # そのステップだけ流し直す
```

## workspace / 権限

### `workspace idmap (raw.idmap) is not permitted on this host`

`workspace.idmap: raw` を明示したが、ホストが許可していない。
エラーが追加すべき行を示す（`/etc/subuid`・`/etc/subgid` の `root:<uid>:1`）。
追加後、Incusの再起動は不要。ホスト設定を変えたくなければ
`workspace.idmap: shift` にする（コンテナが作ったファイルはホスト側でroot所有）。

### コンテナ内で `/workspace` へ書けない、生成物がroot所有になる

`workspace.idmap` の選択の問題。既定の `auto` は `raw` が使えなければ
`shift` へ退避する。`shift` ではホスト側の所有者がrootになる。

### 変更が反映されない

- `provision` を変えた → `idev provision`
- `instance.config` / `devices` / `volumes` を変えた → **`idev up`**
- 反映に再起動が要る設定 → `idev up --restart`

## Incusへ接続できない

```text
connect to the local incus: The incus daemon doesn't appear to be started
```

`idev` は `incus` コマンドと同じ設定（`~/.config/incus/config.yml`）を読む。
`incus info` が通るかをまず確認する。操作対象は常に `local` remote であり、
`incus remote switch` の既定には従わない。remoteのIncusは対象外である。
