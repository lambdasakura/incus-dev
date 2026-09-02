# 4. CLI仕様

実行コマンド名：

```text
idev
```

初期実装では以下を提供する。

```text
idev up
idev provision
idev shell
idev exec
idev status
idev destroy
idev rebuild
idev validate
idev snapshot
```

## 4.0 共通フラグ

すべてのコマンドで以下を受け付ける。

| フラグ | 既定 | 説明 |
| --- | --- | --- |
| `-v`, `--verbose` | | 実行した外部コマンドなどを出力する |
| `-C`, `--directory` | カレントディレクトリ | プロジェクト探索の起点 |
| `--incus-project` | `dev.yml` の `incus.project`、無ければ `default` | Incus project |

`--incus-project` は操作層へそのまま渡す（[05-incus.md](05-incus.md) 5.5）。

remoteを選ぶフラグは持たない。操作対象は常にローカルのIncusである
（[05-incus.md](05-incus.md) 5.6）。

---

## 4.1 `idev up`

```bash
idev up
```

以下を実行する。

1. project rootを検出
2. `.incus-dev/dev.yml` を読み込む
3. schema validation
4. runtime compatibility validation
5. instance名を決定
6. 参照Profileの存在確認（存在しなければ失敗）
7. instance存在確認
8. 存在しなければ作成（image / profiles）
9. instance config・devices・workspace mountを適用
10. idev管理情報 (`user.incus-dev.*`) を設定
11. instance起動
12. instance ready待ち
13. bootstrap実行
14. provisionステップを順に実行
15. 完了状態を表示

### 制約

- すでにinstanceが存在する場合は破壊してはならない
- 既存instanceがidev管理下でない場合は明示的に失敗する
- 既存instanceに対しても、`dev.yml` の宣言内容は再適用する
  （[05-incus.md](05-incus.md) 参照）
- 宣言から消えた設定・deviceは、idevが適用したものに限り取り消す

```bash
idev up --restart
```

反映に再起動が必要な変更があれば、instanceを再起動する。
既定では警告のみを表示する（作業中のプロセスを予期せず止めないため）。

---

## 4.2 `idev provision`

```bash
idev provision
```

Incus instanceを再作成せず、bootstrapとprovisionステップのみ再実行する。

主な用途：

- `dev.yml` の `provision` 更新
- playbook / スクリプトの変更
- 依存パッケージの追加

### 制約

- 実行対象instanceが存在しない場合は明示的なエラーとする
- 暗黙的に `idev up` へ切り替えてはならない
- instance config / devices の変更は行わない（それは `idev up` の責務）

### 部分実行

ステップ数が増えると全体再実行が重くなるため、一部だけを実行できる。

```bash
idev provision --list             # ステップの一覧（Incusへは触れない）
idev provision --step <name>      # 指定ステップのみ実行（複数指定可）
idev provision --from <name>      # 指定ステップ以降を実行
```

- ステップは名前または番号（1始まり）で指定する
- 複数一致する名前は、すべて実行する
- 指定順ではなく **宣言順** で実行する（前から順に積み上げる前提のため）
- 解決できない指定は、instanceへ触れる前に失敗し、選べるステップを示す
- ログとエラーには全体の中での位置を表示する（`step 2/3`）
- bootstrapは省略しない。provisionerを動かすための準備であり、
  軽量かつ冪等であることを前提としているため

---

## 4.3 `idev shell`

```bash
idev shell
```

対象コンテナへinteractive shellを開く。

- 標準入出力が端末に接続されている場合のみTTYを割り当てる
  （端末でない場合に割り当てると出力へCRが混入する。[05-incus.md](05-incus.md) 5.7.2）
- 実行ユーザー・シェル・作業ディレクトリは `dev.yml` の `shell` で指定する
  （[03-configuration.md](03-configuration.md) 3.14）。既定はinstanceの既定ユーザー、
  `/bin/sh`、`workspace.target`

引数を与えた場合は、そのコマンドを実行して終了する。

```bash
idev shell -- make test
```

---

## 4.3.1 `idev exec`

```bash
idev exec -- make test
```

コンテナ内でコマンドを実行する。**TTYは割り当てない。**

`idev shell` と違い、端末に接続されていても擬似端末を割り当てない。
CIやスクリプトから呼ぶ場合に、端末の有無で挙動が変わらないようにするためである。

実行ユーザー・作業ディレクトリの扱いは `idev shell` と同じ（`shell` の指定に従う）。
コマンドの指定は必須とし、省略された場合は `idev shell` を使うよう促して失敗する。

---

## 4.4 `idev status`

```bash
idev status
```

最低限以下を表示する。

```text
Project:    example-project
Instance:   dev-example-project
Status:     Running
Image:      images:ubuntu/24.04
Workspace:  /home/user/src/example-project -> /workspace
```

可能であれば以下も表示する。

- Profiles
- 主要なconfig（`limits.cpu`, `limits.memory` など）
- devices
- provisionステップ数
- Incus project
- runtime version
- idev管理下かどうか

`Image` と `Workspace` はinstanceが実際に持っている値を表示する
（`user.incus-dev.image` と workspace deviceの `source`）。
`dev.yml` が別のものを宣言している場合は併記する。`up` が作り直さない以上、
宣言の方を表示すると、実態と違うものを「現在の状態」として見せることになる。

これらは実装済みである。

---

## 4.5 `idev destroy`

```bash
idev destroy
```

対象プロジェクトのIncus instanceを削除する。

### 制約

- ソースコードはbind mountされたホスト側ディレクトリなので削除してはならない
- idev管理下でないinstanceは削除してはならない
- 破壊操作のため、既定では確認を求める
- **永続ボリューム（`volumes`）は削除しない**

```bash
idev destroy --force      # 確認を省略
idev destroy --volumes    # 永続ボリュームも削除する
```

永続ボリュームはinstanceを作り直しても残すためのものなので、
削除は明示的な指示があった場合のみ行う。

---

## 4.6 `idev rebuild`

```bash
idev rebuild
```

概念的には、

```text
idev destroy
idev up
```

を実行する。

既存instance内の状態は破棄されるため、実行前に確認を求める。

非interactive用途のため、以下をサポートする。

```bash
idev rebuild --force
```

`dev.yml` から削除した設定を確実に消したい場合の正規手段でもある
（[05-incus.md](05-incus.md) 5.4.4 参照）。

---

## 4.6.1 `idev snapshot`

instanceのスナップショットを操作する。

```bash
idev snapshot create [name]     # 名前を省略すると日時（20060102-150405）
idev snapshot list
idev snapshot restore <name>    # 破壊的。確認を求める（--force で省略）
idev snapshot delete <name>     # 同上
```

- 対象はidev管理下のinstanceに限る
- 復元してもホスト側のworkspaceには影響しない
  （bind mountであり、instanceの状態ではないため）

`create` は名前を検査する。Incus自身の制約（`/` と空白を含まない）に加え、
`.` と `..` を拒否する。ストレージドライバによっては、これらの名前で作った
snapshotが残り、**instanceごと削除できなくなる**ため。

`restore` / `delete` は検査しない。idev以外が作ったsnapshotへ到達できなくなる。

---

## 4.7 `idev validate`

```bash
idev validate
```

以下だけを確認し、Incusへ一切変更を加えない。

- YAML syntax
- schema version
- JSON Schemaへの適合
- runtime version互換性
- 必須フィールドの存在
- provisionステップの構造
  - `run` と `ansible` が排他であること
  - bootstrapに `ansible` ステップが無いこと
- 参照パスの存在
  - `ansible.playbook` / `vars` / `inventory`
  - `workspace.source`
  - 相対パスで指定されたdeviceの `source`
- Profile名の構文
- `profiles: []` の場合に root disk device が宣言されていること
- `instance.config` / `instance.devices` のキーが `-` で始まらないこと
  （Incusが受け付けないキー形式であり、誤記を早期に知らせるため）
- `instance.config` の値がスカラであること
- `user.incus-dev.*` を使用していないこと

上記は代表例であり、網羅ではない。`internal/config` の検査が正であり、
そこにはこのほか、参照パスの存在、コンテナ内パスが絶対であること、
キーが `=` や `,` を含まないこと、`-` で始まる値、`env` と `file` の排他、
`IDEV_` プレフィックスの予約、ステップ種別ごとに許されるフィールドなどが
含まれる。

CIから実行可能なこと。Incusが無い環境でも実行可能とする。

Incus daemonへの問い合わせ（Profileの実在確認など）は行わない。

ホスト側の前提（Incusへ到達できるか、Profileが実在するか、`/etc/subuid`、
`ansible-playbook` の有無、secretsが解決できるか）まで検査するオプションは
持たない。`idev up --dry-run` がIncusへ変更を加えずに同じ検査を行うため
（[4.8](#48-dry-run)）、別のフラグを設ける理由が無い。

---

## 4.8 Dry Run

```bash
idev up --dry-run
```

実際に変更せず、実行予定の操作を表示する。

```text
Create instance dev-example-project (images:ubuntu/24.04)
Apply profiles: default
Set config limits.cpu=8
Set config limits.memory=16GiB
Add device workspace (disk /home/user/src/example-project -> /workspace)
Start instance
Bootstrap: 1 step (default)
Provision step 1/2: prepare (run)
Provision step 2/2: main playbook (ansible .incus-dev/ansible/site.yml)
```

- ホスト側の前提（idmap、Profileの存在、管理下のinstanceか）は
  `idev up` と同じように確認する。preflightとして使えるようにするため
- Incusへの読み取りは行うが、変更は一切行わない
- 既存instanceに対しては、現状との差分だけを表示する
- idevの管理用キー（`user.incus-dev.*`）は常に設定されるため
  1行にまとめる

実行予定の算出は、実際に適用する経路と同じ関数
（`desiredConfig` / `desiredDevices` / `staleConfigKeys` / `staleDevices`）を使う。
表示と実際の適用がずれないようにするため。

---

## 4.9 Logging

通常モード：

```bash
idev up
```

では、人間が読みやすい簡潔な出力とする。

例：

```text
[idev] Project: example-project
[idev] Creating instance dev-example-project
[idev] Mounting workspace /home/user/src/example-project -> /workspace
[idev] Starting instance dev-example-project
[idev] bootstrap step 1/1 in dev-example-project: default
[idev] provision step 1/2 in dev-example-project: prepare
[idev] provision step 2/2 in dev-example-project: main playbook
[idev] Development environment is ready
```

警告が出た実行では、末尾を
`Development environment is ready, with N warning(s) above` とする。
成功で終わった旨だけを最後に出すと、その上の警告が読まれないためである。

ステップ実行中の出力は、そのまま標準エラーへ中継する。

長時間かかる処理（パッケージの導入など）で進行が見えなくなることを避けるため、
要約や抑制は行わない。`--verbose` では、これに加えて実行した外部コマンドと
Incusへの操作を出力する。

Incus操作のログには **操作名と対象（instance名・キー名）のみ** を出す。
configやenvの値はSecretを含みうるため出さない。

詳細確認用に以下を提供する。

```bash
idev up --verbose
idev -v up
```

実装には標準ライブラリの `log/slog` を用いる。

---

## 4.10 Error Handling

すべての外部コマンド失敗を検出する。

対象：

```text
ansible-playbook / ansible-galaxy / ansible-doc
git
Incus API の呼び出し
コンテナ内で実行した run ステップ
```

失敗時には最低限、以下の **情報** を含めること。整形は問わない。

| 項目 | 例 |
| --- | --- |
| 操作 | `provision step 2/3` |
| 対象instance | `dev-example-project` |
| ステップ | `main playbook (ansible)` |
| 実行コマンド | `ansible-playbook ...` |
| 終了コード | `2` |
| エラー内容 | コマンドのstderr |

実装は1行で連結して出す。

```text
[idev] error: provision step 2/3 in dev-example-project: main playbook (ansible):
command failed: ansible-playbook ... (exit code 2)
<stderr>
```

ステップ実行の失敗では、どのステップが失敗したかを必ず特定できること。
対象instance名を含めるのは、`project.scope` が `path` / `branch` の場合に
instance名が導出されるため、どの環境で失敗したかが自明でないからである。

ただしSecretを含む可能性のある引数や環境変数を無条件で出力してはならない。

---

## 4.11 Exit Code

AIツールやCIが判定できるよう、終了コードを正しく返す。

```text
0 = success
non-zero = failure
```

`main` 関数のみが `os.Exit` を呼び、それ以外の層は `error` を返す。

可能なら将来的にエラー種別別exit codeを導入してよいが、初期段階では不要。

---

## 4.12 Output stability

通常出力は人間向けでよい。

ただし、標準出力と標準エラー出力の役割は分ける。

- **標準出力**はコマンドの結果だけを載せる。
  `status` / `validate` / `provision --list` / `snapshot list` / `up --dry-run` の出力と、
  `shell` / `exec` が中継するコンテナ内プロセスの標準出力がこれにあたる
- ログ、警告、エラー、確認プロンプトは**標準エラー出力**へ出す
- provisioningステップ（`run` / `ansible` / `galaxy`）の出力も**標準エラー出力**へ出す。
  `up` / `provision` / `rebuild` の結果は環境そのものであって、途中の出力ではない。
  `exec` は逆で、コマンドの出力こそが結果である

結果が1行1件の形式（`provision --list` と `snapshot list`）である場合、
0件は「0行」で表す。「該当なし」という文をそこへ書かない。
`wc -l` が1を返し、`cut` がその文をデータとして拾ってしまうためである。
0件であること自体は標準エラー出力へ出す。

同じ理由で、行に埋め込まれる値には制御文字を許さない
（`provision[].name` — [03-configuration.md](03-configuration.md) 参照）。

将来的なAIやツール連携のため、

```bash
idev status --json
```

のようなmachine-readable outputを追加可能な構造にする。

MVPで `--json` を実装する場合、最低限以下を返す。

```json
{
  "project": "example-project",
  "instance": "dev-example-project",
  "status": "Running",
  "workspace": "/workspace"
}
```

---

## 4.13 AI開発ツールとの利用

本システムはCodexおよびClaude Codeから操作されることを想定する。

AIエージェントは原則として、

```bash
idev up
idev provision
idev shell
```

を使用する。

AIエージェントがIncus内部実装を知らなくても利用できることを目標とする。

環境を変更したい場合、AIエージェントが編集すべき対象は
`.incus-dev/` 配下のファイルのみである。この一貫性がAI利用時の
理解しやすさに直結する。

---

## 4.14 AI向けコマンド設計

CLIはinteractive promptを必要最小限にする。

特に以下は非interactiveで実行可能であること。

```bash
idev up
idev provision
idev status
idev validate
```

破壊操作のみ確認を入れる。

```bash
idev destroy
idev rebuild
```

非interactive用途として、以下を提供する。

```bash
idev destroy --force
idev rebuild --force
```

標準入力から一文字も読めなかった場合は「拒否された」とせず、
`--force` を案内するエラーで終了する。CIやhookから駆動したときに
「nと答えた」と区別できないと、原因の切り分けができないため。
