# 9. MVPとロードマップ

## 9.1 MVP

最初の実装では以下に機能を限定する。

### CLI

```text
dev up
dev provision
dev shell
dev status
dev destroy
dev rebuild
dev validate
```

### Incus

```text
local remote
default project
container only
Ubuntu 24.04
CPU
Memory
Profile
Workspace bind mount
```

### Ansible

```text
community.general.incus

bootstrap
common
packages
python
docker
project.yml
```

### Configuration

```text
schema
runtime
project
instance
workspace
packages
features
provision
```

---

## 9.2 MVP以降の候補

以下はMVP完成後に検討する。

```text
Node.js
Go
Rust

AMD GPU
NVIDIA GPU

multiple workspaces
persistent volumes

environment variables
secrets

Incus remote
Incus project

multiple checkout support
branch-specific instance

snapshot
restore

clone environment

dev exec

JSON output

shell completion

custom images

cloud-init support

Incus Go client library への移行
```

---

## 9.3 開発優先順位

推奨実装順：

### Phase 0

Goプロジェクトの初期化。

```text
go.mod
cmd/dev
internal/ の骨格
Makefile
CI (gofmt / go vet / go test)
```

以下が成功する状態にする。

```bash
go build ./cmd/dev
dev --version
```

---

### Phase 1

```text
config parser
project discovery
CLI skeleton
```

以下が成功する状態にする。

```bash
dev validate
```

---

### Phase 2

Incus基本操作。

```text
create
start
status
destroy
workspace mount
CPU
Memory
Profile
```

以下を成立させる。

```bash
dev up
dev status
dev shell
dev destroy
```

まだAnsibleは不要。

---

### Phase 3

Ansible統合。

```text
community.general.incus
temporary inventory
bootstrap
common role
packages
```

同時に、Playbook/Roleの `go:embed` 同梱と展開を実装する。

---

### Phase 4

Feature。

```text
python
docker
```

---

### Phase 5

project-specific Ansible。

```text
.incus-dev/ansible/project.yml
.incus-dev/ansible/vars.yml
```

---

### Phase 6

テストとUX改善。

```text
unit tests
integration tests
verbose
dry-run
JSON output
error messages
shell completion
```

---

## 9.4 完成条件

MVPは以下がすべて成立した時点で完成とする。

新規Ubuntuホスト上で必要な共通ツールをセットアップ済みとする。

（dev CLIについては、単一バイナリをPATH上へ配置するだけで動作すること。）

任意のテストプロジェクトで、

```bash
git clone <repository>
cd <repository>

dev validate

dev up

dev status

dev shell
```

が成功すること。

コンテナ内部で、

```bash
cd /workspace
```

するとホスト側Git repositoryが見えること。

`dev.yml` に、

```yaml
packages:
  - jq

features:
  python:
    version: "3.13"
```

を指定した場合、コンテナ内部で該当環境を利用できること。

さらに、

```bash
dev provision
dev provision
```

を連続実行して正常終了すること。

最後に、

```bash
dev destroy --force
```

を実行するとIncus instanceだけが削除され、

```text
Git repository
source files
.incus-dev/dev.yml
```

がホスト上に残ること。
