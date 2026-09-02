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
Step planning（実行順序）
Command construction
  - run ステップのargvとexec指定
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

**カバレッジは品質の指標ではない。** 行カバレッジ99%の状態で15ラウンドの
レビューを実施し、毎ラウンド実欠陥が出た。変異テスト（実装を1箇所壊して
テストが落ちるか確認する）を一巡させたところ、**実行されているが何も
検査していない挙動が24件**見つかった。


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

REQ-007（idevが環境固有の資産を持たないこと）は仕様の中核であり、
テストで機械的に検証する。

```text
idevリポジトリに以下が存在しないこと

  ansible/roles/
  profiles/*.yaml
  requirements.yml

embedされるアセットが schemas/ のみであること
```

`examples/` 配下がバイナリへ同梱されていないことも確認する。

パッケージの依存方向（07-implementation.md 7.1）も検査する。
実装側の直接importのみを見る。推移的な依存はそれをimportしたパッケージの
責務であり、テストがfakeを参照するのは実装の依存ではないためである。

`examples/` 配下の設定ファイル（Markdownを除く）がASCIIのみであることも
検査する。サンプルは1組しか無く、言語を問わず読まれるためである。
英日の二言語を持つのは、隣に置く `README.md` / `README.ja.md` の側である。

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

### 8.3.1 回帰テストの置き場所

**Incusの挙動に関する指摘を修正した場合、単体テストだけでは不足する。**
`test/integration/regression_test.go` にも追加する。

`internal/incus/incustest` は idev 自身が書いた fake である。
Incusに対する誤った思い込みは、誤った fake と誤った単体テストを生み、
両者が一致してしまう。**思い込みとそのテストが同じ誤りの二重記述になる。**

実際に発生した回帰は全てこの形だった。

| 誤った思い込み | 実際 |
| --- | --- |
| 404は「対象が存在しない」 | projectやpoolが無い場合も404 |
| snapshot名は自由 | `.` はストレージドライバによってinstanceごと削除不能にする |
| `both 1000 0` と uid/gid別行は別物 | 同じマッピング |
| receive-then-close は冪等 | 2つのgoroutineが同時にdefaultへ入り panic |

いずれも単体テストでは検出できず、結合テストでは検出できる。
結合テストは実CLIを起動するため、メソッドを直接呼ぶ単体テストが到達しない
層（フラグ解釈など）も検証する。

追加したテストは、**対応する修正を戻して落ちることを確認する**。
落ちないテストは何も守っていない。

### 8.3.2 契約テスト

`internal/incus/contract` に、`incus.Client` のあらゆる実装が満たすべき挙動を
1組の検査として置く。同じ検査を2回実行する。

| 実行 | 対象 | 走らせる場所 |
| --- | --- | --- |
| `go test ./internal/incus/incustest/` | `incustest.Fake` | 単体（Incus不要） |
| `-tags integration` | `incus.API`（実daemon） | 結合 |

**食い違えばどちらかが落ちる。** これが fake を「正しいと仮定されたもの」から
「実物と一致することが検査されたもの」へ変える唯一の仕組みである。

導入時、20項目中6項目が食い違っていた。

| 項目 | fake | 実Incus |
| --- | --- | --- |
| 既存名での `CreateVolume` | 成功 | 拒否 |
| 存在しないpoolへの `CreateVolume` | 成功 | 拒否 |
| `VolumeExists`（pool不在） | `false, nil` | pool不在エラー |
| `CheckImage`（存在しないimage） | 成功 | 拒否 |
| `CreateSnapshot("a/b")` | 成功 | 拒否 |
| 重複snapshot名 | 成功 | 拒否 |

**`Client` にメソッドを追加したら、契約テストにも追加する。**
プログラムの実行そのもの（`Exec` が渡されたargvを本当に走らせるか）だけは
fakeに要求できないため、`Env.RunsPrograms` で結合側のみ検査する。

契約テストは製品コードではなく検査であるため、`make cover` の計測対象から
除外する（daemon側の半分は単体実行では動かないため）。

---

## 8.4 CI

最低限以下を実行する。

```bash
gofmt -l .
go vet ./...
go test ./...
idev validate           # examples/ 配下の各サンプルに対して
```

`examples/` の各サンプルが常に `idev validate` を通ることを保証する
（`test/examples_test.go` が `go test ./...` の中で行うため、別ジョブは要らない）。

### 8.4.0 脆弱性の検査

`make vuln` が `govulncheck` を実行する。CIも同じターゲットを呼ぶ
（CIとローカルで判定が食い違わないようにするため）。

**失敗させるのは「このコードから到達可能で、かつ現在使っているモジュールに
修正版が存在する」もののみ。** バージョンを上げるだけで対処できるものである。

それ以外は一覧を出力するが、失敗させない。出力には**修正版がどこに存在するか**
を併記する。

| 表示 | 意味 |
| --- | --- |
| `update to X` | 使用中のモジュールに修正版がある。**失敗させる** |
| `only in <module>@<version>` | 別のモジュールパスにのみ存在する。移行の判断が要る |
| `no fix released` | どこにも修正版が無い |

理由は、リンクされているモジュールの脆弱性が全て報告されるためである。
Incus clientはdaemonとパッケージを共有しており、`subprocess` `tls` `util` `ws`
といった共通ヘルパ経由で、**サーバ側の脆弱性22件が「到達可能」として報告される**。
このうち13件は `incus/v7` でのみ修正されており、v6を使う限り
`fixed_version` は空のままである。**「修正版が出れば赤くなる」は
同一モジュールパス内でしか成り立たない**ため、v7側の修正は
`only in` として表示する。メジャーバージョンの移行はバンプではなく判断だからである。

**永久に緑にならないゲートは、いずれ無効化される。** そうなれば本当に対処
すべきものも見えなくなる。修正版が出た時点で赤くなり、依存を更新する、
という運用にする。

除外リストを手で保守しない。放置されて腐るためである。

判定ロジックは `scripts/vuln.jq` に置き、`make vuln` と CI の双方が同じものを使う。
`jq` を必要とするため、無い場合は `make vuln` が明示的に失敗する。

unit testはIncus daemonが無い環境で全て通らなければならない。

開発機ではIncusが動いていることが多く、daemonを必要とするテストが
混ざっても気付けない。そのため `make test` / `make cover` は
`INCUS_SOCKET` を存在しないパスへ向けて実行する。
CIで初めて発覚する、という状態を作らないためである。

Incusを必要とするテストは統合テスト（8.3）に置く。

### 8.4.2 実行するプラットフォーム

Linuxのみで実行する。配布対象がLinuxだけであり
（仕様 07-implementation.md 7.7）、他のプラットフォームで
検査する対象が無いためである。

テスト自体もLinuxを前提にしてよい。`internal/runner` のテストは
`sh` / `sleep` を実際に起動し、`internal/project` のテストは
POSIXのパーミッション（`chmod 0o000`）に依存する。
**移植性のためにこれらの検査を弱めない**（8.1「カバレッジ」参照）。

統合テスト（8.3）はIncusを必要とするため、CIでは実行しない。
Incusのある環境で `make test-integration` を手で実行する。

GitHub Actionsのランナーには Incus が無く、入れて `incus admin init` まで
持っていくのはCIの安定性に見合わない。Incusを持たない環境では
テスト側がskipするため（`test/integration/main_test.go`）、
うっかり緑になることもない。

### 8.4.3 リリース

`v*` のタグをpushすると GoReleaser（`.goreleaser.yaml`）が
linux の amd64 / arm64 のアーカイブと
`checksums.txt` を作り、GitHub Releaseへ公開する。

リリース設定が壊れていないことは、通常のCIで
`goreleaser release --snapshot` を実行して検証する。
タグを打った時点で初めて失敗する、という状態を作らないためである。
