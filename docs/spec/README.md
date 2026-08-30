# Incus Development Environment Toolkit 仕様書

Incusを利用してプロジェクト単位の開発環境を再現可能な形で構築・管理する
CLIツール（`dev`）の仕様書。

dev CLIはGoで実装し、単一バイナリとして配布する。
コンテナ内部の構成管理はAnsibleが担当する。

## 目次

| # | ドキュメント | 内容 |
| --- | --- | --- |
| 1 | [01-overview.md](01-overview.md) | 概要、必須要件(REQ)、非目標、全体アーキテクチャ、設計原則 |
| 2 | [02-repository-layout.md](02-repository-layout.md) | 実装言語の選定、dev CLI側／プロジェクト側のリポジトリ構成、アセット同梱、配布 |
| 3 | [03-configuration.md](03-configuration.md) | `.incus-dev/dev.yml` の全設定項目 |
| 4 | [04-cli.md](04-cli.md) | 各コマンドの仕様、ログ、エラー、exit code、AI利用 |
| 5 | [05-incus.md](05-incus.md) | instance命名、project／remote、Profile、Incus操作層 |
| 6 | [06-ansible.md](06-ansible.md) | 接続方式、bootstrap、Feature実装、Role設計、Ansible操作層 |
| 7 | [07-implementation.md](07-implementation.md) | Go実装ガイドライン、外部コマンド実行、config parser、開発時の原則 |
| 8 | [08-testing.md](08-testing.md) | Unit Test、Integration Test、CI |
| 9 | [09-roadmap.md](09-roadmap.md) | MVP、MVP以降の候補、開発優先順位、完成条件 |

## 読む順序

- 全体像を把握したい: 1 → 4 → 3
- 実装を始める: 2 → 7 → 9
- 環境定義を書きたい（利用者）: 3 → 4
