# 7. 実装ガイドライン（Go）

## 7.1 パッケージ構成の原則

責務を以下の層に分離し、依存方向を一方向に保つ。

```text
cmd/dev
   │
   ▼
internal/cli          コマンド定義・入出力
   │
   ├─▶ internal/project    project root 探索
   ├─▶ internal/config     dev.yml の解釈
   ├─▶ internal/incus      Incus操作
   └─▶ internal/ansible    Ansible操作
              │
              ▼
       internal/runner     外部コマンド実行
```

- `internal/config` からIncus操作やAnsible実行を行ってはならない
- `internal/incus`、`internal/ansible` はCLIの出力形式を知らない
- 外部コマンド実行は `internal/runner` に集約する

---

## 7.2 外部コマンド実行

外部コマンド呼び出しは `internal/runner` へ集約する。

各パッケージで `os/exec` を直接乱用しない。

共通wrapperで以下を管理する。

- logging
- stdout / stderr
- timeout（`context.Context`）
- exit code
- エラー生成
- dry-run対応

例：

```go
package runner

type Runner interface {
    Run(ctx context.Context, c Command) (Result, error)
}

type Command struct {
    Name  string
    Args  []string
    Dir   string
    Env   []string
    Stdin io.Reader
    // 端末を引き継ぐ対話実行（dev shell）用
    Interactive bool
}

type Result struct {
    ExitCode int
    Stdout   []byte
    Stderr   []byte
}
```

実装上の要点：

- キャンセル・タイムアウトのため `exec.CommandContext` を使用する
- 失敗時は `exec.ExitError` から exit code を取得し、
  操作名・対象・コマンド・exit code・stderr を含むエラーを構築する
- ログ出力時、Secretを含みうる引数・環境変数はマスクする
- dry-runモードでは実行せず、実行予定コマンドを記録する
- 対話実行（`dev shell`）では `os.Stdin` / `os.Stdout` / `os.Stderr` を直接引き継ぐ

エラーは `fmt.Errorf("...: %w", err)` でラップし、
呼び出し側は `errors.Is` / `errors.As` で判別する。

---

## 7.3 Configuration Parser

`internal/config` は、

```text
.incus-dev/dev.yml
```

の読み込みとvalidationのみを担当する。

Incus操作やAnsible実行をここから行ってはならない。

設定は型付き構造体へ変換する。

```go
type Config struct {
    Schema    int        `json:"schema"`
    Runtime   Runtime    `json:"runtime"`
    Project   Project    `json:"project"`
    Instance  Instance   `json:"instance"`
    Workspace Workspace  `json:"workspace"`
    Packages  []string   `json:"packages"`
    Features  map[string]map[string]any `json:"features"`
    Provision *Provision `json:"provision,omitempty"`
}
```

### 7.3.1 YAMLの扱い

YAMLはJSONへ変換したうえでデコードする方式を推奨する。

```text
sigs.k8s.io/yaml
```

これにより以下が両立する。

- 構造体タグを `json` に一本化できる
- 同じJSON表現に対してJSON Schema validationを適用できる

### 7.3.2 Validation

二段構えとする。

1. JSON Schema (`schemas/dev-v1.schema.json`) による構造検証
   - 例: `github.com/santhosh-tekuri/jsonschema`
2. Goコードによる意味検証
   - schema versionの既知性
   - runtime versionの互換性
   - Feature名が既知のRoleに対応するか
   - パスの解決可否

JSON Schemaとコード側validationの責務重複を過度に複雑化しない。
「形」はSchema、「意味」はコード、という分担を基本とする。

### 7.3.3 未知フィールド

未知のフィールドは原則としてエラーとする（JSON Schemaの
`additionalProperties: false` で表現する）。

タイプミスによる設定の無視を防ぐため。

---

## 7.4 Project discovery

`internal/project` は現在ディレクトリから親方向へ探索し、

```text
.incus-dev/dev.yml
```

を探す。

例：

```text
project/
├── .incus-dev/dev.yml
└── src/
    └── foo/
        └── bar/
```

で、

```bash
cd src/foo/bar
dev status
```

を実行した場合でもproject rootを検出できること。

### 7.4.1 Gitへの依存

project root検出にGitを利用してもよい。

ただし、

```text
.incus-dev/dev.yml
```

が存在する非Git directoryでも動作可能な設計とする。

優先順位：

1. `.incus-dev/dev.yml` 探索
2. Git情報

とする。

探索はファイルシステムrootまで、または上限段数で打ち切る。

---

## 7.5 ホスト側Git repositoryの保護

dev CLIは原則としてGit working treeを変更してはならない。

以下は禁止する。

- 自動commit
- 自動checkout
- 自動reset
- 自動clean
- ソースコードの削除

`.incus-dev` 内のファイルを更新する操作も、明示的コマンドなしでは行わない。

---

## 7.6 依存ライブラリ方針

外部依存は最小限に保つ。採用候補：

| 用途 | 候補 |
| --- | --- |
| CLI | `github.com/spf13/cobra` |
| YAML | `sigs.k8s.io/yaml` |
| JSON Schema | `github.com/santhosh-tekuri/jsonschema` |
| ログ | 標準 `log/slog` |
| テスト | 標準 `testing`（必要なら `github.com/google/go-cmp`） |
| Incus API（将来） | `github.com/lxc/incus/client` |

標準ライブラリで十分な領域に依存を追加しない。

`CGO_ENABLED=0` でビルドできない依存を導入しない。

---

## 7.7 コーディング規約

- `gofmt` / `go vet` を通すこと（CIで検証する）
- `golangci-lint` の導入を推奨する
- 小さな関数を使用する
- 外部プロセス、ファイルシステム、時刻に触れる箇所はinterface化し、
  テストで差し替え可能にする
- panicを制御フローに使わない。エラーは戻り値で伝播する
- `context.Context` を外部プロセス実行を伴う関数の第一引数に取る

---

## 7.8 AIエージェントによる開発時の原則

本ツール自体をAIエージェント（Codex / Claude Code）で開発する場合、以下を守る。

### MUST

- 本仕様を実装判断の基準とする。
- 不必要な機能を追加しない。
- CLI / Incus / Ansible / Configurationの責務を分離する。
- 外部コマンド失敗を無視しない。
- Ansible Roleを冪等にする。
- `dev provision` で既存instanceを破壊しない。
- `dev destroy` でhost側source treeを削除しない。
- Git repositoryへSecretを書き込まない。
- 実装変更には可能な限りテストを追加する。
- `gofmt` および `go vet` を通す。

### SHOULD

- 小さな関数を使用する。
- 外部コマンド実行を `internal/runner` へ集中管理する。
- shell scriptへの依存を最小化する。
- configを型付き内部表現へ変換する。
- CLI behaviorをintegration testする。
- Incusが存在しない環境でもunit testを実行可能にする。
