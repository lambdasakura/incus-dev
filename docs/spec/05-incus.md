# 5. Incus層

## 5.1 Instance命名規則

基本形：

```text
dev-<project-name>
```

例：

```text
dev-example-project
```

ただし同一マシン上で複数checkoutを利用する可能性を考慮する。

将来的には以下のような方式をサポート可能とする。

```text
dev-example-project-main
dev-example-project-feature-foo
```

または、

```text
dev-example-project-a8f213
```

初期実装では単純なproject name方式でよい。

命名ロジックは単独の純粋関数として実装し、単体テスト可能にする。

---

## 5.2 Incus Project

初期実装ではIncusの `default` projectを利用してよい。

ただし内部APIではIncus projectを固定値として埋め込まない。

将来的に、

```yaml
incus:
  project: development
```

のような設定を追加できる設計とする。

---

## 5.3 Incus Remote

同様にlocal Incusをデフォルトとする。

将来的には、

```yaml
incus:
  remote: local
```

または、

```yaml
incus:
  remote: dev-server
```

を扱える構造とする。

初期実装でremote対応を実装する必要はないが、
Incus操作層ではremoteを引数として受けられるようにすることが望ましい。

---

## 5.4 Profile管理

共通ツールが利用するProfile例：

```text
dev-base
nested
gpu-amd
gpu-nvidia
```

Profileには原則として以下のみを持たせる。

- network
- disk
- GPU
- device
- security
- nesting

言語ランタイムやOS packageはProfileへ含めない。

---

## 5.5 Profileの存在確認

`dev up` 実行時、指定Profileが存在しない場合は明示的に失敗する。

初期実装ではProfileを自動作成しなくてもよい。

将来的には、

```bash
dev setup
```

により共通Profileをホストへinstallする機能を追加してよい。

---

## 5.6 Incus操作層

Incus関連処理を `internal/incus` へ集約する。

CLI処理からIncusコマンド文字列を直接組み立てることを避ける。

インターフェースとして最低限以下を定義する。

```go
package incus

type Client interface {
    InstanceExists(ctx context.Context, name string) (bool, error)
    InstanceStatus(ctx context.Context, name string) (Status, error)

    CreateInstance(ctx context.Context, spec InstanceSpec) error
    StartInstance(ctx context.Context, name string) error
    StopInstance(ctx context.Context, name string) error
    DeleteInstance(ctx context.Context, name string) error

    SetResources(ctx context.Context, name string, r Resources) error

    AddWorkspaceMount(ctx context.Context, name string, m Mount) error

    Exec(ctx context.Context, name string, argv []string, opt ExecOptions) error
}
```

interfaceとして定義することで、以下を可能にする。

- 単体テストでのfake実装（Incus daemon不要）
- 将来的な実装差し替え

### 5.6.1 実装方針

MVPでは `incus` CLIをラップした実装 (`internal/incus/cli.go`) を用いる。

理由：

- 対象Incus versionのCLI互換性を確認しやすい
- `dev shell` のような端末を伴う操作を扱いやすい

将来的に、公式Go client library

```text
github.com/lxc/incus/client
```

を用いた実装へ差し替え可能とする。これにより以下の利点が得られる。

- CLI出力のパースが不要になる
- 型付きのAPIレスポンスを扱える
- CLIのバージョン差異の影響を受けにくい

ただし `dev shell` は端末制御の都合上、CLI呼び出しを維持してよい。

### 5.6.2 出力パース

CLI実装で状態を取得する場合は、人間向け出力ではなく
機械可読な形式を用いる。

```bash
incus list <name> --format json
```

得られたJSONは型付き構造体へデコードする。
