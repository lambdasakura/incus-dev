# 6. 用途別の構成例

そのまま `.incus-dev/dev.yml` へ貼って使える例。

*[English version](../06-recipes.md)*

動作するサンプル一式は [examples/](../../../examples/README.ja.md) にもある。

## 6.1 最小

```yaml
schema: 1
project:
  name: my-project
instance:
  image: images:ubuntu/24.04
```

workspaceがマウントされた素のコンテナが起動する。

---

## 6.2 Go

```yaml
schema: 1

project:
  name: my-go-project

instance:
  image: images:ubuntu/24.04
  config:
    limits.cpu: "8"
    limits.memory: 8GiB

provision:
  - name: toolchain
    run: sh /workspace/.incus-dev/scripts/setup-go.sh
    env:
      GO_VERSION: "1.25.0"
```

```sh
#!/bin/sh
# .incus-dev/scripts/setup-go.sh
set -eu
: "${GO_VERSION:?}"

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl git make

if [ ! -x /usr/local/go/bin/go ] ||
   ! /usr/local/go/bin/go version | grep -q "go${GO_VERSION}"; then
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tgz
    rm -f /tmp/go.tgz
fi

cat > /etc/profile.d/go.sh <<'EOS'
export PATH=$PATH:/usr/local/go/bin
export GOPATH=/workspace/.go
EOS
```

---

## 6.3 Python

```yaml
schema: 1

project:
  name: my-python-project

instance:
  image: images:ubuntu/24.04

provision:
  - name: python
    run: |
      set -eu
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends \
        python3 python3-venv python3-pip build-essential

  - name: dependencies
    run: |
      set -eu
      cd /workspace
      [ -d .venv ] || python3 -m venv .venv
      ./.venv/bin/pip install --upgrade pip
      [ -f requirements.txt ] && ./.venv/bin/pip install -r requirements.txt || true
```

仮想環境を `/workspace/.venv` に置くとホスト側からも見える。
`.gitignore` への追加を忘れないこと。

---

## 6.4 Node.js

```yaml
schema: 1

project:
  name: my-node-project

instance:
  image: images:ubuntu/24.04

provision:
  - name: nodejs
    run: |
      set -eu
      command -v node >/dev/null 2>&1 && exit 0
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends ca-certificates curl
      curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
      apt-get install -y nodejs

  - name: dependencies
    run: |
      cd /workspace
      [ -f package-lock.json ] && npm ci || true
```

`node_modules` はネイティブモジュールがOS依存になるため、
ホスト側とコンテナ内で共有すると壊れることがある。
その場合は workspace の外へ置く構成を検討する。

---

## 6.5 コンテナ内でDockerを使う

```yaml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
  config:
    security.nesting: "true"     # これが必要

provision:
  - name: docker
    run: |
      set -eu
      command -v docker >/dev/null 2>&1 && exit 0
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends ca-certificates curl
      curl -fsSL https://get.docker.com | sh
```

必要な設定を `instance.config` に直接書けるため、
専用のProfileをホストに用意する必要はない。

---

## 6.6 データベースを併走させる

コンテナ内でサービスを動かし、ホストからポートで触る。

```yaml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
  devices:
    postgres:
      type: proxy
      listen: tcp:127.0.0.1:15432      # ホスト側
      connect: tcp:127.0.0.1:5432      # コンテナ側

provision:
  - name: postgres
    run: |
      set -eu
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends postgresql
      systemctl enable --now postgresql || service postgresql start
```

ホストから `psql -h 127.0.0.1 -p 15432` で接続できる。

---

## 6.7 GPU

```yaml
schema: 1

project:
  name: my-ml-project

instance:
  image: images:ubuntu/24.04
  devices:
    gpu0:
      type: gpu
      gputype: physical
```

ホスト固有の事情（GPUの有無やベンダー）に依存するため、
ホスト管理者が用意したProfileを参照する方が可搬性が高い場合もある。

```yaml
instance:
  profiles:
    - default
    - host-gpu
```

参照したProfileが無いホストでは `idev up` が明示的に失敗する。

---

## 6.8 ホストのProfileに一切依存しない

```yaml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
  profiles: []
  devices:
    root:
      type: disk
      pool: default
      path: /
    eth0:
      type: nic
      network: incusbr0
```

storage pool名とnetwork名はホスト依存になる点に注意する。

---

## 6.9 複数のリポジトリをマウントする

`instance.devices` に追加すれば、workspace以外のディレクトリも共有できる。
workspaceと同じuid/gid対応付けが自動的に適用される。

```yaml
instance:
  devices:
    other-repo:
      type: disk
      source: ../other-repo      # project root基準
      path: /other-repo
```

---

## 6.10 追加のデータをマウントする

```yaml
instance:
  devices:
    dataset:
      type: disk
      source: /srv/dataset        # ホスト側の絶対パス
      path: /data
      readonly: "true"

    assets:
      type: disk
      source: ./assets            # project root基準
      path: /assets
```

---

## 6.11 CIから使う

```bash
idev validate                     # Incus不要。設定の妥当性だけ確認する
```

Incusが使えるランナーであれば、実際に構築して検証できる。

```bash
idev up
idev exec -- make test
idev destroy --force
```

`idev exec -- <command>` はコマンドの終了コードをそのまま返すため、
CIの成否判定にそのまま使える。

---

## 6.12 Ansibleを使う構成

```text
my-project/
└── .incus-dev/
    ├── dev.yml
    └── ansible/
        ├── ansible.cfg
        ├── site.yml
        ├── vars.yml
        ├── requirements.yml
        └── roles/
            └── base/
```

```yaml
# .incus-dev/dev.yml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04

provision:
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
      vars: .incus-dev/ansible/vars.yml
```

書き方は [05-provisioning.md](05-provisioning.md) 5.3 を参照。
