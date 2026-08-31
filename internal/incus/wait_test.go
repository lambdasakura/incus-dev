package incus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lxc/incus/v6/shared/api"
)

// nic は eth0 を持つinstanceにする。
func nic(full *api.InstanceFull) *api.InstanceFull {
	full.ExpandedDevices = map[string]map[string]string{"eth0": {"type": "nic"}}
	return full
}

// addresses は割り当て済みアドレスを設定する。
func addresses(full *api.InstanceFull, addrs ...api.InstanceStateNetworkAddress) *api.InstanceFull {
	full.State = &api.InstanceState{Network: map[string]api.InstanceStateNetwork{
		"eth0": {Addresses: addrs},
		"lo":   {Addresses: []api.InstanceStateNetworkAddress{{Family: "inet", Address: "127.0.0.1", Scope: "local"}}},
	}}
	return full
}

var (
	ipv4 = api.InstanceStateNetworkAddress{Family: "inet", Address: "10.0.0.2", Scope: "global"}
	ipv6 = api.InstanceStateNetworkAddress{Family: "inet6", Address: "fd42::2", Scope: "global"}
)

// fastWait は待ち時間を詰めたテスト用の指定。
func fastWait() WaitOptions {
	return WaitOptions{
		Timeout:        200 * time.Millisecond,
		NetworkTimeout: 200 * time.Millisecond,
		IPv4Grace:      20 * time.Millisecond,
		Interval:       time.Millisecond,
	}
}

// コマンドを実行でき、IPv4が付いていれば待たない
func TestWaitReadyReturnsWhenReady(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv4)
	a, _ := newAPI(f)

	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
}

// 既定値でも動くこと（呼び出し側が指定を省略できる）
func TestWaitReadyUsesDefaults(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv4)
	a, _ := newAPI(f)

	if err := a.WaitReady(context.Background(), "dev-x", WaitOptions{}); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
}

// 起動直後はexecできないため、できるようになるまで繰り返す
func TestWaitReadyRetriesUntilExecSucceeds(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv4)
	f.execMeta = map[string]any{"return": float64(1)}
	a, _ := newAPI(f)

	attempts := 0
	f.beforeExec = func() {
		attempts++
		if attempts >= 3 {
			f.execMeta = map[string]any{"return": float64(0)}
		}
	}

	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if attempts < 3 {
		t.Errorf("試行回数 = %d, 成功するまで繰り返すこと", attempts)
	}
}

// 時間切れは、待った時間が分かるエラーにする
func TestWaitReadyTimesOut(t *testing.T) {
	tests := []struct {
		name string
		exec func(*fakeServer)
		want string
	}{
		{
			name: "実行できるが終了コードが0にならない",
			exec: func(f *fakeServer) { f.execMeta = map[string]any{"return": float64(1)} },
			want: "did not become ready within",
		},
		{
			name: "実行そのものが失敗し続ける",
			exec: func(f *fakeServer) { f.err["ExecInstance"] = errAPI },
			want: "api failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer()
			addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv4)
			tt.exec(f)
			a, _ := newAPI(f)

			err := a.WaitReady(context.Background(), "dev-x", fastWait())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

// 中断されたら待たずに戻る
func TestWaitReadyStopsOnCancel(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv4)
	f.execMeta = map[string]any{"return": float64(1)}
	a, _ := newAPI(f)

	ctx, cancel := context.WithCancel(context.Background())
	f.beforeExec = cancel

	if err := a.WaitReady(ctx, "dev-x", fastWait()); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// NICを持たないinstanceではアドレスを待たない
func TestWaitReadyWithoutNIC(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
}

// IPv4のDHCPが完了するまで待つ
func TestWaitReadyWaitsForIPv4(t *testing.T) {
	f := newFakeServer()
	full := addresses(nic(f.addInstance("dev-x", api.InstancePut{})))
	a, _ := newAPI(f)

	calls := 0
	f.beforeInstance = func() {
		calls++
		if calls >= 3 {
			addresses(full, ipv6, ipv4)
		}
	}

	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if calls < 3 {
		t.Errorf("取得回数 = %d, IPv4が付くまで待つこと", calls)
	}
}

// IPv6しか付かない環境で無期限に待たない
func TestWaitReadyGivesUpWaitingForIPv4(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv6)
	a, _ := newAPI(f)

	start := time.Now()
	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Errorf("待ち時間 = %v, IPv4の猶予を超えて待たないこと", elapsed)
	}
}

// アドレスが1つも付かない場合は、呼び出し側が判断できるエラーにする
func TestWaitReadyReportsNetworkNotReady(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})))
	a, _ := newAPI(f)

	err := a.WaitReady(context.Background(), "dev-x", fastWait())
	if !errors.Is(err, ErrNetworkNotReady) {
		t.Errorf("error = %v, want ErrNetworkNotReady", err)
	}
}

// アドレスを待つ間に中断された場合
func TestWaitNetworkStopsOnCancel(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})))
	a, _ := newAPI(f)

	ctx, cancel := context.WithCancel(context.Background())
	f.beforeInstance = cancel

	if err := a.WaitReady(ctx, "dev-x", fastWait()); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// instanceの状態を取得できない場合はそのまま返す
func TestWaitNetworkPropagatesError(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})))
	a, _ := newAPI(f)

	f.beforeInstance = func() { f.err["GetInstanceFull"] = errAPI }

	if err := a.WaitReady(context.Background(), "dev-x", fastWait()); !errors.Is(err, errAPI) {
		t.Errorf("error = %v, want %v", err, errAPI)
	}
}

// IPv6だけが付いた状態でも、NetworkTimeout を超えたら待たない。
// grace の判定だけが唯一の脱出路だと、退行時に永久に待ち続けてしまう。
func TestWaitNetworkStopsAtTimeout(t *testing.T) {
	f := newFakeServer()
	addresses(nic(f.addInstance("dev-x", api.InstancePut{})), ipv6)
	a, _ := newAPI(f)

	opt := fastWait()
	opt.IPv4Grace = time.Hour // grace では抜けられない
	opt.NetworkTimeout = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := a.WaitReady(ctx, "dev-x", opt); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if ctx.Err() != nil {
		t.Error("NetworkTimeout を超えても待ち続けている")
	}
}

// instanceを取得できなくなった場合
func TestStopInstanceMissing(t *testing.T) {
	a, _ := newAPI(newFakeServer())

	if err := a.StopInstance(context.Background(), "dev-missing"); !errors.Is(err, ErrInstanceNotFound) {
		t.Errorf("error = %v, want ErrInstanceNotFound", err)
	}
}
