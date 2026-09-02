# 1. 概要と設計目標

## 1.1 概要

本プロジェクトは、Incusを利用してプロジェクト単位の開発環境を再現可能な形で構築・管理するためのCLIツール `idev` を提供する。

各ソフトウェアプロジェクトは、自身のGitリポジトリ内に開発環境定義を保持する。

利用者は対象リポジトリをcloneした後、以下のような操作だけで開発環境を構築できることを目標とする。

```bash
git clone <repository>
cd <repository>

idev up
idev shell
```

### 責務分担

本ツールでは、以下の責務を明確に分離する。

- プロジェクトリポジトリ (`.incus-dev/`)
  - **どのようなコンテナを作るか** を宣言する
  - **コンテナ内部をどう構成するか** の手順を保持する
- `idev`（idev CLI）
  - Incus instanceのライフサイクルを管理する
  - workspaceをコンテナへマウントする
  - コンテナをbootstrapする
  - `.incus-dev/` 配下で宣言された手順を、定義された順序で実行する
- Incus
  - コンテナ、リソース、デバイス、マウント、ネットワークなどを実行する

**idevは環境固有の内容を一切持たない。**

idevはAnsible Role、Incus Profile、言語ランタイムの定義などを同梱しない。
それらはすべてプロジェクト側の `.incus-dev/` に存在する。

`idev` はGoで実装し、単一バイナリとして配布する。詳細は [07-implementation.md](07-implementation.md) を参照。

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
idev up
```

ホスト側に必要なのは、少なくとも以下とする。

- Incus
- `idev`（単一バイナリ）
- プロジェクトが利用するprovisionerの実行環境（Ansibleを使う場合はAnsible）

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

`.incus-dev/` 自体もworkspaceの一部としてコンテナから見えるため、
provisioning用のスクリプトはコンテナ内から直接参照できる。

---

### REQ-004: SSHを不要とすること

プロビジョニングのために各コンテナへSSH Serverを導入してはならない。

コンテナ内でのコマンド実行はIncusの提供する経路のみを使用する。

```text
idev CLI
   │
   ▼
Incus API (daemon)
   │
   ▼
Container
```

Ansibleを使用する場合も、`community.general.incus` connection pluginを使用し、
SSHを経由しない。

---

### REQ-005: 再実行可能なプロビジョニング

以下を複数回実行しても、同じ手順が同じ順序で再実行されること。

```bash
idev provision
idev provision
idev provision
```

idevが保証するのは「同じ手順を同じ順序で再実行すること」までである。

**個々の手順が冪等であることはプロジェクト側の責務とする。**

Ansibleを使用する場合は、可能な限り `shell` や `command` に依存せず、
Ansible Moduleを使用することを推奨する。

---

### REQ-006: 単一バイナリでの配布

`idev` 本体は、実行時に言語ランタイムやパッケージマネージャを要求してはならない。

Goで実装し、静的リンクされた単一バイナリとして配布する。

（Ansibleを使うかどうかはプロジェクトの選択であり、`idev` 本体の依存ではない。）

---

### REQ-007: idevは環境固有の資産を持たないこと

idevは以下を同梱してはならない。

- Ansible Role
- Incus Profile
- 言語ランタイムやツールの導入手順
- 特定OS・特定ディストリビューションを前提とした処理

理由：

- idevが用意したProfileやRoleに依存すると、それが存在しない環境で再現できない
- プロジェクトによっては不要なものまで導入されてしまう
- idevの更新がプロジェクトの環境を意図せず変化させてしまう

idevが持ってよいのは以下に限る。

- Incus instanceのライフサイクル操作
- workspaceのマウント
- bootstrap（provisionerを動かすための最小限の準備）
- `.incus-dev/` 配下で宣言された手順の実行
- 設定ファイルの解釈とvalidation

**「あるプロジェクトの開発環境を再現するために必要な情報が、
すべて `.incus-dev/` の中にある」状態を維持する。**

例外は「bootstrapの既定動作」のみであり、これもプロジェクト側から上書き可能とする
（[06-provisioning.md](06-provisioning.md) 参照）。

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

また、以下は **恒久的に非目標** とする（REQ-007）。

- idevによる共通Ansible Roleの提供
- idevによる共通Incus Profileの提供
- `features:` のような、idev側の実装に紐づく高水準の環境記述

再利用可能なprovisioning資産が必要な場合は、idevとは独立した
Ansible CollectionやGitリポジトリとして配布し、プロジェクトが明示的に取り込む。

ただし、将来の拡張を妨げる設計にはしない。

---

## 1.4 全体アーキテクチャ

```text
                    Project Git Repository
                            │
                     .incus-dev/
                       ├── dev.yml          何を作り、何を実行するか
                       ├── ansible/         プロジェクト所有のplaybook
                       └── scripts/         プロジェクト所有のスクリプト
                            │
                            ▼
              ┌─────────────────────────┐
              │      idev CLI (Go)      │
              │                         │
              │ up / provision / shell  │
              │ status / destroy        │
              │ rebuild / validate      │
              └───────┬─────────┬───────┘
                      │         │
      instance        │         │  bootstrap
      config          │         │  + provision steps
      devices         │         │
      workspace       ▼         ▼
              ┌───────────┐ ┌──────────────────┐
              │   Incus   │ │  Step Executor   │
              │           │ │                  │
              │ create    │ │  run   (in ctr)  │
              │ start     │ │  ansible (host)  │
              │ device    │ │                  │
              │ config    │ │                  │
              └─────┬─────┘ └────────┬─────────┘
                    │                │
                    └────────┬───────┘
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

```text
Project repository (.incus-dev/)
       │
       │ What & How
       ▼
    idev CLI
       │
       │ Orchestration only
       │
       ├─────────────────────┐
       ▼                     ▼
     Incus              Step Executor
       │                     │
       │ Container runtime   │ run / ansible
       │                     │
       └──────────┬──────────┘
                  ▼
           Dev Container
                  │
                  ▼
             /workspace
                  │
                  ▼
          Project Sources
```

**Projectは「何が必要か」と「どう構成するか」を定義する。**

**`idev` は「どの順序で構築・実行するか」だけを管理する。**

**Incusは「コンテナをどう実行するか」を管理する。**

`idev` が「どう構成するか」を持ち始めた時点でこの境界は崩れる。
これを避けることを、本システムの最重要設計原則とする。
