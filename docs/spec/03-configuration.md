# 3. 開発環境定義（dev.yml）

ファイル：

```text
.incus-dev/dev.yml
```

## 3.1 基本例

```yaml
schema: 1

runtime:
  version: "1.0"

project:
  name: example-project

instance:
  image: images:ubuntu/24.04

  profiles:
    - dev-base

  resources:
    cpu: 8
    memory: 16GiB

workspace:
  source: .
  target: /workspace

packages:
  - jq
  - cmake
  - ninja-build

features:
  python:
    version: "3.13"

  docker: {}

provision:
  playbook: .incus-dev/ansible/project.yml
  vars: .incus-dev/ansible/vars.yml
```

すべてのフィールドを必須にはしない。

---

## 3.2 schema

```yaml
schema: 1
```

必須。

設定フォーマットのバージョンを示す。

dev CLIは未知のschema versionを検出した場合、処理を継続してはならない。

---

## 3.3 runtime

```yaml
runtime:
  version: "1.0"
```

共通dev runtimeとの互換性を指定する。

将来的にはSemVer形式を使用することを想定する。

目的は以下の問題を防ぐこと。

```text
project commit A
        +
最新dev CLI / 最新Ansible Role

↓

過去と異なる環境
```

---

## 3.4 project

```yaml
project:
  name: example-project
```

`name` は原則必須。

Incus instance名生成などに利用する。

---

## 3.5 instance

```yaml
instance:
  image: images:ubuntu/24.04

  profiles:
    - dev-base

  resources:
    cpu: 8
    memory: 16GiB
```

### 3.5.1 image

Incus image referenceを指定する。

例：

```yaml
image: images:ubuntu/24.04
```

### 3.5.2 profiles

必要なIncus Profileを指定する。

Profileは以下のようなホスト・仮想化レイヤーの設定に限定することを推奨する。

- Network
- Storage
- GPU
- nesting
- security
- device passthrough

以下はProfileで管理しない。

- Python version
- Node.js version
- OS package
- プロジェクト固有ツール

それらはAnsibleで管理する。

### 3.5.3 resources

最低限以下をサポートする。

```yaml
resources:
  cpu: 8
  memory: 16GiB
```

これらはIncus instance configへ変換する。

---

## 3.6 workspace

```yaml
workspace:
  source: .
  target: /workspace
```

### 3.6.1 source

`dev.yml` ではなく、プロジェクトrootを基準に解決する。

```yaml
source: .
```

の場合、Git repository rootを意味する。

### 3.6.2 target

コンテナ内部のmount point。

デフォルト：

```text
/workspace
```

### 3.6.3 mount方式

Incus disk deviceを使用する。

概念例：

```bash
incus config device add \
    <instance> workspace disk \
    source=<project-root> \
    path=/workspace
```

実際のCLI引数については実装時に対象Incus versionで確認すること。

---

## 3.7 packages

簡単な追加パッケージのために以下を提供する。

```yaml
packages:
  - jq
  - cmake
  - ninja-build
```

これらは共通Ansible Playbook内でインストールする。

プロジェクトごとに `project.yml` を書く必要を減らすことが目的である。

---

## 3.8 features

Featureは共通Ansible Roleに対応する。

例：

```yaml
features:
  python:
    version: "3.13"

  nodejs:
    version: "24"

  docker: {}

  rust: {}
```

初期実装では最低限以下を検討する。

- common
- devtools
- python
- nodejs
- golang
- rust
- docker

Featureは将来的に追加可能でなければならない。

Feature名からAnsible Roleへのマッピングは [06-ansible.md](06-ansible.md) を参照。

---

## 3.9 provision

共通Featureだけでは表現できない処理のため、任意のAnsibleファイルをサポートする。

```yaml
provision:
  playbook: .incus-dev/ansible/project.yml
  vars: .incus-dev/ansible/vars.yml
```

これらはoptionalとする。

### 3.9.1 project.yml

例：

```yaml
---
- name: Install project dependencies
  ansible.builtin.apt:
    name:
      - protobuf-compiler
      - libssl-dev
      - postgresql-client
    state: present
```

共通環境構築終了後に実行する。

実行順序は、

```text
Bootstrap
   ↓
common
   ↓
packages
   ↓
features
   ↓
project-specific provisioning
```

とする。

---

## 3.10 将来的な拡張予定フィールド

以下は初期実装では対象外だが、後方互換に追加できる構造としておく。

```yaml
incus:
  remote: local
  project: development

user:
  name: developer

secrets:
  ...
```

詳細は [05-incus.md](05-incus.md)、[07-implementation.md](07-implementation.md) を参照。

---

## 3.11 Secret

以下を `dev.yml` へ直接書くことを推奨しない。

- API key
- Access token
- Password
- Private key

特にGitへcommitされることを前提に設計する。

将来的には、

```yaml
secrets:
```

のような仕組みを追加できるようにするが、初期実装では対象外とする。

Secretを実装する場合は、

- environment variable
- host-side file
- password manager
- secret manager

等から注入する方式を優先する。
