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
idev status
idev destroy
idev rebuild
idev validate
```

## 4.0 共通フラグ

すべてのコマンドで以下を受け付ける。

| フラグ | 既定 | 説明 |
| --- | --- | --- |
| `-v`, `--verbose` | | 実行した外部コマンドなどを出力する |
| `-C`, `--directory` | カレントディレクトリ | プロジェクト探索の起点 |
| `--incus-remote` | `local` | Incus remote |
| `--incus-project` | `default` | Incus project |

`--incus-remote` / `--incus-project` は操作層へそのまま渡す
（[05-incus.md](05-incus.md) 5.5、5.6）。remoteを指定した場合、
workspaceのbind mountはホスト側パスを前提とするため成立しない点に注意する。

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
10. devkit管理情報 (`user.incus-devkit.*`) を設定
11. instance起動
12. instance ready待ち
13. bootstrap実行
14. provisionステップを順に実行
15. 完了状態を表示

### 制約

- すでにinstanceが存在する場合は破壊してはならない
- 既存instanceがdevkit管理下でない場合は明示的に失敗する
- 既存instanceに対しても、`dev.yml` の宣言内容は再適用する
  （[05-incus.md](05-incus.md) 参照）

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

ステップ数が増えると全体再実行が重くなるため、以下を提供することを推奨する。

```bash
idev provision --step <name>      # 特定ステップのみ実行
idev provision --from <name>      # 指定ステップ以降を実行
```

MVPではoptionalとする。

---

## 4.3 `idev shell`

```bash
idev shell
```

対象コンテナへinteractive shellを開く。

- TTYを割り当て、標準入出力を引き継ぐ
- 作業ディレクトリの既定は `workspace.target`
- 初期実装ではroot shellでよい

将来的には、

```yaml
shell:
  user: developer
  command: /bin/bash
  cwd: /workspace
```

を `dev.yml` で指定できる構造とする。

引数を与えた場合は、そのコマンドを実行して終了する形も検討する。

```bash
idev shell -- make test
```

---

## 4.4 `idev status`

```bash
idev status
```

最低限以下を表示する。

```text
Project:    example-project
Instance:   dev-example-project
Status:     RUNNING
Image:      images:ubuntu/24.04
Workspace:  /home/user/src/example-project -> /workspace
```

可能であれば以下も表示する。

- Profiles
- 主要なconfig（`limits.cpu`, `limits.memory` など）
- devices
- provisionステップ数
- Incus remote / project
- runtime version
- devkit管理下かどうか

---

## 4.5 `idev destroy`

```bash
idev destroy
```

対象プロジェクトのIncus instanceを削除する。

### 制約

- ソースコードはbind mountされたホスト側ディレクトリなので削除してはならない
- devkit管理下でないinstanceは削除してはならない
- 破壊操作のため、既定では確認を求める

```bash
idev destroy --force
```

将来的にpersistent volumeを追加する場合、削除ポリシーを明示的に管理する。

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
  （incusコマンドのフラグとして解釈されうるため）
- `instance.config` の値がスカラであること
- `user.incus-devkit.*` を使用していないこと

CIから実行可能なこと。Incusが無い環境でも実行可能とする。

Incus daemonへの問い合わせ（Profileの実在確認など）は行わない。

ホスト側の状態まで検査するオプション（`--check-host` など）は将来の候補とする
（[09-roadmap.md](09-roadmap.md) 9.2）。

---

## 4.8 Dry Run

可能であれば、

```bash
idev up --dry-run
```

をサポートする。

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

初期MVPではoptionalとする。

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
[idev] Starting instance
[idev] Bootstrap (default)
[idev] Step 1/2: prepare
[idev] Step 2/2: main playbook
[idev] Development environment is ready
```

ステップ実行中の出力は、そのまま標準エラーへ中継する。

長時間かかる処理（パッケージの導入など）で進行が見えなくなることを避けるため、
要約や抑制は行わない。`--verbose` では、これに加えて実行した外部コマンドを出力する。

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
incus
ansible-playbook
git
コンテナ内で実行した run ステップ
```

失敗時には最低限以下を表示する。

```text
Operation        provision step 2/3
Target           dev-example-project
Step             main playbook (ansible)
Command          ansible-playbook ...
Exit code        2
Error message    ...
```

ステップ実行の失敗では、どのステップが失敗したかを必ず特定できること。

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
  "status": "RUNNING",
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
