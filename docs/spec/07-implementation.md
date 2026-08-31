# 7. 実装ガイドライン（Go）

## 7.1 パッケージ構成の原則

責務を以下の層に分離し、依存方向を一方向に保つ。

```text
cmd/idev
   │
   ▼
internal/cli            コマンド定義・入出力
   │
   ├─▶ internal/project     project root 探索
   ├─▶ internal/config      dev.yml の解釈
   ├─▶ internal/incus       Incus操作
   └─▶ internal/provision   bootstrap / step 実行
              │
              ├─▶ internal/incus     run ステップ
              └─▶ internal/runner    ansible ステップ
```

- `internal/config` からIncus操作やステップ実行を行ってはならない
- `internal/incus`、`internal/provision` はCLIの出力形式を知らない
- 外部コマンド実行は `internal/runner` に集約する
- **どのパッケージにも、特定のOS・言語ランタイム・ツールの知識を持たせない**
  （唯一の例外は 6.3.2 の既定bootstrap）

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
    // 端末を引き継ぐ対話実行（idev shell）用
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
  （indexの指定間違いを防ぐため、`ArgList` で引数の追加と同時に区別を宣言する）
- 対話実行（`idev shell`）では `os.Stdin` / `os.Stdout` / `os.Stderr` を直接引き継ぐ

エラーは `fmt.Errorf("...: %w", err)` でラップし、
呼び出し側は `errors.Is` / `errors.As` で判別する。

### dry-runはこの層に置かない

`internal/runner` は読み取りと変更を区別できない。dry-runでも
`incus list` のような読み取りは実行する必要があるため、
この層で実行を止めると計画そのものを立てられなくなる。

dry-runの継ぎ目は `internal/cli/plan.go` の純粋関数群であり、
`App.Plan` がそれらを使って実行予定を組み立てる
（[04-cli.md](04-cli.md) 4.8）。

---

## 7.3 Configuration Parser

`internal/config` は `.incus-dev/dev.yml` の読み込みとvalidationのみを担当する。

Incus操作やステップ実行をここから行ってはならない。

### 7.3.1 型定義

```go
type Config struct {
    Schema    int        `json:"schema"`
    Runtime   *Runtime   `json:"runtime,omitempty"`
    Project   Project    `json:"project"`
    Instance  Instance   `json:"instance"`
    Workspace *Workspace `json:"workspace,omitempty"`
    Bootstrap *[]Step    `json:"bootstrap,omitempty"`
    Provision []Step     `json:"provision,omitempty"`
}

type Instance struct {
    Image    string                       `json:"image"`
    Type     string                       `json:"type,omitempty"`
    Profiles *[]string                    `json:"profiles,omitempty"`
    Config   map[string]string            `json:"config,omitempty"`
    Devices  map[string]map[string]string `json:"devices,omitempty"`
}
```

`instance.config` / `devices` はIncusへの素通しであるため、
devkit側で意味を持つ型に変換しない。未知キーを拒否しない。

### 7.3.2 「省略」と「明示的な空」の区別

以下の2つは意味が異なるため、ポインタで区別する。

| フィールド | 省略 (`nil`) | 明示的な空 |
| --- | --- | --- |
| `instance.profiles` | `["default"]` を適用 | Profileを適用しない |
| `bootstrap` | 既定bootstrapを実行 | bootstrapを行わない |

### 7.3.3 スカラ値の文字列化

Incusのconfig値は文字列である。

YAML上の `limits.cpu: 8` や `security.nesting: true` を受け付け、
デコード時に文字列へ正規化する。

```go
type Scalar string

func (s *Scalar) UnmarshalJSON(b []byte) error {
    // string / number / bool を受け付け、文字列へ正規化する
    // object / array はエラー
}
```

### 7.3.4 ステップのデコード

`provision[]` は `run` と `ansible` の判別を伴うため、
カスタムデコードを実装する。

```go
type Step struct {
    Name    string
    Run     *RunStep
    Ansible *AnsibleStep
}

func (s *Step) UnmarshalJSON(b []byte) error {
    // 1. run / ansible のキー存在を確認
    // 2. 両方あれば error、両方無ければ error
    // 3. run が文字列の場合は短縮形として RunStep.Script へ展開
}
```

ステップ型の追加が、この関数と `internal/provision` への
`Step` 実装追加だけで完結する構造を保つ。

### 7.3.5 YAMLの扱い

YAMLはJSONへ変換したうえでデコードする方式を推奨する。

```text
sigs.k8s.io/yaml
```

これにより以下が両立する。

- 構造体タグを `json` に一本化できる
- 同じJSON表現に対してJSON Schema validationを適用できる

### 7.3.6 Validation

二段構えとする。

1. JSON Schema (`schemas/dev-v1.schema.json`) による構造検証
   - 例: `github.com/santhosh-tekuri/jsonschema`
   - `instance.config` / `instance.devices` は自由なキーを許可する
   - それ以外のオブジェクトは `additionalProperties: false` とし、
     タイプミスを検出する
2. Goコードによる意味検証
   - schema versionの既知性
   - runtime versionの互換性
   - ステップの排他性（`run` / `ansible`）
   - bootstrapに `ansible` ステップが無いこと
   - 参照パスの存在
   - `user.incus-devkit.*` の使用禁止

「形」はSchema、「意味」はコード、という分担を基本とする。

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
idev status
```

を実行した場合でもproject rootを検出できること。

### 7.4.1 Gitへの依存

project root検出にGitを利用してもよい。

ただし `.incus-dev/dev.yml` が存在する非Git directoryでも動作可能とする。

優先順位：

1. `.incus-dev/dev.yml` 探索
2. Git情報

探索はファイルシステムrootまで、または上限段数で打ち切る。

---

## 7.5 ホスト側Git repositoryの保護

`idev` は原則としてGit working treeを変更してはならない。

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
| Incus API | `github.com/lxc/incus/v6/client` |

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
- 一時ファイル・一時ディレクトリは `defer` で確実に削除する

---

## 7.8 AIエージェントによる開発時の原則

本ツール自体をAIエージェント（Codex / Claude Code）で開発する場合、以下を守る。

### MUST

- 本仕様を実装判断の基準とする。
- 不必要な機能を追加しない。
- **devkitへ環境固有の資産（Role / Profile / パッケージ導入手順）を追加しない（REQ-007）。**
  - 「便利だから」という理由での共通Role追加は仕様違反である。
- CLI / Incus / Provision / Configurationの責務を分離する。
- 外部コマンド失敗を無視しない。
- どのステップが失敗したかを特定できるエラーを返す。
- `idev provision` で既存instanceを破壊しない。
- `idev destroy` でhost側source treeを削除しない。
- devkit管理外のinstanceを破壊しない。
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

### 判断に迷った場合

「この処理は、あるプロジェクト固有の事情に依存していないか？」を問う。

依存している場合、それはdevkitではなく `.incus-dev/` に属する。
