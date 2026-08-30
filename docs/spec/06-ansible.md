# 6. Ansible統合

## 6.1 接続方式

原則として、

```text
community.general.incus
```

connection pluginを使用する。

SSHを利用しない。

一時Inventoryはdev CLIが生成する。

例：

```yaml
all:
  hosts:
    dev:
      ansible_host: dev-example-project
      ansible_connection: community.general.incus
```

Incus全体を列挙するDynamic Inventoryは初期実装では使用しない。

理由：

本ツールが管理する対象は、

```text
現在操作しているGit project
```

に対応するinstanceであり、Incus server全体ではないため。

---

## 6.2 Bootstrap

通常のAnsible Module実行にPythonが必要な環境を考慮する。

初期bootstrapは `ansible.builtin.raw` を使用してよい。

例：

```yaml
---
- name: Bootstrap target
  hosts: dev
  gather_facts: false

  tasks:
    - name: Ensure Python is installed
      ansible.builtin.raw: |
        command -v python3 >/dev/null 2>&1 ||
        (apt-get update && apt-get install -y python3)
      changed_when: false
```

bootstrap完了後は通常のAnsible Moduleを使用する。

なお、ここで要求されるPythonは **コンテナ内でAnsible Moduleを動作させるため** のものであり、
dev CLI本体（Go）の依存ではない。

---

## 6.3 cloud-init

初期実装ではcloud-initを必須にしない。

原則：

```text
Incus standard image
        ↓
Incus start
        ↓
Ansible bootstrap
        ↓
Ansible provision
```

とする。

cloud-initへの依存を減らす理由：

- 初回起動時のみの処理となる
- `dev provision` との責務が重複する
- Ansibleによる再適用の方が開発環境管理に適している
- デバッグ経路を単純化できる

将来必要になった場合はoptional featureとして追加可能とする。

---

## 6.4 Feature実装

例えば、

```yaml
features:
  python:
    version: "3.13"
```

は内部的に、

```text
ansible/roles/python
```

へマッピングする。

共通Playbook例：

```yaml
---
- name: Provision development environment
  hosts: dev
  gather_facts: true

  roles:
    - role: common

  tasks:
    - name: Install additional packages
      ansible.builtin.apt:
        name: "{{ dev_packages }}"
        state: present
      when: dev_packages | length > 0

    - name: Configure Python
      ansible.builtin.include_role:
        name: python
      when: dev_features.python is defined

    - name: Configure Node.js
      ansible.builtin.include_role:
        name: nodejs
      when: dev_features.nodejs is defined

    - name: Configure Go
      ansible.builtin.include_role:
        name: golang
      when: dev_features.golang is defined

    - name: Configure Rust
      ansible.builtin.include_role:
        name: rust
      when: dev_features.rust is defined

    - name: Configure Docker
      ansible.builtin.include_role:
        name: docker
      when: dev_features.docker is defined
```

---

## 6.5 Ansible Role設計

各Roleは独立して冪等であること。

例：

```text
ansible/roles/python/
├── defaults/
│   └── main.yml
├── tasks/
│   └── main.yml
├── handlers/
│   └── main.yml
├── templates/
└── files/
```

最低限、

```text
defaults/
tasks/
```

を使用する。

---

## 6.6 Role変数

Feature設定をそのままRoleへ渡す。

例えば、

```yaml
features:
  python:
    version: "3.13"
```

であれば、Python Roleから、

```yaml
dev_features.python.version
```

として参照可能とする。

必要に応じRole内部で、

```yaml
python_version: "{{ dev_features.python.version }}"
```

へ変換してよい。

dev CLIは `dev.yml` の該当部分をJSONへ変換し、
`--extra-vars` または一時varsファイル経由でPlaybookへ渡す。

---

## 6.7 OSサポート

初期ターゲット：

```text
Ubuntu 24.04 LTS
```

を推奨する。

初期段階から複数distribution対応を過剰に抽象化しない。

ただしAnsible Role内で、

```yaml
ansible_facts.distribution
```

等を利用できるため、後からDebianなどを追加可能な設計とする。

---

## 6.8 Ansible操作層

Ansible関連処理を `internal/ansible` へ集約する。

最低限、以下を分離する。

```go
package ansible

type Runner interface {
    Bootstrap(ctx context.Context, target Target) error
    Provision(ctx context.Context, target Target, vars Vars) error
    RunProjectPlaybook(ctx context.Context, target Target, p ProjectPlaybook) error
}
```

temporary inventoryおよび一時varsファイルの生成もこの層で管理してよい。

一時ファイルは `os.MkdirTemp` で作成し、`defer` で確実に削除する。

Playbook・Roleの実体は、同梱アセットの展開先を参照する
（[02-repository-layout.md](02-repository-layout.md) 参照）。
