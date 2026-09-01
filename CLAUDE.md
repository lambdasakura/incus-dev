# incus-devkit

Incusでプロジェクト単位の開発環境を構築・管理するCLIツール `idev` のリポジトリ。

## 最重要原則

**devkitは環境固有の内容を持たない（REQ-007）。**

このリポジトリに以下を追加してはならない。

- Ansible Role / 共通Playbook
- Incus Profile
- 言語ランタイムやツールの導入手順
- 特定ディストリビューションを前提とした処理

唯一の例外は `internal/provision` の既定bootstrap（Debian系前提のpython3導入）であり、
プロジェクト側から上書き可能な形でのみ存在してよい。

「便利だから共通Roleを追加する」は仕様違反である。判断に迷ったら
`docs/spec/01-overview.md` の REQ-007 を読むこと。

## ドキュメント

外部公開しているため、**利用者向け文書は英語と日本語の両方を保つ**。
英語が既定のパス、日本語が `ja/` または `.ja.md`。

| 文書 | 英語 | 日本語 |
| --- | --- | --- |
| README | `README.md` | `README.ja.md` |
| マニュアル | `docs/manual/` | `docs/manual/ja/` |
| トラブルシューティング | `docs/troubleshooting.md` | `docs/troubleshooting.ja.md` |
| 構成例 | `examples/README.md` | `examples/README.ja.md` |
| Agent Skill | `skills/incus-devkit/` | `skills/incus-devkit-ja/` |

`docs/spec/`（設計仕様。実装判断の基準）と `CLAUDE.md` は日本語のみ。
利用者向け文書ではないため二言語化しない。

CLIのフラグ・出力・既定値を変更した場合、`03-commands.md` と `04-dev-yml.md`、
および Agent Skill の該当箇所を **英日そろえて** 更新する。
片方だけ更新して放置しない。

スキルは `test/skills_test.go` が雛形の妥当性・コマンド名の実在・
frontmatterの `name` がディレクトリ名と一致し重複しないことを検査する。

### 利用者が見るテキストは英語

CLIのusage（Short / Long / フラグ説明）、確認プロンプト、ログ、エラーは英語。
`internal/cli` の `TestUserFacingTextIsASCII` が非ASCIIの混入を検査する。
日本語の説明はマニュアルの日本語版が担う。

## 仕様書

`docs/spec/` が実装判断の基準。実装前に該当章を読むこと。

| 章 | 内容 |
| --- | --- |
| [01-overview.md](docs/spec/01-overview.md) | 要件(REQ-001〜007)、責務分担 |
| [02-repository-layout.md](docs/spec/02-repository-layout.md) | パッケージ構成 |
| [03-configuration.md](docs/spec/03-configuration.md) | `.incus-dev/dev.yml` の全項目 |
| [04-cli.md](docs/spec/04-cli.md) | 各コマンドの挙動 |
| [05-incus.md](docs/spec/05-incus.md) | Incus操作層 |
| [06-provisioning.md](docs/spec/06-provisioning.md) | bootstrap / ステップ実行 |
| [07-implementation.md](docs/spec/07-implementation.md) | Go実装方針 |
| [08-testing.md](docs/spec/08-testing.md) | テスト方針 |
| [09-roadmap.md](docs/spec/09-roadmap.md) | 実装順序 |
| [10-examples.md](docs/spec/10-examples.md) | プロジェクト側の構成例 |

仕様と実装が食い違った場合、原則として仕様を正とする。
仕様側を変えるべきと判断した場合は、実装より先に仕様書を更新する。

## コマンド

```bash
make build              # ./bin/idev をビルド
make test               # go test ./...（Incus不要）
make cover              # カバレッジ（-coverpkg=./... で横断集計）
make test-integration   # go test -tags integration ./test/integration/...（Incus必須）
make lint               # golangci-lint（無ければ gofmt + go vet）
make fmt                # 整形
make tools              # golangci-lint を導入
make check              # lint + test
```

## 開発フロー（TDD）

**必ずテストを先に書く。**

1. 失敗するテストを書く
2. `make test` で失敗を確認する（期待した理由で失敗しているか見る）
3. テストが通る最小限の実装を書く
4. `make test` で成功を確認する
5. リファクタリングする

「実装してからテストを書く」「テストを書かずにコミットする」は行わない。

カバレッジは到達可能な分岐をすべて網羅することを目標とする。
ただし **カバレッジのために検査を削ったり、テスト専用の注入点を実装へ
埋め込んだりしない**。不変条件により到達しない防御的分岐は残してよい
（仕様 08-testing.md「カバレッジ」参照）。

Incus daemonが無い環境でも `make test` が全て通ること。
Incusに触れるテストは `test/integration/` に置き `//go:build integration` を付ける。

## パッケージ構成と依存方向

```
cmd/idev
   └─▶ internal/cli
          ├─▶ internal/project     project root 探索
          ├─▶ internal/config      dev.yml の解釈とvalidation
          ├─▶ internal/incus       Incus操作（interface + API実装）
          └─▶ internal/provision   bootstrap / ステップ実行
                 ├─▶ internal/incus    run ステップ
                 └─▶ internal/runner   ansible ステップ
```

守るべき制約：

- `internal/config` はIncus操作もステップ実行も行わない（解釈とvalidationのみ）
- Incus操作はGo client library（`internal/incus/api.go`）で行う。
  `incus` コマンドを呼び出さない（`docs/spec/05-incus.md` 5.7.1）
- `internal/incus` / `internal/provision` はCLIの出力形式を知らない
- 外部コマンド実行（ansible-playbook / git）は `internal/runner` に集約する。
  他パッケージで `os/exec` を直接使わない
- `os.Exit` は `cmd/idev/main.go` のみ。他は `error` を返す
- 外部プロセスを伴う関数は第一引数に `context.Context` を取る

## コーディング規約

- エラーは `fmt.Errorf("...: %w", err)` でラップする
- 一時ファイル・一時ディレクトリは `defer` で必ず削除する
- ログは `log/slog`。通常は `[idev] ...` 形式、`--verbose` でdebug
- Secretを含みうる引数・環境変数をログやエラーへ出さない。
  外部コマンドへ渡す値は `runner.Command.Redact` で表示時にマスクする
  （実行される引数は変えない）
- `CGO_ENABLED=0` でビルドできない依存を追加しない
- 標準ライブラリで足りる領域に依存を足さない

## 依存ライブラリ

| 用途 | ライブラリ |
| --- | --- |
| CLI | `github.com/spf13/cobra` |
| YAML | `sigs.k8s.io/yaml`（YAML→JSON→struct） |
| JSON Schema | `github.com/santhosh-tekuri/jsonschema/v6` |
| Incus API | `github.com/lxc/incus/v6/client` |
| 端末制御 | `golang.org/x/term` |
| 制御用websocket | `github.com/gorilla/websocket` |
| テスト差分 | `github.com/google/go-cmp` |

## 用語

| 用語 | 意味 |
| --- | --- |
| `idev` | コマンド名 |
| `.incus-dev/` | プロジェクト側の設定ディレクトリ |
| `dev.yml` | 開発環境定義ファイル |
| `dev-<project>` | 生成されるIncus instance名 |
| `dev` | Ansible inventory上のホスト名（`hosts: dev`） |

これらはコマンド名の変更に追従しない識別子である。

## コミット

- 1コミット1論点。実装とテストは同じコミットに含める
- メッセージは日本語。`種別: 要約` 形式（`feat:` `fix:` `docs:` `test:` `refactor:` `chore:`）
- コミット前に `make check` を通す
