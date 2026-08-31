# 5. Incus層

devkitがIncusに対して行うのは、instanceのライフサイクル管理と、
`dev.yml` に宣言された設定の適用のみである。

devkitはIncus Profileを同梱・作成しない（REQ-007）。

---

## 5.1 Instance命名規則

基本形：

```text
dev-<project-name>
```

例：

```text
dev-example-project
```

プレフィックス `dev-` は「開発環境用instance」を意味するものであり、
コマンド名 `idev` とは独立している。`incus list` での可読性を優先する。

ただし同一マシン上で複数checkoutを利用する可能性を考慮する。

将来的には以下のような方式をサポート可能とする。

```text
dev-example-project-main
dev-example-project-a8f213
```

初期実装では単純なproject name方式でよい。

命名ロジックは単独の純粋関数として実装し、単体テスト可能にする。

Incusのinstance名制約（長さ、使用可能文字）に適合するよう正規化する。

---

## 5.2 devkitが管理するinstanceの識別

devkitは自身が作成したinstanceを、instance configで印付けする。

```text
user.incus-devkit.project = example-project
user.incus-devkit.root    = /home/user/src/example-project
user.incus-devkit.schema  = 1
```

目的：

- 既存instanceが本当にこのプロジェクトのものかを確認する
- 名前衝突時に、無関係なinstanceを破壊しないようにする

`idev destroy` および `idev rebuild` は、対象instanceが
devkit管理下でない場合、明示的に失敗する。

`user.incus-devkit.*` 名前空間はdevkitの予約とする。

---

## 5.3 Profile

`instance.profiles` は **ホスト側に既に存在するProfileの名前参照のみ** を表す。

- devkitはProfileを同梱しない
- devkitはProfileを作成・更新・削除しない
- 指定されたProfileが存在しない場合、`idev up` は明示的に失敗する
  - エラーには不足しているProfile名を含める

省略時は `["default"]` を使用する。

明示的な空リストはProfileを一切適用しないことを意味する。
実装は対象Incus versionの該当フラグ（例: `--no-profiles`）を用いる。

この場合、Profileが提供していたroot diskとネットワークも失われるため、
`instance.devices` で明示する必要がある（[03-configuration.md](03-configuration.md) 3.6.3）。

環境依存を避けたいプロジェクトは、Profileを使わず
`instance.config` と `instance.devices` にすべて記述できる。

---

## 5.4 configとdeviceの適用

### 5.4.1 適用対象

| 由来 | 内容 |
| --- | --- |
| `instance.config` | 宣言されたkey-valueをそのまま設定 |
| `instance.devices` | 宣言されたdeviceをそのまま設定 |
| `workspace` | `workspace` という名前のdisk deviceとして設定（`shift` 方式では `shift=true` を付与） |
| devkit管理情報 | `user.incus-devkit.*` |

devkitはconfigキーの意味を解釈しない。
CPUやメモリも `limits.cpu` / `limits.memory` として素通しする。

### 5.4.2 作成時の適用

`instance.config` と `instance.devices` はinstance作成時に適用する。

`incus create` の `-c` / `-d` フラグはProfile上に既にあるdeviceの
上書きしか行えず、新規deviceを作成できない。このため作成時の指定は
標準入力へYAMLで渡す。

```bash
incus create <image> <name> -p <profile> <<'YAML'
config:
  limits.cpu: "8"
devices:
  workspace:
    type: disk
    source: /home/u/src/example
    path: /workspace
YAML
```

### 5.4.3 適用タイミング

`idev up` は、instanceが既に存在する場合も宣言内容を再適用する。

これにより `dev.yml` の変更（リソース増減、device追加）が反映される。

### 5.4.4 削除の扱い

`dev.yml` から削除された設定は、**devkitが適用したものに限り** 取り消す。

devkitは適用したconfigキーとdevice名を、instance自身へ記録する。

```text
user.incus-devkit.managed  = limits.cpu,limits.memory,raw.idmap
user.incus-devkit.devices  = extdata,workspace
```

`idev up` のたびに記録と宣言を突き合わせ、記録にあって宣言に無いものを
取り消す。記録に無いもの（利用者が `incus config set` で手動追加した設定や
device）には一切触れない。

記録を持たない古いinstanceに対しては、devkit自身が設定したidmapキー
（`raw.idmap`）のみを対象とする。

完全にクリーンな状態が必要な場合は `idev rebuild` を使用する。

### 5.4.5 再起動を要する設定

一部の設定は変更にinstance再起動を要する。

devkitは再起動が必要な変更を検出した場合、既定では警告のみを表示する。

```bash
idev up --restart
```

が指定された場合に限り、instanceを再起動して反映する。

利用者の作業中プロセスを予期せず停止させてはならないため、
再起動は明示的な指示があった場合のみ行う。

対象は devkit が実際に変更・取り消したキーに限る。
触れていないキーを含めると、何もしていないのに警告が出続けてしまう。

---

## 5.5 Incus Project

初期実装ではIncusの `default` projectを利用してよい。

ただし内部APIではIncus projectを固定値として埋め込まない。

将来的に、

```yaml
incus:
  project: development
```

のような設定を追加できる設計とする。

---

## 5.6 Incus Remote

同様にlocal Incusをデフォルトとする。

将来的には、

```yaml
incus:
  remote: dev-server
```

を扱える構造とする。

初期実装でremote対応を実装する必要はないが、
Incus操作層ではremoteを引数として受けられるようにすることが望ましい。

remoteを利用する場合、workspaceのbind mountはホスト側パスを前提とするため
成立しない点に注意する。remote対応時に扱いを定義する。

---

## 5.7 Incus操作層

Incus関連処理を `internal/incus` へ集約する。

CLI処理からIncusコマンド文字列を直接組み立てることを避ける。

インターフェースとして最低限以下を定義する。

```go
package incus

type Client interface {
    Instance(ctx context.Context, name string) (*Instance, error)

    CreateInstance(ctx context.Context, spec InstanceSpec) error
    StartInstance(ctx context.Context, name string) error
    DeleteInstance(ctx context.Context, name string) error

    ApplyConfig(ctx context.Context, name string, cfg map[string]string) error
    UnsetConfig(ctx context.Context, name string, keys []string) error
    ApplyDevices(ctx context.Context, name string, dev map[string]Device) error

    ProfileExists(ctx context.Context, name string) (bool, error)

    Exec(ctx context.Context, name string, argv []string, opt ExecOptions) (int, error)
    WaitReady(ctx context.Context, name string, opt WaitOptions) error
}

type ExecOptions struct {
    Env         map[string]string
    Cwd         string
    User        string
    TTY         bool      // idev shell のみ true
    Stdin       io.Reader
    Stdout      io.Writer
    Stderr      io.Writer
}
```

interfaceとして定義することで、以下を可能にする。

- 単体テストでのfake実装（Incus daemon不要）
- 将来的な実装差し替え

interfaceには **実際に使う操作だけ** を並べる。
instanceの存在確認は `Instance` と `errors.Is(ErrInstanceNotFound)` で行う。
差し替え時の負担がそのまま増えるため、使わない操作を含めない。

### 5.7.1 実装方針

MVPでは `incus` CLIをラップした実装 (`internal/incus/cli.go`) を用いる。

理由：

- 対象Incus versionのCLI互換性を確認しやすい
- `idev shell` のような端末を伴う操作を扱いやすい

将来的に、公式Go client library

```text
github.com/lxc/incus/client
```

を用いた実装へ差し替え可能とする。これにより以下の利点が得られる。

- CLI出力のパースが不要になる
- 型付きのAPIレスポンスを扱える
- CLIのバージョン差異の影響を受けにくい

ただし `idev shell` は端末制御の都合上、CLI呼び出しを維持してよい。

### 5.7.2 出力パース

CLI実装で状態を取得する場合は、人間向け出力ではなく
機械可読な形式を用いる。

```bash
incus list <name> --format json
incus config show <name>
```

得られたJSON/YAMLは型付き構造体へデコードする。
