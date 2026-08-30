# 9. MVPとロードマップ

## 9.1 MVP

最初の実装では以下に機能を限定する。

### CLI

```text
idk up
idk provision
idk shell
idk status
idk destroy
idk rebuild
idk validate
```

### Incus

```text
local remote
default project
container のみ
任意の image
既存 profile の名前参照
instance.config / instance.devices の素通し
workspace bind mount（idmap: auto）
devkit管理情報 user.incus-devkit.*
```

### Provisioning

```text
instance ready 待ち
bootstrap（既定 + 上書き + 無効化）
run ステップ（コンテナ内実行）
ansible ステップ（community.general.incus / 一時inventory）
devkit変数の注入
```

### Configuration

```text
schema
runtime
project
instance (image / type / profiles / config / devices)
workspace (source / target / idmap)
bootstrap
provision (run / ansible)
```

### 明示的にMVPに含まないもの

```text
packages / features に相当する高水準フィールド（恒久的な非目標）
devkit同梱の Role / Profile（恒久的な非目標）
requirements.yml の自動適用
```

---

## 9.2 MVP以降の候補

```text
provision --step / --from（部分実行）
galaxy ステップ型（ansible-galaxy install）
追加のステップ型（必要が生じた場合のみ）

idk exec
idk shell の user / command / cwd 設定
JSON output
shell completion

instance.config の削除追従（user.incus-devkit.managed による差分unset）
再起動が必要な設定変更の自動処理
virtual-machine サポート

Incus remote
Incus project

multiple checkout support
branch-specific instance

snapshot / restore
persistent volumes
multiple workspaces

環境変数 / secrets の注入機構

Incus Go client library への移行
```

---

## 9.3 開発優先順位

### Phase 0

Goプロジェクトの初期化。

```text
go.mod
cmd/idk
internal/ の骨格
Makefile
CI (gofmt / go vet / go test)
```

以下が成功する状態にする。

```bash
go build ./cmd/idk
idk --version
```

---

### Phase 1

設定とproject discovery。

```text
config parser（instance.config / devices / step のデコード）
JSON Schema
project discovery
CLI skeleton
```

以下が成功する状態にする。

```bash
idk validate
```

---

### Phase 2

Incus基本操作。

```text
create / start / stop / delete
profile 存在確認
config / devices の適用
workspace mount（idmap）
devkit管理情報
```

以下を成立させる。

```bash
idk up        # provisionなし
idk status
idk shell
idk destroy
```

---

### Phase 3

実行機構（devkitの中核）。

```text
instance ready 待ち
bootstrap（既定 / 上書き / 無効化）
run ステップ
ステップ失敗時のエラー
```

`.incus-dev/` にシェルスクリプトだけを置いたプロジェクトが
完全に動作する状態にする。

---

### Phase 4

ansibleステップ。

```text
一時inventory生成
devkit変数の注入
ansible.cfg の扱い
```

---

### Phase 5

運用系。

```text
idk rebuild
idk provision の再実行検証
dry-run
エラーメッセージの整備
```

---

### Phase 6

テストとUX改善。

```text
unit tests
integration tests
verbose
JSON output
shell completion
examples/ の整備
```

---

## 9.4 完成条件

MVPは以下がすべて成立した時点で完成とする。

### 前提

新規Ubuntuホスト上で、以下のみをセットアップ済みとする。

- Incus
- `idk`（単一バイナリの配置のみ）
- Ansible（Ansibleを使うサンプルを検証する場合）

### 基本フロー

任意のテストプロジェクトで、

```bash
git clone <repository>
cd <repository>

idk validate
idk up
idk status
idk shell
```

が成功すること。

コンテナ内部で、

```bash
cd /workspace
```

するとホスト側Git repositoryが見えること。

### provisioning

`.incus-dev/dev.yml` に以下を記述したプロジェクトで、
コンテナ内に該当環境が構築されること。

```yaml
provision:
  - name: packages
    run: |
      command -v jq >/dev/null 2>&1 ||
        (apt-get update && apt-get install -y jq)

  - name: playbook
    ansible:
      playbook: .incus-dev/ansible/site.yml
```

さらに、

```bash
idk provision
idk provision
```

を連続実行して正常終了すること。

### 破棄

```bash
idk destroy --force
```

を実行するとIncus instanceだけが削除され、

```text
Git repository
source files
.incus-dev/
```

がホスト上に残ること。

### 自己完結性（REQ-007）

以下がすべて成立すること。

1. devkitリポジトリに Ansible Role / Incus Profile が存在しない
2. devkitバイナリが同梱するアセットは JSON Schema のみである
3. あるプロジェクトの `.incus-dev/` を別の空リポジトリへコピーし、
   `idk up` を実行すると同じ環境が再現される
4. devkitを新しいバージョンへ更新しても、既存プロジェクトの
   構築内容（導入されるパッケージやツール）が変化しない
