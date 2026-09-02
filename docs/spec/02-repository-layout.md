# 2. リポジトリ構成

## 2.1 実装言語

`idev` はGoで実装する。

理由：

- 単一の静的バイナリとして配布でき、実行時のランタイム依存が無い
- クロスコンパイルが容易（Linux amd64 / arm64 など）
- `os/exec` による外部コマンド呼び出しが標準ライブラリで完結する
- 構造体タグによるYAML/JSONの型付きデコードが自然に書ける
- 標準の `testing` によりテストが容易
- Incusの公式Go client library (`github.com/lxc/incus/v6/client`) をそのまま利用できる

ただし、アーキテクチャ上Go固有機能への過剰な依存は避ける。
外部（Incus / ansible-playbook / git）との境界はインターフェースとして
定義し、実装差し替えが可能な形に保つ。

---

## 2.2 idev側リポジトリ

共通ツールは独立したGitリポジトリとして管理する。

推奨構成：

```text
incus-dev/
├── README.md                   # 英語版
├── README.ja.md                # 日本語版
├── LICENSE
├── go.mod
├── go.sum
├── Makefile
│
├── cmd/
│   └── idev/
│       └── main.go             # エントリポイント / exit code のみ担当
│
├── internal/
│   ├── cli/                    # コマンド定義（cobra）と実処理
│   │   ├── root.go             # コマンド配線
│   │   ├── app.go              # up / provision / shell / status / ...
│   │   ├── plan.go             # 適用すべきconfig / deviceの算出
│   │   ├── dryrun.go           # 実行予定の組み立て
│   │   ├── naming.go           # project.scope に応じたinstance名
│   │   ├── secrets.go          # ホストからの秘密情報の取り込み
│   │   ├── snapshot.go         # snapshot サブコマンド
│   │   ├── idmap.go            # ホスト側のidmap可否検査
│   │   ├── idmap_mode.go       # idmap方式の解決
│   │   └── log.go
│   │
│   ├── config/                 # dev.yml の読み込みとvalidation
│   │   ├── config.go
│   │   ├── step.go             # provision ステップのデコード
│   │   ├── parse.go
│   │   └── validate.go
│   │
│   ├── project/                # project root 探索
│   │   └── discover.go
│   │
│   ├── incus/                  # Incus操作層
│   │   ├── client.go           # interface定義
│   │   ├── api.go              # Go client library 実装
│   │   ├── connect.go          # remote / image の解決
│   │   ├── exec_tty.go         # 端末を伴う実行
│   │   ├── wait.go             # 起動待ち
│   │   ├── name.go             # instance命名規則
│   │   └── incustest/          # テスト用fake
│   │
│   ├── provision/              # bootstrap / provision ステップの実行
│   │   ├── provision.go
│   │   ├── select.go           # 部分実行（--step / --from）
│   │   ├── env.go
│   │   ├── step_run.go
│   │   ├── step_ansible.go
│   │   └── step_galaxy.go
│   │
│   └── runner/                 # 外部コマンド実行の集約
│       ├── runner.go
│       ├── args.go             # 引数の組み立てとマスク指定
│       └── runnertest/         # テスト用fake
│
├── schemas/
│   ├── dev-v1.schema.json
│   └── embed.go
│
├── docs/
│   ├── README.md               # 文書の索引（英語版・日本語版）
│   ├── troubleshooting.md      # ホスト環境起因の問題（英語版・日本語版）
│   ├── manual/                 # 利用者向けマニュアル（英語）
│   │   └── ja/                 # 同（日本語）
│   └── spec/                   # 設計仕様。日本語のみ
│
├── skills/                     # AIエージェント向けAgent Skill
│   ├── incus-dev/           # 英語版
│   │   ├── SKILL.md
│   │   ├── references/
│   │   └── templates/
│   └── incus-dev-ja/        # 日本語版
│
├── examples/                   # ドキュメント用サンプル（実行時には使用しない）
│   ├── minimal/
│   ├── shell-based/
│   └── ansible-based/
│
├── scripts/                    # ビルド・検査用。バイナリには入らない
│   └── vuln.jq                 # make vuln の判定（8.4.0）
│
├── .claude/skills/             # 開発者向け作業手順（利用者向けの skills/ とは別）
│
└── test/
    ├── examples_test.go        # examples/ が読めることの確認
    ├── skills_test.go          # skills/ と .claude/skills/ の確認
    ├── structure_test.go       # REQ-007の資産検査、パッケージ依存方向、
    │                           # 外部コマンドとos.Exitの局在、Makefile/CIのゲート
    └── integration/            # //go:build integration
        └── contract_test.go    # 実daemonに対する Client 契約（8.3.2）
```

利用者向け文書は英語と日本語の両方を保つ。英語が既定のパスで、
日本語は `ja/` ディレクトリまたは `.ja.md` サフィックス。
`docs/spec/` は実装判断の基準であり利用者向けではないため、日本語のみとする。

Goの慣例に従い、外部から利用されない実装は `internal/` へ配置する。

---

## 2.3 idevリポジトリに置かないもの

REQ-007により、以下はidevリポジトリに存在してはならない。

```text
ansible/roles/         共通Ansible Role
ansible/*.yml          共通Playbook
profiles/*.yaml        共通Incus Profile
requirements.yml       共通collection定義
```

これらはすべてプロジェクト側の `.incus-dev/` に属する。

`examples/` 配下のサンプルは **ドキュメントとしてのみ** 存在し、
`idev` の実行時に参照されない。バイナリにも同梱しない。

再利用可能なprovisioning資産を共有したい場合は、idevとは別の
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

これはidevを「実行機構に特化させる」ことによる直接的な利点である。

---

## 2.5 ビルドと配布

```bash
go build ./cmd/idev            # ローカルビルド
go install ./cmd/idev          # $GOBIN へインストール
```

バージョン情報は `-ldflags` で埋め込む。

```bash
go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/idev
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

idevは `dev.yml` 以外のディレクトリ名・構成を原則として規定しない。
`dev.yml` から参照されたパスのみを使用する。

唯一の例外は `.incus-dev/ansible/ansible.cfg` で、
存在する場合にansibleステップの設定として使用する
（[06-provisioning.md](06-provisioning.md) 6.5.3）。置かなくてもよい。

**この配下のファイルだけで、その開発環境が完全に再現できる状態を維持する。**

具体例は [10-examples.md](10-examples.md) を参照。
