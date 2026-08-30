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
- `dev.yml` のパースは、正常系・異常系のYAMLをテスト内に直接書いて検証する
  （入力と期待結果が同じ場所にある方が読みやすいため）
- 生成されるinventoryやextra-varsは、一時ファイルの内容を読んで検証する

```bash
go test ./...
```

がIncusの無い環境で成功すること。

### カバレッジ

```bash
make cover
```

パッケージ横断で測る（`-coverpkg=./...`）。テスト用のfakeは他パッケージの
テストからのみ実行されるため、これを付けないと0%として集計される。

**目標は、到達可能な分岐をすべて網羅すること。**

以下のような、不変条件により到達しない防御的分岐は残してよい。
カバレッジのために検査を削ったり、テスト専用の注入点を実装へ埋め込んだりしない。

- 同梱JSON Schemaが壊れている場合の分岐（ビルド成果物の不具合）
- JSON Schema検証を通過した値の構造体デコード失敗
- 文字列だけを含むmapのmarshal失敗
- `os.Getwd` の失敗

これらは「起きたとき何が起きるか」を定義するために必要であり、
到達可能性とは別の価値を持つ。

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
idev up
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

idev provision
  ↓
再実行成功（ステップが冪等なテストプロジェクトを用いる）

idev up（dev.yml のリソース変更後）
  ↓
instance を破壊せず設定が反映される

idev destroy
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
idev validate           # examples/ 配下の各サンプルに対して
```

`examples/` の各サンプルが常に `idev validate` を通ることを保証する。
