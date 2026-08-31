---
name: incus-devkit
description: idev コマンドと .incus-dev/dev.yml でIncusのプロジェクト専用開発環境を扱うときに使う。環境の新規作成、ビルド・テストのコンテナ内実行、ツールや依存の追加、idev の失敗の切り分け、既存プロジェクトへの導入が対象。「idev」「.incus-dev」「dev.yml」「Incusで開発環境」に触れる作業で参照する。
---

# incus-devkit（idev）の使い方

`idev` はプロジェクト単位の開発環境をIncusコンテナとして構築・管理するCLI。
ホストを汚さずに、リポジトリごとに再現可能な環境を用意するために使う。

## 3つの原則

**1. ビルド・テスト・実行はコンテナ内で行う。**
ホストへツールを入れない。`apt-get` や `npm i -g` をホストで実行しない。

```bash
idev exec -- make test
```

**2. 環境の変更は `.incus-dev/` の中だけで表現する。**
`incus` コマンドを直接叩いて設定を変えない。手で変えた設定は次の
`idev up` で戻される（devkitが管理するキーのみ）か、そもそも記録に残らない。
`incus` を直接使いたくなったら、それは `dev.yml` で表現できていない合図。

**3. provisionステップは冪等に書く。**
`idev provision` は何度でも再実行される前提。`git clone` ではなく
`test -d x || git clone`、`useradd` ではなく `id -u dev >/dev/null 2>&1 || useradd`。

## やりたいこと → コマンド

| やりたいこと | コマンド |
| --- | --- |
| 環境を用意する（初回・`dev.yml` 変更後） | `idev up` |
| コンテナ内でコマンドを実行する | `idev exec -- make test` |
| 対話シェルに入る | `idev shell` |
| provisionだけ流し直す | `idev provision` |
| 何が起きるか先に見る | `idev up --dry-run` |
| 状態を機械可読に取る | `idev status --json` |
| `dev.yml` の妥当性だけ確認する（Incusに触れない） | `idev validate` |
| 作り直す | `idev rebuild --force` |
| 消す（ホストのソースは残る） | `idev destroy --force` |

`idev exec` と `idev shell -- <cmd>` の違いは擬似端末の有無だけ。
**スクリプトやCIからは `idev exec` を使う**（端末の有無で挙動が変わらない）。
どちらもコンテナ内コマンドの終了コードをそのまま返すので、
`idev exec -- make test || exit 1` がそのまま使える。

確認を求めるのは破壊操作（`destroy` / `rebuild` / `snapshot restore|delete`）だけで、
いずれも `--force` で省略できる。他は非対話で動く。

## 新しくプロジェクトへ導入する

1. **既存の構築手順を読む。** README、Dockerfile、CI設定、Makefile から
   「どのOSで」「何を入れて」「どう起動するか」を拾う。
2. **`.incus-dev/dev.yml` を書く。** `templates/dev.yml` を出発点にする。
   - image は Dockerfile やCIに合わせる（`images:ubuntu/24.04`、`images:debian/12`、
     `images:alpine/3.21` など）
   - provisionは **shellで書く**。Ansibleは、そのプロジェクトが既に
     Ansibleを使っているか、手順が複雑で冪等性を自力で保つのが辛い場合だけ
3. `idev validate` → `idev up` の順で確認する。
4. **プロジェクトの `CLAUDE.md` / `AGENTS.md` へ追記する**（後続のエージェント向け）。

```markdown
## 開発環境

ビルド・テスト・実行はIncusコンテナ内で行う。ホストでは実行しない。

    idev up                    # 環境の構築（初回・dev.yml 変更後）
    idev exec -- make test     # テスト
    idev shell                 # 対話シェル

環境に何か足すときは `.incus-dev/dev.yml` の provision を編集して
`idev provision`。ホストへツールを入れない。
```

## 環境に何かを足す

`dev.yml` の `provision` にステップを足して `idev provision`。
instanceは作り直されないので、作業中の状態は失われない。

```yaml
provision:
  - name: go toolchain
    run: |
      test -x /usr/local/go/bin/go && exit 0
      curl -fsSL https://go.dev/dl/go1.25.0.linux-amd64.tar.gz | tar -C /usr/local -xz
```

- `instance.config` や `instance.devices`（CPU・メモリ・追加マウント）を
  変えた場合は `idev provision` ではなく **`idev up`**
- 反映に再起動が要る設定（`security.nesting` など）は警告が出る。
  再起動してよければ `idev up --restart`
- 長いprovisionの一部だけ試すときは `idev provision --list` で番号を見て
  `idev provision --step 3` / `--from 3`

## うまくいかないとき

**軽い順に試す。いきなり `rebuild` しない**（コンテナ内の作業結果が消える）。

```bash
idev validate          # 1. dev.yml の書式・参照パス
idev status            # 2. instanceの状態、devkit管理下か
idev up --dry-run      # 3. 何が適用されるのか
idev provision         # 4. provisionだけやり直す
idev up                # 5. 設定の再適用も含めてやり直す
idev up --verbose      # 6. Incusへの操作とコマンドを見る
idev rebuild --force   # 7. 最後の手段
```

エラー別の対処は [references/troubleshooting.md](references/troubleshooting.md)。
**コンテナ内でパッケージ導入が失敗する場合、ほぼホスト側のネットワーク問題**
（Dockerとの競合）なので、`dev.yml` を書き換える前にそちらを確認する。

## やってはいけないこと

| アンチパターン | 代わりに |
| --- | --- |
| ホストで `apt-get install` してから作業する | `dev.yml` の provision に書く |
| `incus exec` / `incus config set` を直接叩く | `dev.yml` に書いて `idev up` |
| 失敗したらとりあえず `idev rebuild` | 上の順序で切り分ける |
| provisionに `git clone`（2回目で失敗する） | `test -d dir \|\| git clone ...` |
| APIトークンを `dev.yml` に直書き | `secrets:` でホストから注入する |
| 生成物を `/root` や `/tmp` へ置く | `/workspace`（ホストと共有）へ置く |
| ホスト側のパスをコンテナ内で使う | `/workspace` 起点で書く |

## 参照

| 文書 | 内容 |
| --- | --- |
| [references/dev-yml.md](references/dev-yml.md) | `dev.yml` の全項目と既定値 |
| [references/troubleshooting.md](references/troubleshooting.md) | エラー文言から原因と対処を引く |
| [templates/dev.yml](templates/dev.yml) | 注釈つきの雛形 |

`idev <command> --help` が常に正。迷ったらそれを見る。
