# 8. テスト

## 8.1 Unit Test

Incus daemonを必要としないテストを充実させる。

最低限：

```text
Configuration parsing
Schema validation
Project root detection
Instance naming
Command construction
Feature mapping
Error handling
```

実装方針：

- 標準の `testing` を使用し、table-driven testを基本とする
- Incus / Ansible操作層はinterfaceとして定義し、fake実装で差し替える
  （[05-incus.md](05-incus.md)、[06-ansible.md](06-ansible.md) 参照）
- 外部コマンド実行は `internal/runner` のinterfaceを差し替え、
  「どのコマンドが構築されたか」を検証する
- project discoveryは `t.TempDir()` に一時ツリーを作って検証する
- `dev.yml` のパースは `internal/config/testdata/` の
  正常系・異常系ファイルで検証する

```bash
go test ./...
```

がIncusの無い環境で成功すること。

---

## 8.2 Integration Test

Incusを利用可能な環境では最低限以下を検証する。

```text
dev up
  ↓
container RUNNING

/workspace
  ↓
host repositoryが見える

python feature
  ↓
pythonが利用可能

dev provision
  ↓
再実行成功

dev destroy
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

- テスト用instance名は衝突しないよう一意化し、
  `t.Cleanup` で必ず削除する
- CIではIncusが利用可能なジョブでのみ実行する

---

## 8.3 CI

最低限以下を実行する。

```bash
gofmt -l .
go vet ./...
go test ./...
dev validate       # サンプルプロジェクトに対して
ansible-lint       # Role更新時
```
