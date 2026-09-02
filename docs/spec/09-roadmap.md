# 9. MVPとロードマップ

## 9.1 MVP

最初の実装では以下に機能を限定する。

### CLI

```text
idev up
idev provision
idev shell
idev status
idev destroy
idev rebuild
idev validate
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
idev管理情報 user.incus-dev.*
```

### CLI補助

```text
up --dry-run
provision --step / --from / --list
status --json
```

### Provisioning

```text
instance ready 待ち（ネットワークの割り当てを含む）
bootstrap（既定 + 上書き + 無効化）
run ステップ（コンテナ内実行）
ansible ステップ（community.general.incus / 一時inventory）
idev変数の注入
部分実行（--step / --from / --list）
```

### Configuration

```text
schema
runtime
project
instance (image / profiles / config / devices)
workspace (source / target / idmap)
bootstrap
provision (run / ansible)
```

### 明示的にMVPに含まないもの

```text
packages / features に相当する高水準フィールド（恒久的な非目標）
idev同梱の Role / Profile（恒久的な非目標）
requirements.yml の自動適用
```

---

## 9.2 MVP以降の候補

MVP完成後に以下を実装した。

```text
up --dry-run / up --restart
provision --step / --from / --list（部分実行）
idev exec
idev snapshot（create / list / restore / delete）
dev.yml の shell（user / command / cwd）
dev.yml の incus.project
dev.yml の volumes（永続ボリューム）
dev.yml の secrets（ホストからの注入）
project.scope（複数checkout / ブランチ別instance）
galaxy ステップ型（ansible-galaxy install）
設定・deviceの削除追従（user.incus-dev.managed / .devices）
Incus Go client library への移行（incus コマンドへの依存を解消）
```

残る候補：

```text
追加のステップ型（必要が生じた場合のみ）
```

### 対応しないもの

以下は **恒久的な非目標** である。「いつか」ではなく、やらない。
いずれも **workspaceの共有方式を別に設計しないと成立しない** ものであり、
それは「手元のマシンにプロジェクト単位の開発環境を作る」という
このツールの目的から外れる。

**remoteのIncus。**

workspaceはホスト側のパスのbind mountであり、そのパスはremoteの向こう側には
存在しない。操作対象は常にローカルのIncusに固定する
（[05-incus.md](05-incus.md) 5.6）。フラグも `incus.Target` のフィールドも
持たない。

**virtual-machine。**

virtual-machine では bind mount が使えず、virtiofs / 9p となる。
`raw.idmap` や disk の `shift` もコンテナ固有の仕組みで意味を持たない。
instanceは常にコンテナとし、`instance.type` という設定項目も持たない
（[03-configuration.md](03-configuration.md) 3.6.2）。

「渡せるが未検証」という中途半端な状態を残さない。
動くと期待した利用者が分かりにくい形で失敗するだけだからである。

### 複数workspaceについて

`instance.devices` でホストのディレクトリを追加マウントでき、
workspaceと同じuid/gid対応付けが適用される（[03-configuration.md](03-configuration.md) 3.7.3）。
専用の構文は設けない。

---

## 9.3 開発優先順位

### Phase 0

Goプロジェクトの初期化。

```text
go.mod
cmd/idev
internal/ の骨格
Makefile
CI (gofmt / go vet / go test)
```

以下が成功する状態にする。

```bash
go build ./cmd/idev
idev --version
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
idev validate
```

---

### Phase 2

Incus基本操作。

```text
create / start / stop / delete
profile 存在確認
config / devices の適用
workspace mount（idmap）
idev管理情報
```

以下を成立させる。

```bash
idev up        # provisionなし
idev status
idev shell
idev destroy
```

---

### Phase 3

実行機構（idevの中核）。

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
idev変数の注入
ansible.cfg の扱い
```

---

### Phase 5

運用系。

```text
idev rebuild
idev provision の再実行検証
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
- `idev`（単一バイナリの配置のみ）
- Ansible（Ansibleを使うサンプルを検証する場合）

### 基本フロー

任意のテストプロジェクトで、

```bash
git clone <repository>
cd <repository>

idev validate
idev up
idev status
idev shell
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
idev provision
idev provision
```

を連続実行して正常終了すること。

### 破棄

```bash
idev destroy --force
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

1. idevリポジトリに Ansible Role / Incus Profile が存在しない
2. idevバイナリが同梱するアセットは JSON Schema のみである
3. あるプロジェクトの `.incus-dev/` を別の空リポジトリへコピーし、
   `idev up` を実行すると同じ環境が再現される
4. idevを新しいバージョンへ更新しても、既存プロジェクトの
   構築内容（導入されるパッケージやツール）が変化しない
