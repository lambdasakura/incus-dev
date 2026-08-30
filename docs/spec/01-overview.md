# 1. 概要と設計目標

## 1.1 概要

本プロジェクトは、Incusを利用してプロジェクト単位の開発環境を再現可能な形で構築・管理するためのCLIツールを提供する。

各ソフトウェアプロジェクトは、自身のGitリポジトリ内に開発環境定義を保持する。

利用者は対象リポジトリをcloneした後、以下のような操作だけで開発環境を構築できることを目標とする。

```bash
git clone <repository>
cd <repository>

dev up
dev shell
```

開発環境のインフラ部分はIncusで管理し、コンテナ内部のOS・パッケージ・開発ツールの構成はAnsibleで管理する。

本ツールでは、以下の責務を明確に分離する。

- プロジェクトリポジトリ
  - 「どのような開発環境が必要か」を宣言する
- dev CLI
  - プロジェクト定義を解釈し、IncusとAnsibleをオーケストレーションする
- Incus
  - コンテナ、リソース、デバイス、マウント、ネットワークなどを管理する
- Ansible
  - コンテナ内部のOS設定、パッケージ、ランタイム、開発ツールを構成する

dev CLIはGoで実装し、単一バイナリとして配布する。詳細は [07-implementation.md](07-implementation.md) を参照。

---

## 1.2 必須要件

システムは以下を満たさなければならない。

### REQ-001: プロジェクト単位の環境定義

開発環境定義は対象プロジェクト自身のGitリポジトリに格納する。

```text
my-project/
├── .incus-dev/
│   └── dev.yml
├── src/
├── README.md
└── ...
```

中央管理リポジトリにプロジェクト固有設定を保持する構成にはしない。

---

### REQ-002: clone後に環境を再現可能であること

原則として以下だけで環境を構築できること。

```bash
git clone <repository>
cd <repository>
dev up
```

ホスト側に必要なのは、少なくとも以下とする。

- Incus
- dev CLI（単一バイナリ）
- Ansible
- dev CLIが要求する補助ツール

プロジェクトごとの手作業によるIncus設定は要求しない。

---

### REQ-003: ソースコードをホストとコンテナで共有すること

プロジェクトのGit working treeはコピーせず、Incusのdisk deviceを使用してコンテナへbind mountする。

デフォルトでは、

```text
Host:

/home/user/src/project-a

        ↓

Container:

/workspace
```

とする。

これにより、

- IDE
- Git
- Codex
- Claude Code

などはホスト側から利用可能としながら、ビルド・テスト・実行はIncusコンテナ内で行える構成を可能にする。

---

### REQ-004: SSHを不要とすること

Ansibleによるプロビジョニングのために各コンテナへSSH Serverを導入してはならない。

AnsibleからIncusへは原則として、

```text
community.general.incus
```

connection pluginを使用する。

概念上は以下の経路とする。

```text
Ansible
   │
   ▼
Incus CLI / Incus daemon
   │
   ▼
Container
```

---

### REQ-005: 冪等なプロビジョニング

コンテナ内部の構成管理は原則としてAnsibleで実装する。

以下を複数回実行しても正常に収束すること。

```bash
dev provision
dev provision
dev provision
```

可能な限り `shell` や `command` に依存せず、Ansible Moduleを使用する。

---

### REQ-006: 単一バイナリでの配布

dev CLI本体は、実行時に言語ランタイムやパッケージマネージャを要求してはならない。

Goで実装し、静的リンクされた単一バイナリとして配布する。

ホスト側にPython仮想環境やnpm等のセットアップを要求しない。

（Ansible自体はホストにインストールされている前提とするが、dev CLI本体の依存とは分離して扱う。）

---

## 1.3 非目標

初期バージョンでは以下を主目的としない。

- Kubernetes上への開発環境構築
- VM中心の開発環境
- Public Cloud上のVMプロビジョニング
- TerraformによるIncus全体のIaC管理
- Incusクラスタそのものの構築
- IDE固有のRemote Development機能
- 本番環境の構築
- 開発者ごとのSecret管理基盤
- GUI

ただし、将来の拡張を妨げる設計にはしない。

---

## 1.4 全体アーキテクチャ

```text
                    Project Git Repository
                            │
                  .incus-dev/dev.yml
                            │
                 Development declaration
                            │
                            ▼
              ┌─────────────────────────┐
              │      dev CLI (Go)       │
              │                         │
              │ up                      │
              │ provision               │
              │ shell                   │
              │ status                  │
              │ destroy                 │
              │ rebuild                 │
              └───────┬─────────┬───────┘
                      │         │
             Incus    │         │ Ansible
                      ▼         ▼
              ┌───────────┐ ┌─────────────┐
              │ Instance  │ │ Common Roles│
              │ Profile   │ │ Features    │
              │ Devices   │ │ Project     │
              │ Resources │ │ Provision   │
              └─────┬─────┘ └──────┬──────┘
                    │              │
                    └──────┬───────┘
                           ▼
                 Development Container
                           │
                           ▼
                      /workspace
                           │
                           ▼
                  Project Git Working Tree
```

---

## 1.5 設計原則まとめ

本システムでは、最終的に以下の責務分担を維持する。

```text
Project repository
       │
       │ What
       ▼
.incus-dev/dev.yml
       │
       ▼
     dev CLI
       │
       │ Orchestration
       │
       ├─────────────────┐
       ▼                 ▼
     Incus             Ansible
       │                 │
       │ Infrastructure  │ OS / Tools
       │                 │
       └────────┬────────┘
                ▼
         Dev Container
                │
                ▼
           /workspace
                │
                ▼
        Project Sources
```

**Projectは「何が必要か」を定義する。**

**dev CLIは「どの順序で構築するか」を管理する。**

**Incusは「コンテナをどう実行するか」を管理する。**

**Ansibleは「コンテナ内部をどう構成するか」を管理する。**

この境界を崩さないことを、本システムの最重要設計原則とする。
