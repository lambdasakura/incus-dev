# 7. AIコーディングツールからの利用

本ツールは Codex や Claude Code のようなAIエージェントから
操作されることを想定して設計されている。

## 7.1 基本方針

エージェントに使わせるのは以下だけでよい。

```bash
idev up
idev provision
idev shell -- <command>
idev status --json
```

Incusの内部（instance名、device、profile）を知らなくても利用できる。

環境を変更したい場合、エージェントが編集すべき対象は
**`.incus-dev/` 配下のファイルだけ** である。この一貫性が
「どこを直せばよいか」の判断を単純にする。

## 7.2 非対話で動く

以下は確認を求めず実行できる。

```bash
idev up
idev provision
idev status
idev validate
```

破壊操作のみ確認するが、フラグで省略できる。

```bash
idev destroy --force
idev rebuild --force
```

## 7.3 終了コードで判定できる

```bash
idev up || exit 1
```

`idev shell -- <command>` はコンテナ内コマンドの終了コードをそのまま返すため、
テストの成否をそのまま扱える。

```bash
idev shell -- make test        # テストが失敗すれば非0
```

出力は端末以外へ渡しても壊れないため、パイプで加工してよい。

```bash
idev shell -- go test ./... 2>&1 | tail -20
```

## 7.4 状態を機械可読に取れる

```bash
idev status --json
```

```json
{
  "project": "my-project",
  "instance": "dev-my-project",
  "status": "Running",
  "exists": true,
  "managed": true,
  "workspace": "/workspace",
  "provision_steps": 2
}
```

```bash
# 未構築なら構築する
[ "$(idev status --json | jq -r .exists)" = "true" ] || idev up
```

## 7.5 プロジェクトの指示ファイルへ書く例

`CLAUDE.md` や `AGENTS.md` に以下のように書いておくと、
エージェントがホスト側でビルドを試みるのを防げる。

```markdown
## 開発環境

ビルド・テスト・実行はIncusコンテナ内で行う。ホスト側で直接実行しない。

    idev up                    # 環境の構築（初回・設定変更後）
    idev shell -- make test    # テスト
    idev shell -- make build   # ビルド
    idev shell                 # 対話シェル

環境に必要なものを追加する場合は `.incus-dev/dev.yml` の provision を編集し、
`idev provision` で反映する。ホストへツールを追加しない。

provisionステップは再実行される前提で、冪等に書くこと。
```

## 7.6 注意

- `idev up` は既存のinstanceを破壊しない。環境を作り直したい場合のみ
  `idev rebuild --force` を使う
- `idev destroy` はコンテナだけを削除し、ホスト側のソースツリーには触れない
- エージェントに `incus` コマンドを直接使わせる必要はない。
  必要になった場合、それは `dev.yml` で表現できていないということなので、
  まず `dev.yml` 側での表現を検討する
