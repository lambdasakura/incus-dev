# 2. リポジトリ構成

## 2.1 実装言語

dev CLIはGoで実装する。

理由：

- 単一の静的バイナリとして配布でき、実行時のランタイム依存が無い
  （利用者はGoツールチェインを持たなくてよい）
- クロスコンパイルが容易（Linux amd64 / arm64 など）
- `os/exec` による外部コマンド呼び出しが標準ライブラリで完結する
- 構造体タグによるYAML/JSONの型付きデコードが自然に書ける
- `go:embed` によりAnsible Role・Profile・JSON Schemaをバイナリへ同梱できる
- 標準の `testing` によりテストが容易
- Incusの公式Go client library (`github.com/lxc/incus/client`) を将来利用できる

ただし、アーキテクチャ上Go固有機能への過剰な依存は避ける。
特に、外部コマンド（incus / ansible-playbook / git）との境界は
インターフェースとして定義し、実装差し替えが可能な形に保つ。

---

## 2.2 dev CLI側リポジトリ

共通ツールは独立したGitリポジトリとして管理する。

推奨構成：

```text
incus-devkit/
├── README.md
├── LICENSE
├── go.mod
├── go.sum
├── Makefile
├── requirements.yml            # ansible-galaxy collections
│
├── cmd/
│   └── dev/
│       └── main.go             # エントリポイント / exit code のみ担当
│
├── internal/
│   ├── cli/                    # コマンド定義（cobra）
│   │   ├── root.go
│   │   ├── up.go
│   │   ├── provision.go
│   │   ├── shell.go
│   │   ├── status.go
│   │   ├── destroy.go
│   │   ├── rebuild.go
│   │   └── validate.go
│   │
│   ├── config/                 # dev.yml の読み込みとvalidation
│   │   ├── config.go
│   │   ├── schema.go
│   │   └── testdata/
│   │
│   ├── project/                # project root 探索
│   │   └── discover.go
│   │
│   ├── incus/                  # Incus操作層
│   │   ├── client.go           # interface定義
│   │   ├── cli.go              # incus CLI 実装
│   │   └── name.go             # instance命名規則
│   │
│   ├── ansible/                # Ansible操作層
│   │   ├── ansible.go
│   │   └── inventory.go
│   │
│   ├── runner/                 # 外部コマンド実行の集約
│   │   └── runner.go
│   │
│   ├── assets/                 # go:embed による同梱アセットの展開
│   │   └── assets.go
│   │
│   └── errs/                   # エラー型 / exit code マッピング
│       └── errs.go
│
├── schemas/
│   └── dev-v1.schema.json
│
├── ansible/
│   ├── ansible.cfg
│   ├── bootstrap.yml
│   ├── provision.yml
│   │
│   └── roles/
│       ├── common/
│       ├── devtools/
│       ├── python/
│       ├── nodejs/
│       ├── golang/
│       ├── rust/
│       └── docker/
│
├── profiles/
│   ├── dev-base.yaml
│   ├── nested.yaml
│   ├── gpu-amd.yaml
│   └── gpu-nvidia.yaml
│
└── test/
    └── integration/            # //go:build integration
```

Goの慣例に従い、外部から利用されない実装は `internal/` へ配置する。

将来的にライブラリとして公開したいパッケージが生じた場合のみ `pkg/` を検討する。

---

## 2.3 アセットの同梱

`ansible/`、`profiles/`、`schemas/` は `go:embed` でバイナリへ同梱する。

```go
//go:embed all:ansible all:profiles all:schemas
var Assets embed.FS
```

`ansible-playbook` は実ファイルパスを必要とするため、
実行時にキャッシュディレクトリへ展開してから使用する。

```text
${XDG_CACHE_HOME:-~/.cache}/incus-devkit/runtime/<runtime-version>/
```

これにより以下を満たす。

- 利用者は単一バイナリの配置のみで動作する
- `runtime.version` ごとに展開先を分離でき、再現性を確保できる
  （[03-configuration.md](03-configuration.md) の `runtime` を参照）

開発時にはリポジトリ内のファイルを直接参照できるよう、
展開先を上書きする手段（環境変数など）を用意してよい。

---

## 2.4 ビルドと配布

```bash
go build ./cmd/dev            # ローカルビルド
go install ./cmd/dev          # $GOBIN へインストール
```

バージョン情報は `-ldflags` で埋め込む。

```bash
go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/dev
```

リリース成果物は各プラットフォーム向けの単一バイナリとする。

CGOは不要とし、`CGO_ENABLED=0` でビルド可能な実装を維持する。

---

## 2.5 プロジェクト側構成

基本形：

```text
my-project/
├── .incus-dev/
│   └── dev.yml
│
├── src/
├── tests/
├── README.md
└── ...
```

多くのプロジェクトでは `.incus-dev/dev.yml` だけで環境を定義できることを目標とする。

特殊なプロビジョニングが必要な場合のみ以下を追加する。

```text
my-project/
├── .incus-dev/
│   ├── dev.yml
│   │
│   └── ansible/
│       ├── project.yml
│       ├── vars.yml
│       ├── files/
│       └── templates/
│
└── ...
```

プロジェクト固有のAnsible Roleを必須としてはならない。
