# 2. 最初のプロジェクトを作る

*[English version](../02-getting-started.md)*

既存のプロジェクトへ開発環境定義を追加する手順を、順に追う。

## 2.1 定義ファイルを置く

プロジェクトのルートで以下を作る。

```bash
mkdir -p .incus-dev
```

```yaml
# .incus-dev/dev.yml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
```

必須なのは `schema` `project.name` `instance.image` の3つだけである。

```bash
idev validate
```

```text
configuration is valid
Project:    my-project
Instance:   dev-my-project-cb958c73
Provision:  0 step(s)
```

`validate` はIncusへ触れないため、CIでも実行できる。

## 2.2 起動する

```bash
idev up
```

```text
[idev] Project: my-project
[idev] Creating instance dev-my-project-cb958c73
[idev] Mounting workspace /home/you/src/my-project -> /workspace
[idev] Starting instance dev-my-project-cb958c73
[idev] Development environment is ready
```

この時点で、プロジェクトのworking treeがコンテナの `/workspace` に見えている。

```bash
idev shell
```

```text
# cd /workspace && ls
README.md  src  .incus-dev
```

ホスト側でファイルを編集すると、コンテナ内へ即座に反映される。コピーではない。

## 2.3 必要なものを入れる

環境構築の手順は `provision` に書く。ステップは上から順に実行される。

```yaml
# .incus-dev/dev.yml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
  config:
    limits.cpu: "4"
    limits.memory: 8GiB

provision:
  - name: base packages
    run: |
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends git make jq
```

```bash
idev up
```

既にinstanceがあるため作り直されない。設定変更（CPU・メモリ）が反映され、
provisionが再実行される。

コンテナを作り直さずにprovisionだけ流したい場合は次を使う。

```bash
idev provision
```

## 2.4 手順は再実行できるように書く

`idev provision` は何度でも実行される前提のため、
**各ステップが再実行できることはプロジェクト側の責任** である。

```yaml
provision:
  # 良い例: 既に入っていれば何もしない
  - run: command -v jq >/dev/null 2>&1 || apt-get install -y jq

  # 良い例: apt install は元々再実行できる
  - run: apt-get install -y --no-install-recommends make

  # 悪い例: 実行のたびに追記されてしまう
  - run: echo 'export PATH=$PATH:/opt/bin' >> /root/.bashrc
```

確認方法は単純で、2回続けて実行して成功すればよい。

```bash
idev provision && idev provision
```

## 2.5 状態を確認する

```bash
idev status
```

```text
Project:    my-project
Instance:   dev-my-project-cb958c73
Status:     Running
Image:      images:ubuntu/24.04
Workspace:  /home/you/src/my-project -> /workspace
Profiles:   default
Managed:    yes
limits.cpu: 4
Provision:  1 step(s)
```

## 2.6 コミットする

```bash
git add .incus-dev
git commit -m "開発環境定義を追加"
```

これで、cloneした人が `idev up` だけで同じ環境を再現できる。

README には以下を書いておくとよい。

```markdown
## 開発環境

    idev up
    idev shell
```

## 2.7 片付ける

```bash
idev destroy          # 確認あり
idev destroy --force  # 確認なし
```

instanceだけが削除され、**ホスト側のソースツリーには一切触れない**。

作り直したい場合は次を使う。コンテナ内の状態は破棄される。

```bash
idev rebuild --force
```

## 次に読む

- 設定項目の詳細: [04-dev-yml.md](04-dev-yml.md)
- Ansibleを使う場合: [05-provisioning.md](05-provisioning.md)
- 言語・用途別の例: [06-recipes.md](06-recipes.md)
