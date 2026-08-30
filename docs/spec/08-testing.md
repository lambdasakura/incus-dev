# 8. テスト

## 8.1 Unit Test

Incus daemonを必要としないテストを充実させる。

最低限：

```text
Configuration parsing
  - 最小構成 / 完全構成
  - run 短縮形と完全形
  - run と ansible の排他性
  - 省略と明示的な空の区別（profiles / bootstrap）
  - スカラ値の文字列化（数値・真偽値）
Schema validation
Runtime version 互換性判定
Project root detection
Instance naming / 正規化
Bootstrap 既定動作の選択
  - ansible ステップの有無による分岐
  - bootstrap 明示時の上書き
Step planning（実行順序、dry-run出力）
Command construction
  - incus exec の引数
  - ansible-playbook の引数
Inventory / extra-vars 生成
Error handling（失敗ステップの特定）
```

実装方針：

- 標準の `testing` を使用し、table-driven testを基本とする
- Incus操作層はinterfaceとして定義し、fake実装で差し替える
  （[05-incus.md](05-incus.md) 参照）
- 外部コマンド実行は `internal/runner` のinterfaceを差し替え、
  「どのコマンドが構築されたか」を検証する
- project discoveryは `t.TempDir()` に一時ツリーを作って検証する
- `dev.yml` のパースは `internal/config/testdata/` の
  正常系・異常系ファイルで検証する
- 生成されるinventoryやextra-varsはgolden fileで比較する

```bash
go test ./...
```

がIncusの無い環境で成功すること。

---

## 8.2 構造の検証

REQ-007（devkitが環境固有の資産を持たないこと）は仕様の中核であり、
テストで機械的に検証する。

```text
devkitリポジトリに以下が存在しないこと

  ansible/roles/
  profiles/*.yaml
  requirements.yml

embedされるアセットが schemas/ のみであること
```

`examples/` 配下がバイナリへ同梱されていないことも確認する。

---

## 8.3 Integration Test

Incusを利用可能な環境では最低限以下を検証する。

```text
idk up
  ↓
container RUNNING

/workspace
  ↓
host repositoryが見える
（コンテナ内から .incus-dev/dev.yml が読める）

run ステップ
  ↓
コンテナ内で実行され、結果が反映される

ansible ステップ
  ↓
SSHなしで playbook が適用される

idk provision
  ↓
再実行成功（ステップが冪等なテストプロジェクトを用いる）

idk up（dev.yml のリソース変更後）
  ↓
instance を破壊せず設定が反映される

idk destroy
  ↓
instance削除

host repository
  ↓
残存
```

実装方針：

- `test/integration/` へ配置し、build tagで分離する

```go
//go:build integration
```

```bash
go test -tags integration ./test/integration/...
```

- テスト用フィクスチャは `.incus-dev/` 一式を持つ最小プロジェクトとする
- テスト用instance名は衝突しないよう一意化し、`t.Cleanup` で必ず削除する
- CIではIncusが利用可能なジョブでのみ実行する

---

## 8.4 CI

最低限以下を実行する。

```bash
gofmt -l .
go vet ./...
go test ./...
idk validate           # examples/ 配下の各サンプルに対して
```

`examples/` の各サンプルが常に `idk validate` を通ることを保証する。
