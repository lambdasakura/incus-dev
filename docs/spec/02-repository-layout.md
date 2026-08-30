# 2. リポジトリ構成

## 2.1 実装言語

dev CLIはGoで実装する。

理由：

- 単一の静的バイナリとして配布でき、実行時のランタイム依存が無い
- クロスコンパイルが容易（Linux amd64 / arm64 など）
- `os/exec` による外部コマンド呼び出しが標準ライブラリで完結する
- 構造体タグによるYAML/JSONの型付きデコードが自然に書ける
- 標準の `testing` によりテストが容易
- Incusの公式Go client library (`github.com/lxc/incus/client`) を将来利用できる

ただし、アーキテクチャ上Go固有機能への過剰な依存は避ける。
外部コマンド（incus / ansible-playbook / git）との境界はインターフェースとして
定義し、実装差し替えが可能な形に保つ。

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
│   │   ├── step.go             # provision ステップのデコード
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
│   ├── provision/              # bootstrap / provision ステップの実行
│   │   ├── provision.go
│   │   ├── step_run.go
│   │   ├── step_ansible.go
│   │   └── inventory.go
│   │
│   ├── runner/                 # 外部コマンド実行の集約
│   │   └── runner.go
│   │
│   └── errs/                   # エラー型 / exit code マッピング
│       └── errs.go
│
├── schemas/
│   └── dev-v1.schema.json
│
├── examples/                   # ドキュメント用サンプル（実行時には使用しない）
│   ├── minimal/
│   ├── shell-based/
│   └── ansible-based/
│
└── test/
    └── integration/            # //go:build integration
```

Goの慣例に従い、外部から利用されない実装は `internal/` へ配置する。

---

## 2.3 devkitリポジトリに置かないもの

REQ-007により、以下はdevkitリポジトリに存在してはならない。

```text
ansible/roles/         共通Ansible Role
ansible/*.yml          共通Playbook
profiles/*.yaml        共通Incus Profile
requirements.yml       共通collection定義
```

これらはすべてプロジェクト側の `.incus-dev/` に属する。

`examples/` 配下のサンプルは **ドキュメントとしてのみ** 存在し、
dev CLIの実行時に参照されない。バイナリにも同梱しない。

再利用可能なprovisioning資産を共有したい場合は、devkitとは別の
Ansible Collectionやリポジトリとして配布し、各プロジェクトが
明示的に取り込む形とする。

---

## 2.4 同梱アセット

バイナリへ同梱するのはJSON Schemaのみとする。

```go
//go:embed schemas
var schemaFS embed.FS
```

Schemaはメモリ上で読み込むだけであり、ファイルとして展開する必要がない。

Playbook・Role・Profileを同梱しないため、

- 実行時のアセット展開処理
- バージョンごとのキャッシュディレクトリ管理
- 展開先とリポジトリの不整合

といった機構は一切不要になる。

これはdevkitを「実行機構に特化させる」ことによる直接的な利点である。

---

## 2.5 ビルドと配布

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

## 2.6 プロジェクト側構成

最小形：

```text
my-project/
├── .incus-dev/
│   └── dev.yml
│
├── src/
└── ...
```

`dev.yml` 1ファイルだけで環境を定義できる。

provisioningを伴う場合の例：

```text
my-project/
├── .incus-dev/
│   ├── dev.yml
│   │
│   ├── scripts/
│   │   └── prepare.sh
│   │
│   └── ansible/
│       ├── ansible.cfg
│       ├── site.yml
│       ├── vars.yml
│       ├── requirements.yml
│       ├── roles/
│       ├── files/
│       └── templates/
│
└── ...
```

devkitは `dev.yml` 以外のディレクトリ名・構成を規定しない。
`dev.yml` から参照されたパスのみを使用する。

**この配下のファイルだけで、その開発環境が完全に再現できる状態を維持する。**

具体例は [10-examples.md](10-examples.md) を参照。
