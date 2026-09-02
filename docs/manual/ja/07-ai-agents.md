# 7. AIコーディングツールからの利用

本ツールは Codex や Claude Code のようなAIエージェントから
操作されることを想定して設計されている。

*[English version](../07-ai-agents.md)*

## 7.1 基本方針

エージェントに使わせるのは以下だけでよい。

```bash
idev up
idev provision
idev exec -- <command>
idev status --json
```

`idev exec` は擬似端末を割り当てない。端末の有無で挙動が変わらないため、
スクリプトやエージェントからはこちらを使う（対話が要る場合のみ `idev shell`）。

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

`idev exec` / `idev shell` はコンテナ内コマンドの終了コードをそのまま返すため、
テストの成否をそのまま扱える。

```bash
idev exec -- make test        # テストが失敗すれば非0
```

出力は端末以外へ渡しても壊れないため、パイプで加工してよい。

```bash
idev exec -- go test ./... 2>&1 | tail -20
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

標準出力には結果だけが載るので、そのまま取り込んで良い。
`status --json` / `validate` / `provision --list` / `snapshot list` /
`up --dry-run` の出力と、`idev exec` / `idev shell` が中継するコマンドの出力がこれにあたる。
ログ・警告・エラー・確認プロンプト、そして `up` / `provision` / `rebuild` 中の
provisioningステップの出力は、すべて標準エラー出力へ出る。

一覧系の2つは1行1件のタブ区切りで、0件のときは何も出さない。

```bash
idev provision --list | wc -l     # ステップが無ければ 0
idev snapshot list | cut -f1      # 名前が1行ずつ
```

## 7.5 プロジェクトの指示ファイルへ書く例

`CLAUDE.md` や `AGENTS.md` に以下のように書いておくと、
エージェントがホスト側でビルドを試みるのを防げる。

```markdown
## 開発環境

ビルド・テスト・実行はIncusコンテナ内で行う。ホスト側で直接実行しない。

    idev up                    # 環境の構築（初回・設定変更後）
    idev exec -- make test     # テスト
    idev exec -- make build    # ビルド
    idev shell                 # 対話シェル

環境に必要なものを追加する場合は `.incus-dev/dev.yml` の provision を編集し、
`idev provision` で反映する。ホストへツールを追加しない。

provisionステップは再実行される前提で、冪等に書くこと。
```

## 7.6 Agent Skill

Claude Code のようにAgent Skillを読み込むツールでは、
リポジトリの [`skills/incus-dev-ja/`](../../../skills/incus-dev-ja/) をそのまま使える。

```bash
cp -r skills/incus-dev-ja ~/.claude/skills/          # 全プロジェクトで使う
cp -r skills/incus-dev-ja <project>/.claude/skills/  # そのプロジェクトだけ
```

原則・コマンドの対応表・`dev.yml` の書き方・エラーからの切り分けが入っている。

---

## 7.7 注意

- `idev up` は既存のinstanceを破壊しない。環境を作り直したい場合のみ
  `idev rebuild --force` を使う
- `idev destroy` はコンテナだけを削除し、ホスト側のソースツリーには触れない
- エージェントに `incus` コマンドを直接使わせる必要はない。
  必要になった場合、それは `dev.yml` で表現できていないということなので、
  まず `dev.yml` 側での表現を検討する
