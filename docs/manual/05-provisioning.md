# 5. 環境構築手順の書き方

`idev` が持つのは「宣言された手順を、宣言された順序で実行する」機構だけである。
**何を実行するかはプロジェクトが決める。**

## 5.1 実行順序

```text
instance作成 / 起動
        ↓
コマンドを実行できるまで待機
        ↓
bootstrap
        ↓
provision[0] → provision[1] → ...
```

`idev up` も `idev provision` も、この順序で毎回実行する。
途中で失敗した時点で停止し、後続のステップは実行されない。

失敗したステップは位置と名前で特定できる。

```text
[idev] error: provision step 2/3: install deps: exec in dev-my-project: ... (exit code 1)
```

---

## 5.2 `run` ステップ

コンテナ内でスクリプトを実行する。

### 短縮形

```yaml
provision:
  - run: apt-get update
```

### 完全形

```yaml
provision:
  - name: install packages
    run: |
      apt-get update
      apt-get install -y --no-install-recommends jq make
    shell: /bin/sh                 # 既定 /bin/sh
    cwd: /workspace                # 作業ディレクトリ（コンテナ内）
    user: root                     # 実行ユーザー
    env:
      DEBIAN_FRONTEND: noninteractive
```

| フィールド | 説明 |
| --- | --- |
| `run` | 実行するスクリプト本文（必須） |
| `name` | 表示名。ログとエラーに使われる |
| `shell` | スクリプトを解釈するシェル |
| `cwd` | 作業ディレクトリ |
| `user` | 実行ユーザー。数値uidも名前も指定できる |
| `env` | 追加の環境変数 |

### スクリプトファイルを実行する

`.incus-dev/` はworkspaceの一部としてコンテナから見えるため、
スクリプトをファイルとして持てる。

```yaml
provision:
  - name: setup
    run: sh /workspace/.incus-dev/scripts/setup.sh
```

```sh
#!/bin/sh
# .incus-dev/scripts/setup.sh
set -eu

echo "setting up ${DEVKIT_PROJECT_NAME}"
command -v jq >/dev/null 2>&1 || apt-get install -y jq
```

処理が数行を超えるならファイルへ切り出した方が、
シェルの構文チェックやエディタの支援を受けられる。

---

## 5.3 `ansible` ステップ

ホスト側で `ansible-playbook` を実行する。SSHは使わない。

```yaml
provision:
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml    # 必須。project root基準
      vars: .incus-dev/ansible/vars.yml        # 任意
      inventory: .incus-dev/ansible/hosts.yml  # 任意（追加のinventory）
      tags: [setup]                            # 任意
      skip_tags: [slow]                        # 任意
      extra_args: ["--diff"]                   # 任意
```

### playbookの書き方

対象ホスト名は `dev` で固定である。idev が一時inventoryを生成して渡す。

```yaml
# .incus-dev/ansible/site.yml
---
- name: Provision development environment
  hosts: dev
  gather_facts: true

  roles:
    - role: base

  tasks:
    - name: Install packages
      ansible.builtin.apt:
        name:
          - protobuf-compiler
          - libssl-dev
        state: present
```

生成されるinventoryは以下に相当する。

```yaml
all:
  children:
    devkit:
      hosts:
        dev:
          ansible_host: dev-my-project
          ansible_connection: community.general.incus
          ansible_incus_remote: local
          ansible_incus_project: default
```

### Roleとcollection

Roleはプロジェクトの所有物である。idev は role path を注入しないため、
`ansible.cfg` で指定する。

```ini
# .incus-dev/ansible/ansible.cfg
[defaults]
roles_path = .incus-dev/ansible/roles
stdout_callback = yaml
```

このファイルが存在すれば、idev は `ANSIBLE_CONFIG` として使用する。

外部のcollectionが必要な場合は `requirements.yml` を置き、
セットアップ手順やCIで導入する（idev は自動導入しない）。

```bash
ansible-galaxy install -r .incus-dev/ansible/requirements.yml
```

### コンテナ内のPython

Ansible Moduleの実行にはコンテナ内にPythonが必要である。

`bootstrap` を省略していて `ansible` ステップがある場合、
idev はDebian系を前提とした既定bootstrapでPythonの導入を試みる。

```sh
command -v python3 >/dev/null 2>&1 || (apt-get update && apt-get install -y python3)
```

Debian系以外のイメージでは失敗するため、`bootstrap` を明示する。

```yaml
instance:
  image: images:fedora/41

bootstrap:
  - run: command -v python3 >/dev/null 2>&1 || dnf install -y python3
```

Pythonが最初から入っているイメージ（`images:ubuntu/noble` など）であれば、
既定bootstrapは確認だけで終わる。

---

## 5.4 idevが渡す変数

instance名やパスをハードコードしなくて済むよう、実行時の情報が渡される。

### `run` ステップ（環境変数）

```text
DEVKIT_PROJECT_NAME       プロジェクト名
DEVKIT_INSTANCE           instance名
DEVKIT_WORKSPACE          コンテナ内のworkspaceパス
DEVKIT_WORKSPACE_SOURCE   ホスト側のproject rootパス
DEVKIT_INCUS_REMOTE       Incus remote
DEVKIT_INCUS_PROJECT      Incus project
```

```yaml
provision:
  - run: |
      cd "$DEVKIT_WORKSPACE"
      make setup
```

ステップの `env` で同じ名前を指定した場合はそちらが優先される。

### `ansible` ステップ（変数）

```yaml
devkit_project_name: my-project
devkit_instance: dev-my-project
devkit_workspace: /workspace
devkit_workspace_source: /home/you/src/my-project
devkit_incus_remote: local
devkit_incus_project: default
```

```yaml
- name: Configure shell to start in the workspace
  ansible.builtin.copy:
    content: "cd {{ devkit_workspace }}\n"
    dest: /etc/profile.d/workspace.sh
    mode: "0644"
```

これらは `vars` より先に渡されるため、プロジェクト側で上書きできる。

---

## 5.5 再実行できるように書く

`idev provision` は繰り返し実行される。冪等性を保つのはプロジェクトの責任である。

| 方式 | 書き方 |
| --- | --- |
| Ansible | `shell` / `command` より Ansible Module を優先する |
| シェル | 状態を確認してから変更する |

```sh
# 導入済みなら何もしない
command -v jq >/dev/null 2>&1 || apt-get install -y jq

# 追記ではなく生成する
cat > /etc/profile.d/workspace.sh <<EOS
cd ${DEVKIT_WORKSPACE}
EOS

# 存在確認してから作る
[ -d /opt/tools ] || mkdir -p /opt/tools
```

確認は2回続けて実行するだけでよい。

```bash
idev provision && idev provision
```

---

## 5.6 どちらを使うか

| | `run` | `ansible` |
| --- | --- | --- |
| ホスト側の追加要件 | なし | ansible-playbook、community.general |
| コンテナ側の要件 | シェルのみ | Python |
| 冪等性 | 自分で書く | Moduleが面倒を見る |
| 向いている規模 | 数十行まで | Roleを分けたい規模 |

小さなプロジェクトは `run` だけで足りることが多い。
両方を混在させることもできる。

```yaml
provision:
  - name: apt update
    run: apt-get update

  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml

  - name: project setup
    run: |
      cd /workspace
      make deps
```
