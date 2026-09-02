# 10. プロジェクト側の構成例

本章はプロジェクトが `.incus-dev/` に置く内容の例を示す。

idevはこれらの内容を一切持たない。すべてプロジェクトの所有物である。

---

## 10.1 最小構成

```text
my-project/
└── .incus-dev/
    └── dev.yml
```

```yaml
# .incus-dev/dev.yml
schema: 1

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
```

`idev up` により、`/workspace` にソースがマウントされた素のコンテナが起動する。

provisioningは行われず、bootstrapも実行されない。

---

## 10.2 シェルスクリプトで構成する（Go開発環境）

```text
my-project/
└── .incus-dev/
    ├── dev.yml
    └── scripts/
        └── setup.sh
```

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
  - name: setup toolchain
    run: sh /workspace/.incus-dev/scripts/setup.sh
    env:
      GO_VERSION: "1.25.0"
```

```sh
#!/bin/sh
# .incus-dev/scripts/setup.sh
set -eu

: "${GO_VERSION:?}"

apt-get update
apt-get install -y --no-install-recommends ca-certificates curl git make

# 再実行時に既定バージョンが入っていれば何もしない
if [ ! -x /usr/local/go/bin/go ] ||
   ! /usr/local/go/bin/go version | grep -q "go${GO_VERSION}"; then
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tgz
    rm -f /tmp/go.tgz
fi

cat > /etc/profile.d/go.sh <<'PROFILE'
export PATH=$PATH:/usr/local/go/bin
PROFILE
```

Ansibleを使わない場合、ホスト側に必要なのはIncusと`idev` のみとなる。

冪等性はスクリプト側で担保する（REQ-005）。

---

## 10.3 Ansibleで構成する

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
            ├── base/
            └── python/
```

```yaml
# .incus-dev/dev.yml
schema: 1

runtime:
  version: "1.0"

project:
  name: my-project

instance:
  image: images:ubuntu/24.04
  config:
    limits.cpu: "8"
    limits.memory: 16GiB

provision:
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
      vars: .incus-dev/ansible/vars.yml
```

```ini
# .incus-dev/ansible/ansible.cfg
[defaults]
roles_path = .incus-dev/ansible/roles
result_format = yaml
```

```yaml
# .incus-dev/ansible/site.yml
---
- name: Provision development environment
  hosts: dev
  gather_facts: true

  roles:
    - role: base
    - role: python

  tasks:
    - name: Install project dependencies
      ansible.builtin.apt:
        name:
          - protobuf-compiler
          - libssl-dev
          - postgresql-client
        state: present
```

```yaml
# .incus-dev/ansible/vars.yml
---
python_version: "3.13"
```

`hosts: dev` はidevが生成するinventoryの規約
（[06-provisioning.md](06-provisioning.md) 6.5.2）に対応する。

Roleは完全にプロジェクトの所有物であり、idevの更新の影響を受けない。

共有したい場合は、Ansible Collectionとして別リポジトリで配布し、
`requirements.yml` で取り込む。

---

## 10.3.1 一般ユーザーで作業する

provisioningでアカウントを作り、`shell.user` でそこへ入る。
`examples/dev-user/` が実物である。

```yaml
workspace:
  idmap: shift

provision:
  - name: developer account
    run: sh /workspace/.incus-dev/scripts/create-user.sh
    env:
      DEV_USER: developer
      DEV_UID: "1000"
      DEV_GID: "1000"

shell:
  user: developer
  command: /bin/bash
  cwd: /workspace
```

`idmap` の選択がこの構成の成立条件である。実daemonで測ると次のようになる。

| `workspace.idmap` | コンテナ内 root が書いたファイル | コンテナ内の一般アカウント |
| --- | --- | --- |
| `raw` | ホストの実行ユーザー | workspaceへ書き込めない |
| `shift` | ホストの `root` | uidが一致すればホストの実行ユーザー |

`raw` が付け替えるのはコンテナの **root** の1点だけであり、
一般アカウントのidは何もしない場合と変わらない。
workspaceはコンテナ内からrootの所有に見えるので、そもそも書き込めない。
`shift` はそのマウントで名前空間の効果を打ち消すため、
アカウントに実行ユーザーと同じuid/gidを与えれば書いたファイルの所有者が一致する。
`DEV_UID` / `DEV_GID` がそれである。詳しくは
[03-configuration.md](03-configuration.md) 3.7.3。

イメージが既に同じuidのアカウントを持つ場合（Ubuntuイメージの `ubuntu` は
uid 1000）、`useradd --non-unique` で両方が存在する。
`id` が返す名前はgetentが先に返した方になるが、
workspaceにとって意味を持つのはuidであり、それは要求したものになる。

一時的にrootで入るには `idev shell --user root` を使う
（[04-cli.md](04-cli.md) 4.3）。`dev.yml` を書き換えると
その変更は他の全員へ及ぶ。

---

## 10.4 併用と順序制御

`run` と `ansible` は自由に混在でき、記述順に実行される。

```yaml
provision:
  # Ansibleを動かす前に必要な準備
  - name: apt update
    run: apt-get update

  # 本体
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml

  # 最後にプロジェクト固有のセットアップ
  - name: bootstrap app
    run: |
      cd /workspace
      make deps
```

---

## 10.5 Docker in Container

```yaml
instance:
  image: images:ubuntu/24.04
  config:
    security.nesting: "true"

provision:
  - name: docker
    run: |
      command -v docker >/dev/null 2>&1 && exit 0
      apt-get update
      apt-get install -y ca-certificates curl
      curl -fsSL https://get.docker.com | sh
```

必要な設定を `instance.config` に直接書くため、
idev側にProfileを用意する必要がない。

---

## 10.6 GPU

```yaml
instance:
  image: images:ubuntu/24.04
  devices:
    gpu0:
      type: gpu
      gputype: physical
```

ホスト固有の事情（GPUの有無、ベンダー）はプロジェクト、
あるいはホスト側で用意したProfileの参照で表現する。

```yaml
instance:
  profiles:
    - default
    - my-host-gpu       # ホスト管理者が用意したProfile
```

この場合、当該Profileが存在しないホストでは `idev up` が
明示的に失敗する（[05-incus.md](05-incus.md) 5.3）。

---

## 10.7 Profileに依存しない構成

ホスト側のProfileに一切依存させたい場合、`profiles: []` として
必要なdeviceをすべて宣言する。

```yaml
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

root diskとネットワークもProfile由来であるため、明示が必要になる。
storage poolやnetwork名はホストに依存するため、多くの場合は
`default` profileを参照する方が可搬性が高い。

---

## 10.8 bootstrapの上書き

Debian系以外のイメージを使う場合、既定bootstrapは通用しない。

```yaml
instance:
  image: images:fedora/41

bootstrap:
  - name: ensure python
    run: |
      command -v python3 >/dev/null 2>&1 || dnf install -y python3

provision:
  - ansible:
      playbook: .incus-dev/ansible/site.yml
```

Ansibleを使わない場合は明示的に無効化できる。

```yaml
bootstrap: []
```
