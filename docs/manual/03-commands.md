# 3. コマンドリファレンス

## 3.1 共通事項

### プロジェクトの探索

`idev` はカレントディレクトリから親方向へ `.incus-dev/dev.yml` を探す。
サブディレクトリからでも実行できる。

```bash
cd src/foo/bar
idev status        # プロジェクトrootが見つかる
```

Gitリポジトリである必要はない。

### 共通フラグ

| フラグ | 既定 | 説明 |
| --- | --- | --- |
| `-v`, `--verbose` | | 実行した外部コマンドなど詳細を出力する |
| `-C`, `--directory <dir>` | カレントディレクトリ | 探索を開始するディレクトリ |
| `--incus-remote <name>` | `local` | Incus remote |
| `--incus-project <name>` | `default` | Incus project |

### 終了コード

| コード | 意味 |
| --- | --- |
| `0` | 成功 |
| 非0 | 失敗 |

例外として `idev shell -- <command>` は、**コンテナ内コマンドの終了コードをそのまま返す**。

```bash
idev shell -- sh -c 'exit 42'; echo $?   # 42
```

### instance名

`dev-<プロジェクト名>` となる。英数字とハイフン以外は正規化される。

```text
project.name: my.project_1   →   instance: dev-my-project-1
```

- 63文字を超える場合は切り詰める
- 正規化した結果が空になる名前（記号のみなど）は、
  元の名前のハッシュで区別する

---

## 3.2 `idev validate`

`dev.yml` を検証する。**Incusへ一切変更を加えない**ため、CIやIncusの無い環境でも実行できる。

```bash
idev validate
```

検査する内容：

- YAMLの構文、スキーマ、必須項目、未知のフィールド
- `runtime.version` の互換性
- provisionステップの構造（`run` と `ansible` の排他など）
- 参照先ファイルの存在（playbook、vars、inventory、workspace source）
- `profiles: []` の場合に root disk device が宣言されているか
- devkit予約キー（`user.incus-devkit.*`）を使っていないか

Profileがホストに実在するかどうかは確認しない（Incusへ問い合わせないため）。

---

## 3.3 `idev up`

instanceを用意し、bootstrapとprovisionを実行する。

```bash
idev up
```

処理の流れ：

1. `dev.yml` の読み込みと検証
2. workspaceのidmap方式の決定（ホストの設定を確認）
3. 参照Profileの存在確認（無ければ失敗）
4. instanceが無ければ作成、あれば **作り直さずに設定を再適用**
5. workspaceとdeviceの適用
6. 起動と、コマンドを実行できるようになるまでの待機
7. bootstrap → provision の実行

**既にあるinstanceを破壊することはない。** `dev.yml` を変更したあとに
再実行すれば、リソースやdeviceの変更が反映される。

idev が作ったものでないinstanceが同名で存在する場合は、
何もせずに失敗する（誤って壊さないため）。

---

## 3.4 `idev provision`

instanceを作り直さず、bootstrapとprovisionのみ再実行する。

```bash
idev provision
```

主な用途：

- provisionステップを書き換えたとき
- playbookやスクリプトを変更したとき

instanceが存在しない場合は明示的に失敗する。暗黙的に `idev up` へ切り替えない。

`instance.config` や `devices` の変更は反映しない（それは `idev up` の役割）。

---

## 3.5 `idev shell`

コンテナ内でシェル、または指定したコマンドを実行する。

```bash
idev shell                      # 対話シェル（作業ディレクトリは /workspace）
idev shell -- make test         # コマンドを実行して終了
idev shell -- sh -c 'cd /workspace && go build ./...'
```

- 標準入出力が端末に接続されている場合のみ擬似端末を割り当てる。
  パイプやリダイレクト経由でも出力が壊れない

```bash
idev shell -- cat /etc/os-release > os.txt
idev shell -- go test ./... | tee test.log
```

- コンテナが停止していれば起動してから実行する
- 初期実装ではrootで実行する

---

## 3.6 `idev status`

対象instanceの状態を表示する。

```bash
idev status
```

instanceが未作成の場合は `Status: NOT CREATED` となるが、
コマンド自体は成功する（`0` を返す）。

機械可読な出力：

```bash
idev status --json
```

```json
{
  "project": "my-project",
  "instance": "dev-my-project",
  "status": "Running",
  "image": "images:ubuntu/24.04",
  "workspace": "/workspace",
  "workspace_source": "/home/you/src/my-project",
  "exists": true,
  "managed": true,
  "profiles": ["default"],
  "config": { "limits.cpu": "4" },
  "provision_steps": 1
}
```

```bash
# 実行中かどうかで分岐する例
[ "$(idev status --json | jq -r .status)" = "Running" ] || idev up
```

---

## 3.7 `idev destroy`

instanceを削除する。

```bash
idev destroy
idev destroy --force        # 確認を省略（CI・スクリプト用）
```

- **ホスト側のソースツリーは削除しない。** workspaceはbind mountであり、
  削除されるのはコンテナだけである
- idev が作ったものでないinstanceは削除しない

---

## 3.8 `idev rebuild`

instanceを破棄して作り直す。

```bash
idev rebuild
idev rebuild --force
```

コンテナ内の状態はすべて失われる。`dev.yml` から設定項目を削除した場合、
その削除を確実に反映させたいときにも使う（`idev up` は削除を追従しない）。

---

## 3.9 シェル補完

```bash
idev completion bash > /etc/bash_completion.d/idev
idev completion zsh  > "${fpath[1]}/_idev"
idev completion fish > ~/.config/fish/completions/idev.fish
```
