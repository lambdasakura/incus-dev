# 5. Incus層

idevがIncusに対して行うのは、instanceのライフサイクル管理と、
`dev.yml` に宣言された設定の適用のみである。

idevはIncus Profileを同梱・作成しない（REQ-007）。

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

同一マシン上で複数のチェックアウトを扱う場合、`project.scope` で
区別の仕方を選べる（[03-configuration.md](03-configuration.md) 3.5）。

```text
scope: name    dev-example-project              （既定）
scope: path    dev-example-project-cb958c73     チェックアウト先ごと
scope: branch  dev-example-project-feature-x    ブランチごと
```

既定は従来どおりプロジェクト名のみとし、明示的に指定した場合のみ
名前が変わるようにする。既存の環境が意図せず別物になることを避けるため。

命名ロジックは単独の純粋関数として実装し、単体テスト可能にする。

Incusのinstance名制約（長さ、使用可能文字）に適合するよう正規化する。

ただし `project.name` 自体はスキーマが制限しており（03-configuration.md 3.5）、
既にinstance名として使える形に限られる。正規化が実際に効くのは
`scope: path` / `branch` の suffix 側である。

---

## 5.2 idevが管理するinstanceの識別

idevは自身が作成したinstanceを、instance configで印付けする。

```text
user.incus-dev.project = example-project
user.incus-dev.root    = /home/user/src/example-project
user.incus-dev.schema  = 1
user.incus-dev.image   = images:ubuntu/24.04
user.incus-dev.volumes = default/dev-example-project-cache
user.incus-dev.restart-pending = 2026-09-01T00:00:00Z|security.nesting=false
```

目的：

- 既存instanceが本当にこのプロジェクトのものかを確認する
- 名前衝突時に、無関係なinstanceを破壊しないようにする

`user.incus-dev.root` が現在のproject rootと食い違う場合は警告する。
`up` だけでなく、instanceを操作するすべてのコマンド（`shell` / `exec` /
`provision` / `destroy` / `rebuild` / `snapshot`）で警告する。
workspaceは最後に `up` を実行したcheckoutを指しており、
`idev exec -- rm -rf build` が別のcheckoutのツリーを消しうるためである。
既定の `project.scope: name` では同じプロジェクトの複数checkoutが
1つのinstanceを共有する設計であり、`idev up` はworkspaceを最後に実行した
checkoutへ向け直す。黙って向け直すと、もう一方のcheckoutで作業している人が
気付かないうちに別のツリーをビルドすることになる。

`idev destroy` および `idev rebuild` は、対象instanceが
idev管理下でない場合、明示的に失敗する。

`user.incus-dev.*` 名前空間はidevの予約とする。

---

## 5.3 Profile

`instance.profiles` は **ホスト側に既に存在するProfileの名前参照のみ** を表す。

- idevはProfileを同梱しない
- idevはProfileを作成・更新・削除しない
- 指定されたProfileが存在しない場合、`idev up` は明示的に失敗する
  - エラーには不足しているProfile名を含める

省略時は `["default"]` を使用する。

明示的な空リストはProfileを一切適用しないことを意味する。
実装は作成リクエストの `profiles` へ空リストを渡す
（省略とは区別する必要があるため、`InstanceSpec.NoProfiles` で明示する）。

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
| idev管理情報 | `user.incus-dev.*` |

idevはconfigキーの意味を解釈しない。
CPUやメモリも `limits.cpu` / `limits.memory` として素通しする。

### 5.4.2 作成時の適用

`instance.config` と `instance.devices` はinstance作成時に適用する。

作成リクエスト（`api.InstancesPost`）へconfig・devices・profilesを載せ、
一度の呼び出しで作る。作成後に設定し直すと、起動前にしか適用できない
設定を扱えなくなるためである。

```go
req := api.InstancesPost{
    Name: spec.Name,
    Type: api.InstanceTypeContainer,
    InstancePut: api.InstancePut{
        Config:   spec.Config,
        Devices:  toAPIDevices(spec.Devices),
        Profiles: spec.Profiles,
    },
}
```

### 5.4.3 適用タイミング

`idev up` は、instanceが既に存在する場合も宣言内容を再適用する。

これにより `dev.yml` の変更（リソース増減、device追加）が反映される。

**profileも作成時にしか設定しない。** Profileはroot diskとネットワークを
決めるものであり、そのうちどれをidevが付けたかの記録が無いため、
利用者が自分で付けたProfileを外してしまう恐れがある。
宣言と食い違う場合は警告する。

ただし **image は作り直さない。** 既存instanceのimageを差し替えることは
できないためである。作成時のimageを `user.incus-dev.image` に記録しておき、
宣言と食い違う場合は警告する（黙って無視すると、利用者は
`instance.image` を変えたのに古い環境のまま作業を続けることになる）。
作り直すには `idev rebuild` を使う。

### 5.4.4 削除の扱い

`dev.yml` から削除された設定は、**idevが適用したものに限り** 取り消す。

idevは適用したconfigキーとdevice名を、instance自身へ記録する。

```text
user.incus-dev.managed  = limits.cpu,limits.memory,raw.idmap
user.incus-dev.devices  = extdata,workspace
```

`idev up` のたびに記録と宣言を突き合わせ、記録にあって宣言に無いものを
取り消す。記録に無いもの（利用者が `incus config set` で手動追加した設定や
device）には一切触れない。

記録を持たない古いinstanceに対しては、idev自身が設定したidmapキー
（`raw.idmap`）のみを対象とする。

deviceの**中のキー**については、宣言をそのまま置き換える。
idevが適用するdeviceはworkspace・volume・`instance.devices` のいずれかであり、
どれも宣言が内容の全体だからである。マージにすると、宣言から消えたキーが
instance側に残り続け、`pool` と ホストパスの `source` を同時に持つdiskのように
Incusが拒否する組み合わせができて、`dev.yml` をどう直しても復旧できなくなる。

完全にクリーンな状態が必要な場合は `idev rebuild` を使用する。

### 5.4.5 再起動を要する設定

一部の設定は変更にinstance再起動を要する。対象は以下。

```text
raw.idmap
security.nesting
security.privileged
```

`limits.*` は含めない。コンテナでは増減とも実行中に反映されるためである。

idevは再起動が必要な変更を検出した場合、既定では警告のみを表示する。

```bash
idev up --restart
```

が指定された場合に限り、instanceを再起動して反映する。

利用者の作業中プロセスを予期せず停止させてはならないため、
再起動は明示的な指示があった場合のみ行う。

**再起動が必要であるという事実は instance へ記録する**
（`user.incus-dev.restart-pending`、`<適用時点の起動時刻>|<キー>=<起動時の値>` の形）。
警告を出すのは変更を書き込んだ回であり、
利用者がその案内に従って `idev up --restart` を実行する頃には、
宣言とinstanceのconfigは既に一致していて比較対象が残っていないためである。
記録が無いと、案内されたコマンドが何もせずに終わる。

**値まで記録するのは、変更を元に戻した場合に収束させるためである。**
キーだけを記録すると、`true` → `false` → `true` と戻したときに
「変更された」と判定し続ける。実行中のコンテナは既に `true` で動いており
再起動しても何も変わらないため、警告が永久に消えず、
案内に従った `idev up --restart` は何も適用せずに
利用者の作業中プロセスだけを停止させる。

起動時の値と宣言が一致した時点で、そのキーの記録は消す。

記録は再起動に成功した時点で消す。停止中のinstanceに対しても消す
（この後の起動で反映されるため）。

利用者が `incus restart` で再起動した場合や、ホスト再起動でinstanceが
自動起動した場合も反映は済んでいる。記録した起動時刻より instance の
起動時刻が新しければ、記録は無効とみなす。これが無いと、既に反映済みの
変更について警告を出し続けることになる。

停止も同じ理由で **正常停止を先に試みる**。ただし応答しないinstanceで
固まらないよう待ち時間には上限（30秒）を設け、超えた場合は強制停止する。
`idev destroy` / `idev rebuild` のように破棄が前提の場合はこの限りではなく、
最初から強制停止してよい。

対象は idev が実際に変更・取り消したキーに限る。
触れていないキーを含めると、何もしていないのに警告が出続けてしまう。

---

## 5.5 Incus Project

操作対象のIncus projectは、CLIの `--incus-project` → `dev.yml` の
`incus.project` → `default` の順で決まる（[04-cli.md](04-cli.md) 4.0、
[03-configuration.md](03-configuration.md) 3.15）。

決定した値は `incus.Target` として操作層へ渡し、接続時に
`UseProject` で固定する。個々の操作でproject名を組み立てない。

idevはIncus projectを作成しない。存在しないprojectを指定した場合、
Incus側のエラーがそのまま利用者へ届く。

---

## 5.6 Incus Remote

**remoteのIncusは対象外である。対応する予定はない。**

workspaceはホスト側のパスのbind mountであり、そのパスはremoteの向こう側には
存在しない。remoteを扱うには共有方式そのものを別に設計する必要があるが、
それはこのツールの目的（手元のマシンにプロジェクト単位の開発環境を作る）から
外れる。

したがって接続先は常に `local` remote に固定する
（`internal/incus/connect.go` の `localRemote`）。remoteを選ぶフラグも
`incus.Target` のフィールドも持たない。`incus remote switch` が設定した
既定remoteにも従わない。手元のIncusを操作しているつもりで、
別のマシンのinstanceを壊さないためである。

imageの取得元を示す remote（`images:ubuntu/24.04` の `images:`）は
これとは別の仕組みであり、従来どおり `incus` コマンドと同じ設定
（`~/.config/incus/config.yml`）から解決する。

`dev.yml` 側での remote 指定は提供しない。

---

## 5.7 Incus操作層

Incus関連処理を `internal/incus` へ集約する。

CLI処理からIncusコマンド文字列を直接組み立てることを避ける。

インターフェースとして以下を定義する（`internal/incus/client.go`）。

```go
package incus

type Client interface {
    Instance(ctx context.Context, name string) (*Instance, error)
    // ListInstances はproject内のcontainerをconfigつきで列挙する。
    // 命名規則が変わった後も、idevが作ったinstanceを見つけるために使う。
    ListInstances(ctx context.Context) ([]Instance, error)

    CreateInstance(ctx context.Context, spec InstanceSpec) error
    StartInstance(ctx context.Context, name string) error
    StopInstance(ctx context.Context, name string) error
    DeleteInstance(ctx context.Context, name string) error

    UpdateInstance(ctx context.Context, name string, change InstanceChange, etag string) error

    // Incusには単一のprofileの存在を問う呼び出しが無く、どの答えも
    // 名前一覧を絞り込んだものになる。名前ごとに問うと同じ一覧を
    // 何度も取得することになるため、一覧を返す。
    ProfileNames(ctx context.Context) ([]string, error)

    // CheckImage は image 参照が解決できるかを、何も作らずに確かめる。
    CheckImage(ctx context.Context, ref string) error

    VolumeExists(ctx context.Context, pool, name string) (bool, error)
    CreateVolume(ctx context.Context, pool, name string, config map[string]string) error
    DeleteVolume(ctx context.Context, pool, name string) error

    CreateSnapshot(ctx context.Context, instance, snapshot string) error
    Snapshots(ctx context.Context, instance string) ([]Snapshot, error)
    RestoreSnapshot(ctx context.Context, instance, snapshot string) error
    DeleteSnapshot(ctx context.Context, instance, snapshot string) error

    Exec(ctx context.Context, name string, argv []string, opt ExecOptions) (int, error)
    WaitReady(ctx context.Context, name string, opt WaitOptions) error
}

type ExecOptions struct {
    // Env は利用者が指定した値。Secretを含みうるため表示しない。
    Env map[string]string
    // PublicEnv はidevが注入する値。診断に役立つため表示してよい。
    PublicEnv map[string]string

    Cwd    string
    User   string
    TTY    bool // idev shell が端末に接続されている場合のみ true
    // Term はホストの端末種別（TERM）。TTYを割り当てる場合のみ渡す。
    // これが無いと vim や less が端末を判別できない。
    Term   string
    Stdin  io.Reader
    Stdout io.Writer
    Stderr io.Writer
}
```

interfaceとして定義することで、以下を可能にする。

- 単体テストでのfake実装（Incus daemon不要）
- 将来的な実装差し替え

interfaceには **実際に使う操作だけ** を並べる。
instanceの存在確認は `Instance` と `errors.Is(ErrInstanceNotFound)` で行う。
差し替え時の負担がそのまま増えるため、使わない操作を含めない。

config と device の書き込みを `UpdateInstance` 1つにまとめているのはこの規則による。
Incus APIのPUTが元々1つであり、idev側で4つに割っていたものを戻した形である。

---

### 5.7.1 実装方針

Incus操作は公式Go client library

```text
github.com/lxc/incus/v6/client
```

を用いた実装 (`internal/incus/api.go`) に一本化する。利点：

- CLI出力のパースが不要になる
- 型付きのAPIレスポンスを扱える
- CLIのバージョン差異や出力形式の変更の影響を受けにくい
- `incus` コマンドの存在に依存しない

project・imageの解決には `incus` コマンドと同じ設定
（`~/.config/incus/config.yml`）を読む (`internal/incus/connect.go`)。
利用者から見た挙動を `incus` コマンドと揃えるためである。
ただし接続先remoteは常に `local` に固定する（5.6）。

image aliasは instance種別ごとに別のimageを指すため、解決の際は
常にコンテナ用のimageを要求する（[03-configuration.md](03-configuration.md) 3.6.2）。

### 5.7.2 端末を伴う実行

`idev shell` のようにTTYを割り当てる実行では、ホスト側の端末操作が要る
(`internal/incus/exec_tty.go`)。

- 入力をそのままコンテナへ渡すため、ホストの端末をraw modeへ切り替える。
  **失敗しても必ず元へ戻す**（戻さないとシェルが壊れる）
- 端末サイズを `InstanceExecPost.Width` / `Height` で渡す
- SIGWINCH を受けたら、制御用websocketへ `window-resize` を送る
- ホストの `TERM` をコンテナへ渡す。Incusは既定値を補わないため、
  渡さないと `vim` や `less` が端末を判別できない

端末操作は `Console` interfaceの背後に置き、テストではfakeへ差し替える。
サイズを取得できない場合はIncus側の既定に任せ、サイズ送信に失敗した場合は
表示の乱れにとどめて実行そのものは継続する。

### 5.7.3 中断の伝達

制御用websocketは端末を伴わない実行でも開く。

`idev` が中断された場合（Ctrl-C / SIGTERM）、この経路で
コンテナ内のプロセスへ `SIGTERM` を送る。伝えないとパッケージ導入などが
走り続け、次の `idev up` がロック待ちなどで衝突する。

同様に、imageの取得（instance作成）と出力の中継待ちも中断できるようにする。
どちらも数分かかることがあり、待っている間に応答しなくなると
利用者は強制終了するしかなくなる。

---

### 5.7.4 楽観的並行制御

`Instance` が返す `ETag` は、その読み取りを識別する。`UpdateInstance` へ渡すと、
読み取り以降にinstanceが変更されていた場合に書き込みを拒否し `ErrChanged` を返す。

idevは「どのvolumeが自分のものか」「どのdeviceを設定したか」「再起動が要るか」を
instanceの読み取りから決め、数回のAPI呼び出しを挟んでから書き戻す。
別の端末で動くもう一つのidevが同じ窓の中で読み書きすると、
後から書いた側が先に書いた側の記録を黙って消す。
記録から落ちたvolumeは、以後どのコマンドからも名前を得られない。

configとdeviceを1回の書き込みにまとめる理由は2つある。

1つは、これらが1つの読み取りから下した1つの判断だからである。
分けると、2回目は1回目が使い切ったETagに対して書くことになる。

もう1つは、記録がdeviceを説明しているからである。
別々に書くと、記録がdeviceを落としたあとで削除に失敗した場合、
deviceは接続されたまま記録から外れる。
idevが外すのは記録が「ある」と言っているものだけなので、以後どの実行もそれを外さない。
1回の書き込みなら、全部適用されるか何も適用されないかのどちらかになる。

`etag` が空文字列の場合は検査しない。
自分で読み直して1つのキーだけを決める書き込み（restart-pending）はこれを使う。

