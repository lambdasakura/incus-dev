# incus-dev

Incusでプロジェクト単位の開発環境を構築・管理するCLIツール `idev` のリポジトリ。

## 最重要原則

**idevは環境固有の内容を持たない（REQ-007）。**

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
| Agent Skill | `skills/incus-dev/` | `skills/incus-dev-ja/` |

CLIのフラグ・出力・既定値を変更した場合、`03-commands.md` と `04-dev-yml.md`、
および Agent Skill の該当箇所を **英日そろえて** 更新する。
片方だけ更新して放置しない。

スキルは `test/skills_test.go` が雛形の妥当性・コマンド名の実在・
frontmatterの `name` がディレクトリ名と一致し重複しないことを検査する。

### 英語で書くもの

外部のコントリビュータが読むため、**リポジトリに入るテキストは原則として英語**。

| 対象 | 補足 |
| --- | --- |
| コミットメッセージ | `## コミット` 参照 |
| `.go` のコメント、`t.Run` のサブテスト名、テストの失敗メッセージ | 仕様書への参照は `spec 04-cli.md 4.10` の形式 |
| CLIのusage（Short / Long / フラグ説明）、確認プロンプト、ログ、エラー | `internal/cli` の `TestUserFacingTextIsASCII` が非ASCIIの混入を検査する |
| `examples/` 配下の `dev.yml` やスクリプトのコメント | サンプルは1組しか無く英日に分けられない。`test/examples_test.go` の `TestExamplesAreASCII` が検査する |
| ビルド・CI設定のコメント | `Makefile`、`.golangci.yml`、`.github/`、`.goreleaser.yaml` |

CLIの日本語での説明はマニュアルの日本語版が担う。

### 日本語で書くもの

- `docs/spec/`（設計仕様。実装判断の基準）と `CLAUDE.md`。
  利用者向け文書ではないため二言語化しない
- 上表の日本語版文書（`ja/` または `.ja.md`）

判断に迷ったら英語で書く。日本語で書いてよいのはここに挙げたものだけである。

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
make vuln               # govulncheck（ネットワークが要る。checkには含めない）
```

**結果は終了コードで判定する。出力のgrepで判定しない。**

```bash
make check >/dev/null 2>&1; echo "check exit=$?"
```

`make check | grep -c FAIL` で判定したことがある。golangci-lint は "FAIL" を
出力しないため、**gofmt違反が2コミット通過しCIで発見された**。
出力を読んで合否を決める検査は検査ではない。

統合テストは全出力をファイルに残す。`| tail -3` で流すと、
200行上の失敗理由が消える。

`make check` は golangci-lint が無ければ失敗する（`make tools` を案内する）。
以前は gofmt + go vet にフォールバックして終了コード0を返していた。

## 作業別のスキル

`.claude/skills/` に、このリポジトリでの作業手順を置いている。
これは開発者向けであり、利用者へ配布する `skills/` とは別物である。

| スキル | 使う場面 |
| --- | --- |
| [fix-finding](.claude/skills/fix-finding/SKILL.md) | 指摘・バグ報告を受けて修正するとき |
| [review-round](.claude/skills/review-round/SKILL.md) | コードレビューを実施するとき、レビューと修正を繰り返すとき |

どちらも、このリポジトリで**実際に起きた失敗**から書かれている。
一般論ではないので、手順を省くときは何が起きたかを読んでから決めること。

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

**カバレッジは品質の指標ではない。** 行カバレッジ99%の状態で15ラウンドの
レビューを行い、毎ラウンド実欠陥が出た。変異テスト（実装を1箇所壊して
テストが落ちるか見る）を一巡させたところ、**実行されているが何も検査して
いない挙動が24件**見つかった。新しいテストを書いたら、対応する修正を戻して
落ちることを必ず確認する。落ちないテストは何も守っていない。

Incus daemonが無い環境でも `make test` が全て通ること。
Incusに触れるテストは `test/integration/` に置き `//go:build integration` を付ける。

### 回帰テスト

**指摘を受けて修正する場合、単体テストだけでは不足する。**

| 指摘の対象 | テストを置く場所 |
| --- | --- |
| idev自身のロジック | 該当パッケージの `_test.go` |
| **Incusの挙動** | **`test/integration/` にも** |
| コマンドの出力 | `internal/cli`。コマンドごとに文言が違うなら `test/integration/` にも |
| 同梱ファイル（examples / docs / skills） | `test/` |

理由は `internal/incus/incustest` が **idev自身が書いたfake** だからである。
Incusに対する誤った思い込みは、誤ったfakeと誤ったテストを生み、両者が
一致してしまう。実際に起きた回帰は全てこの形だった。

- 404は「存在しない」の意味（**projectやpoolが無い場合も404**）
- snapshot名は自由（`.` はbtrfsでinstanceごと削除不能にする）
- `both 1000 0` と uid/gid 別行は別物（**同じマッピング**）
- receive-then-close は冪等（**2つのgoroutineが同時にdefaultへ入る**）

いずれも単体テストでは検出できず、結合テストでは検出できる。
結合テストは実CLIを起動するため、メソッドを直接呼ぶ単体テストが見られない
層も見る（`idev snapshot create -wip` はフラグ解釈で弾かれる）。

### 契約テスト

`internal/incus/contract` が `incus.Client` の満たすべき挙動を1組で定義し、
**fake と実daemonの両方に対して実行する**。食い違えばどちらかが落ちる。

`Client` にメソッドを追加したら、契約テストにも追加する。
導入時、20項目中6項目で fake が実物と食い違っていた。

### 共有関数を変更する前に

呼び出し元を全て列挙し、**それぞれが何を必要とするか**を書き出す。
異なるものを必要とするなら関数を分けるか、引数で区別する。
このリポジトリの回帰3件は、複数の呼び出し元を持つ関数を1つだけ見て
変更したことが原因だった。

```bash
grep -rn '<name>(' --include='*.go' .
```

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
- **`git add -A` を使わない。** 論点に属するファイルを明示的にstageする。
  このリポジトリでは3回、無関係な変更を巻き込んで `reset --soft` による
  分割が必要になった
- コミット前に各コミット単体で `gofmt -l` が空、`go build ./...` が通ることを
  確認する（履歴の途中がCIで落ちる状態を残さない）
- **メッセージは英語**。`type: summary` 形式
  （`feat:` `fix:` `docs:` `test:` `refactor:` `chore:`）
- 要約は命令形で書き、末尾にピリオドを打たない
  （`fix: stop validate from connecting to Incus`）
- 本文には「何をしたか」より「なぜそうしたか」を書く。
  仕様書への参照は `spec 04-cli.md 4.7` の形式
- コミット前に `make check` を通す
