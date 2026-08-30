# 4. CLI仕様

実行コマンド名：

```text
dev
```

初期実装では以下を提供する。

```text
dev up
dev provision
dev shell
dev status
dev destroy
dev rebuild
dev validate
```

---

## 4.1 `dev up`

```bash
dev up
```

以下を実行する。

1. Git project rootを検出
2. `.incus-dev/dev.yml` を読み込む
3. schema validation
4. runtime compatibility validation
5. instance名を決定
6. instance存在確認
7. 存在しなければinstance作成
8. Incus Profile適用
9. CPU / memory等を設定
10. workspace mountを作成
11. instance起動
12. instance ready待ち
13. Ansible bootstrap
14. 共通Ansible provisioning
15. project-specific provisioning
16. 完了状態を表示

すでにinstanceが存在する場合は破壊してはならない。

必要に応じて既存instanceを起動し、provisionを再実行する。

---

## 4.2 `dev provision`

```bash
dev provision
```

Incus instanceを再作成せず、Ansibleのみ再適用する。

主な用途：

- `dev.yml` 更新
- Role更新
- package追加
- project.yml変更

実行対象instanceが存在しない場合は明示的なエラーとする。

暗黙的に `dev up` へ切り替えてはならない。

---

## 4.3 `dev shell`

```bash
dev shell
```

対象コンテナへinteractive shellを開く。

初期実装ではroot shellでも構わない。

将来的には、

```yaml
user:
  name: developer
```

などを定義し、一般ユーザーでshellを開始する機能を追加できる構造とする。

実装上は端末制御を伴うため、`incus exec -t` をそのままforegroundで実行し、
標準入出力を引き継ぐ方式を基本とする（[07-implementation.md](07-implementation.md) 参照）。

---

## 4.4 `dev status`

```bash
dev status
```

最低限以下を表示する。

```text
Project:    example-project
Instance:   dev-example-project
Status:     RUNNING
Image:      images:ubuntu/24.04
Workspace:  /workspace
```

可能であれば以下も表示する。

- CPU
- Memory
- Profile
- Incus remote
- Incus project
- runtime version

---

## 4.5 `dev destroy`

```bash
dev destroy
```

対象プロジェクトのIncus instanceを削除する。

ソースコードはbind mountされたホスト側ディレクトリなので削除してはならない。

将来的にpersistent volumeを追加する場合、削除ポリシーを明示的に管理する。

---

## 4.6 `dev rebuild`

```bash
dev rebuild
```

概念的には、

```text
dev destroy
dev up
```

を実行する。

既存instance内の状態は破棄されるため、実行前に確認を求めてもよい。

非interactive用途のため、

```bash
dev rebuild --force
```

などをサポートすることを推奨する。

---

## 4.7 `dev validate`

```bash
dev validate
```

以下だけを確認し、Incus instanceへ変更を加えない。

- YAML syntax
- schema
- runtime version
- required fields
- Feature existence
- Profile name syntax
- filesystem path

CIから実行可能なこと。

---

## 4.8 Dry Run

可能であれば、

```bash
dev up --dry-run
```

をサポートする。

実際に変更せず、

```text
Create instance
Set CPU=8
Set Memory=16GiB
Mount /foo/bar -> /workspace
Apply profile dev-base
Run Ansible feature python
Run Ansible feature docker
```

などを確認可能にする。

初期MVPではoptionalとする。

---

## 4.9 Logging

通常モード：

```bash
dev up
```

では、人間が読みやすい簡潔な出力とする。

例：

```text
[dev] Project: example-project
[dev] Creating instance dev-example-project
[dev] Mounting workspace
[dev] Starting instance
[dev] Bootstrapping Python
[dev] Running Ansible provisioning
[dev] Development environment is ready
```

詳細確認用に、

```bash
dev up --verbose
```

または、

```bash
dev -v up
```

を提供することを推奨する。

実装には標準ライブラリの `log/slog` を用い、
通常モードは上記形式のハンドラ、`--verbose` ではdebugレベルを出力する。

---

## 4.10 Error Handling

すべての外部コマンド失敗を検出する。

対象：

```text
incus
ansible-playbook
git
```

失敗時には最低限以下を表示する。

```text
Operation
Target
Command
Exit code
Error message
```

ただしSecretを含む可能性のある引数や環境変数を無条件で出力してはならない。

---

## 4.11 Exit Code

AIツールやCIが判定できるよう、終了コードを正しく返す。

```text
0 = success
non-zero = failure
```

`main` 関数のみが `os.Exit` を呼び、それ以外の層は
`error` を返して呼び出し元へ伝播させる。

可能なら将来的にエラー種別別exit codeを導入してよいが、初期段階では不要。

---

## 4.12 Output stability

通常出力は人間向けでよい。

将来的なAIやツール連携のため、

```bash
dev status --json
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
dev up
dev provision
dev shell
```

を使用する。

AIエージェントがIncus内部実装を知らなくても利用できることを目標とする。

---

## 4.14 AI向けコマンド設計

CLIはinteractive promptを必要最小限にする。

特に以下は非interactiveで実行可能であること。

```bash
dev up
dev provision
dev status
dev validate
```

破壊操作のみ確認を入れてよい。

```bash
dev destroy
dev rebuild
```

非interactive用途として、

```bash
dev destroy --force
dev rebuild --force
```

を提供する。
