# 6. Bootstrap と Provisioning

devkitは「何を実行するか」を持たない。
`.incus-dev/dev.yml` に宣言されたステップを、定義された順序で実行するだけである。

本章はその実行機構を規定する。

---

## 6.1 実行順序

```text
instance 作成 / 起動
        ↓
instance ready 待ち
        ↓
bootstrap            （run ステップのみ）
        ↓
provision[0]
provision[1]
        ...
        ↓
完了
```

`idev up` と `idev provision` はいずれもbootstrapから実行する。

bootstrapは軽量かつ冪等であることを前提とし、毎回実行される。

いずれかのステップが失敗した時点で、後続を実行せず全体を失敗とする。

---

## 6.2 instance ready 待ち

コンテナ起動直後はプロセスの初期化が完了していない場合がある。

devkitはbootstrapを開始する前に、以下の2つを待機する。

**1. コマンドを実行できること**

- 実行可能性の確認は軽量なコマンド実行（例: `true`）で行う
- タイムアウトを設け、超過した場合は明示的に失敗する

**2. ネットワークアドレスが割り当てられること**

コマンドを実行できる時点では、まだアドレスが割り当てられていない。
Incusの既定のブリッジではIPv6(ULA)が先に付き、IPv4のDHCPが完了して
デフォルトルートが入るまで外部へ出られない。

ここを待たないと、パッケージ導入を伴うプロジェクトは
**初回の `idev up` が必ず失敗する**。

- IPv4のグローバルアドレスが割り当てられるまで待つ
- IPv6のみが付いた場合、短い猶予の後に先へ進む（IPv6のみの環境のため）
- NICを持たないinstanceでは待たない
- アドレスが1つも付かないまま時間切れになった場合は、
  警告を表示して続行する。アドレスが現れない構成もありうるため、
  ここで停止すると回避手段が無くなる

判定はIncus側の情報（instanceの状態）のみで行い、
コンテナ内部のコマンドやファイルには依存しない（REQ-007）。

---

## 6.3 Bootstrap

### 6.3.1 目的

bootstrapは **provisionerを動かすための最小限の準備** に限定する。

開発環境の内容（パッケージ、ランタイム、ツール）をbootstrapで導入してはならない。
それらは `provision` の責務である。

### 6.3.2 既定動作

`dev.yml` に `bootstrap` の記述が無い場合：

| 条件 | 既定bootstrap |
| --- | --- |
| `provision` に `ansible` ステップが1つ以上ある | Python3の存在を確認し、無ければ導入を試みる |
| それ以外 | 何もしない |

既定bootstrapの概念的な内容：

```sh
command -v python3 >/dev/null 2>&1 ||
  (apt-get update && apt-get install -y python3)
```

これはAnsible Moduleの実行にコンテナ内のPythonが必要なためである。

ただしこの既定動作はDebian系イメージを前提とするため、
**REQ-007に対する唯一の例外** として扱い、以下を満たす。

- Debian系以外のイメージでは失敗しうることを明記する
- プロジェクト側から完全に上書き可能とする
- 失敗時、`bootstrap` を明示的に定義するよう促すエラーメッセージを表示する
  （既定bootstrapであることが分かる名前を付け、エラーに含める）

### 6.3.3 上書き

`bootstrap` を記述した場合、既定動作は実行されない。

```yaml
bootstrap:
  - name: ensure python
    run: |
      command -v python3 >/dev/null 2>&1 || dnf install -y python3
```

無効化：

```yaml
bootstrap: []
```

### 6.3.4 制約

- bootstrapのステップは `run` のみ（`ansible` は使用できない）
- bootstrap時点ではworkspaceは既にマウントされているため、
  `.incus-dev/` 配下のスクリプトを参照してもよい

---

## 6.4 `run` ステップの実行

コンテナ内でスクリプトを実行する。

概念的には以下と等価とする。

```bash
incus exec <instance> --env KEY=VALUE ... -- <shell> -c '<script>'
```

要件：

- 既定シェルは `/bin/sh`。`shell` フィールドで変更可能
- 終了コードが0以外の場合は失敗とする
- stdout / stderrはdevkitの出力へそのまま中継する
  （[04-cli.md](04-cli.md) 4.9。長時間かかる処理の進行が見えなくなるため要約しない）
- `env` は 3.10.1 のdevkit変数に追記される（プロジェクト指定が優先）
- `env` の値はSecretを含みうるため、ログやエラーへは値を出さない
- 対話的入力は行わない（stdinは接続しない）
- TTYは割り当てない（`idev shell` とは異なる）

`cwd` が指定された場合、シェル起動時に当該ディレクトリへ移動する。
存在しない場合は明示的なエラーとする。

---

## 6.5 `ansible` ステップの実行

### 6.5.1 接続方式

SSHを利用しない。

```text
community.general.incus
```

connection pluginを使用する。

このcollectionの導入はホスト側の前提条件とする。
devkitは同梱しない。

未導入が疑われる場合、devkitは以下のような具体的な対処を含むエラーを表示してよい。

```text
ansible-galaxy collection install community.general
```

### 6.5.2 一時Inventory

devkitが生成する。

```yaml
all:
  children:
    devkit:
      hosts:
        dev:
          ansible_host: dev-example-project
          ansible_connection: community.general.incus
          ansible_incus_remote: local
          ansible_incus_project: default
```

- 対象ホストのグループ名・ホスト名は固定とし、仕様として文書化する
  - ホスト: `dev`
  - グループ: `all`, `devkit`
- 一時ファイルとして生成し、実行後に削除する
- プロジェクトが `inventory` を指定した場合、追加のinventoryとして併用する

Incus全体を列挙するDynamic Inventoryは使用しない。

理由：本ツールが管理する対象は、現在操作しているGit projectに対応する
instanceのみであり、Incus server全体ではないため。

### 6.5.3 実行

概念的には以下と等価とする。

```bash
ansible-playbook \
    -i <generated-inventory> \
    [-i <project-inventory>] \
    --extra-vars @<devkit-vars> \
    [--extra-vars @<project-vars>] \
    [--tags ...] [extra_args...] \
    <playbook>
```

要件：

- 作業ディレクトリはproject rootとする
- `.incus-dev/ansible/ansible.cfg` が存在する場合、`ANSIBLE_CONFIG` として使用する
- devkit変数（3.10.2）はプロジェクトのvarsより先に渡し、上書きを許す
- devkitはrole pathやcollection pathを注入しない
  - Role解決はプロジェクトの `ansible.cfg` またはplaybookの配置に従う
- 一時ファイルは実行後に必ず削除する

### 6.5.4 プロジェクト側playbookの例

```yaml
---
- name: Provision development environment
  hosts: dev
  gather_facts: true

  roles:
    - role: base
    - role: python

  tasks:
    - name: Install project dependencies
      ansible.builtin.apt:
        name:
          - protobuf-compiler
          - libssl-dev
        state: present
```

`hosts: dev` はdevkitが生成するinventoryの規約に対応する。

### 6.5.5 collectionの導入

`requirements.yml` の適用はMVPの対象外とする。

プロジェクトは以下のいずれかで対応する。

- ホスト側の前提条件として文書化する
- CI/セットアップ手順で `ansible-galaxy install -r .incus-dev/ansible/requirements.yml` を実行する

将来的に `galaxy` ステップ型の追加を検討する（[09-roadmap.md](09-roadmap.md)）。

---

## 6.6 冪等性の責務

devkitは以下のみを保証する。

- 同じ `dev.yml` に対して、同じステップを同じ順序で実行すること
- ステップの失敗を検出し、後続を実行しないこと

**各ステップが再実行可能であることはプロジェクトの責務とする。**

推奨事項（プロジェクト向けガイダンス）：

- Ansibleを使う場合、`shell` / `command` より Ansible Module を優先する
- `run` を使う場合、以下のような再実行可能な記述にする

```sh
command -v jq >/dev/null 2>&1 || apt-get install -y jq
```

---

## 6.7 OSサポート

devkitは特定のディストリビューションを前提としない。

唯一の例外は 6.3.2 の既定bootstrapであり、これは上書き可能である。

image選択とprovisioning手順の整合はプロジェクトの責務とする。

---

## 6.8 Provisioning操作層

provisioning関連処理を `internal/provision` へ集約する。

```go
package provision

// Env はdevkitが各ステップへ渡す実行文脈。
type Env struct {
    ProjectName     string
    ProjectRoot     string // ホスト側
    Instance        string
    Workspace       string // コンテナ内
    WorkspaceSource string // ホスト側
    Remote          string
    IncusProject    string
}

// Selection は実行するステップの絞り込み（部分実行）。
type Selection struct {
    Only []string // 名前または番号
    From string
}

type Executor struct {
    Incus  incus.Client  // run ステップ用
    Runner runner.Runner // ansible ステップ用
    Logger *slog.Logger
    Stdout io.Writer
    Stderr io.Writer
}

func (e *Executor) Bootstrap(ctx context.Context, cfg *config.Config, env Env) error
func (e *Executor) Provision(ctx context.Context, cfg *config.Config, env Env, sel Selection) error
```

ステップは `config.Step` として宣言的に表現し、`Executor` が種別ごとに
実行する。ステップ型の追加は、`config` 側のデコードと
`Executor` の分岐追加で完結する構造とする。


