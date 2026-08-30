# Incus Development Environment Toolkit 仕様書

Incusを利用してプロジェクト単位の開発環境を再現可能な形で構築・管理する
CLIツール（`dev` / devkit）の仕様書。

## 基本方針

devkitは **Incusコンテナの構築・bootstrap・`.incus-dev/` 配下の実行** に特化する。

Ansible Role、Incus Profile、言語ランタイムの導入手順といった環境固有の内容は
devkitに含めず、すべてプロジェクトの `.incus-dev/` が所有する（REQ-007）。

**「開発環境を再現するために必要な情報が、すべて `.incus-dev/` の中にある」**
状態を維持することが、本仕様の中核である。

dev CLIはGoで実装し、単一バイナリとして配布する。

## 目次

| # | ドキュメント | 内容 |
| --- | --- | --- |
| 1 | [01-overview.md](01-overview.md) | 概要、必須要件(REQ-001〜007)、非目標、全体アーキテクチャ、設計原則 |
| 2 | [02-repository-layout.md](02-repository-layout.md) | 実装言語、devkit側／プロジェクト側の構成、devkitに置かないもの、配布 |
| 3 | [03-configuration.md](03-configuration.md) | `.incus-dev/dev.yml` の全設定項目 |
| 4 | [04-cli.md](04-cli.md) | 各コマンドの仕様、ログ、エラー、exit code、AI利用 |
| 5 | [05-incus.md](05-incus.md) | instance命名、管理情報、Profile、config/device適用、Incus操作層 |
| 6 | [06-provisioning.md](06-provisioning.md) | 実行順序、bootstrap、`run`/`ansible`ステップ、冪等性の責務 |
| 7 | [07-implementation.md](07-implementation.md) | Go実装ガイドライン、config parser、開発時の原則 |
| 8 | [08-testing.md](08-testing.md) | Unit Test、構造の検証、Integration Test、CI |
| 9 | [09-roadmap.md](09-roadmap.md) | MVP、MVP以降の候補、開発優先順位、完成条件 |
| 10 | [10-examples.md](10-examples.md) | プロジェクト側 `.incus-dev/` の構成例 |

## 読む順序

- 設計思想を把握したい: 1 → 6 → 2
- 環境定義を書きたい（利用者）: 10 → 3 → 4
- 実装を始める: 2 → 7 → 9
