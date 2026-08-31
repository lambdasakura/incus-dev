// Package incus はIncus instanceのライフサイクル操作を集約する。
//
// CLI層からIncusコマンド文字列を直接組み立てないための境界である
// （仕様 05-incus.md 5.7）。
package incus

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrInstanceNotFound はinstanceが存在しないことを示す。
var ErrInstanceNotFound = errors.New("instance not found")

// ErrNetworkNotReady はinstanceにネットワークアドレスが割り当てられなかったことを示す。
//
// 静的設定などアドレスが現れない構成もありうるため、
// これを致命的な失敗として扱うかは呼び出し側が判断する。
var ErrNetworkNotReady = errors.New("network address not assigned")

// Device はIncus deviceの定義（キーと値の素通し）。
type Device map[string]string

// Type はdeviceの型を返す。
func (d Device) Type() string { return d["type"] }

// Instance はIncus instanceの状態。
type Instance struct {
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Type     string            `json:"type"`
	Profiles []string          `json:"profiles"`
	Config   map[string]string `json:"config"`
	Devices  map[string]Device `json:"devices"`
	// ExpandedDevices はProfile由来を含む実効的なdevice。
	ExpandedDevices map[string]Device `json:"expanded_devices"`
	State           *InstanceState    `json:"state"`
}

// InstanceState はinstanceの実行時状態。
type InstanceState struct {
	Network map[string]NetworkState `json:"network"`
}

// NetworkState はネットワークインターフェースの状態。
type NetworkState struct {
	Addresses []NetworkAddress `json:"addresses"`
}

// NetworkAddress は割り当てられたアドレス。
type NetworkAddress struct {
	Family  string `json:"family"`
	Address string `json:"address"`
	Scope   string `json:"scope"`
}

// IsRunning はinstanceが実行中かを返す。
func (i *Instance) IsRunning() bool { return i.Status == "Running" }

// HasNIC はネットワークインターフェースを持つかを返す。
func (i *Instance) HasNIC() bool {
	for _, dev := range i.ExpandedDevices {
		if dev.Type() == "nic" {
			return true
		}
	}
	return false
}

// GlobalAddresses は割り当てられたグローバルアドレスを返す。
func (i *Instance) GlobalAddresses() []NetworkAddress {
	if i.State == nil {
		return nil
	}

	var out []NetworkAddress
	for name, net := range i.State.Network {
		if name == "lo" {
			continue
		}
		for _, addr := range net.Addresses {
			if addr.Scope == "global" {
				out = append(out, addr)
			}
		}
	}
	return out
}

// HasGlobalAddress はグローバルアドレスが1つでも割り当てられているかを返す。
func (i *Instance) HasGlobalAddress() bool {
	return len(i.GlobalAddresses()) > 0
}

// HasIPv4Address はIPv4のグローバルアドレスが割り当てられているかを返す。
//
// Incusの既定のブリッジではIPv6(ULA)が先に付き、その時点ではまだ
// デフォルトルートが無い。実際に外部へ出られるのはIPv4が付いてからなので、
// ready判定にはこちらを使う。
func (i *Instance) HasIPv4Address() bool {
	for _, addr := range i.GlobalAddresses() {
		if addr.Family == "inet" {
			return true
		}
	}
	return false
}

// InstanceSpec はinstance作成時の指定。
type InstanceSpec struct {
	Name  string
	Image string
	// Type は container（既定）または virtual-machine。
	Type string
	// Profiles は適用するProfile名。
	Profiles []string
	// NoProfiles が真の場合、Profileを一切適用しない（profiles: [] に対応）。
	NoProfiles bool
	Config     map[string]string
	// Devices は作成時に設定するdevice。
	// profileを適用しない場合、root diskは作成時に必要となる。
	Devices map[string]Device
}

// ExecOptions はコンテナ内でのコマンド実行オプション。
type ExecOptions struct {
	// Env は利用者が指定した環境変数。Secretを含みうるため表示時に値を隠す。
	Env map[string]string
	// PublicEnv はdevkitが注入する環境変数。診断に役立つため表示する。
	PublicEnv map[string]string
	Cwd       string
	User      string
	// TTY が真の場合、擬似端末を割り当てて標準入出力を引き継ぐ。
	TTY    bool
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// WaitOptions はinstanceのready待ちの制御。
type WaitOptions struct {
	// Timeout はコマンドを実行できるようになるまでの待ち時間。
	Timeout time.Duration
	// NetworkTimeout はネットワークアドレスが割り当てられるまでの待ち時間。
	NetworkTimeout time.Duration
	// IPv4Grace はIPv6のみが割り当てられた後、IPv4を待つ時間。
	// IPv6のみの環境で不必要に待たないよう短くする。
	IPv4Grace time.Duration
	Interval  time.Duration
}

// Client はIncus操作のインターフェース。テストではfakeへ差し替える。
//
// 実装差し替えの負担を増やさないよう、実際に使う操作だけを並べる。
// instanceの存在確認は Instance と errors.Is(ErrInstanceNotFound) で行う。
type Client interface {
	Instance(ctx context.Context, name string) (*Instance, error)

	CreateInstance(ctx context.Context, spec InstanceSpec) error
	StartInstance(ctx context.Context, name string) error
	StopInstance(ctx context.Context, name string) error
	DeleteInstance(ctx context.Context, name string) error

	ApplyConfig(ctx context.Context, name string, config map[string]string) error
	UnsetConfig(ctx context.Context, name string, keys []string) error
	ApplyDevices(ctx context.Context, name string, devices map[string]Device) error
	RemoveDevices(ctx context.Context, name string, devices []string) error

	ProfileExists(ctx context.Context, name string) (bool, error)

	Exec(ctx context.Context, name string, argv []string, opt ExecOptions) (int, error)
	WaitReady(ctx context.Context, name string, opt WaitOptions) error
}
