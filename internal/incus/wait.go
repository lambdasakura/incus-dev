package incus

import (
	"context"
	"fmt"
	"time"
)

// waitReady はinstanceがprovisioningを受けられる状態になるまで待つ。
//
// コマンドを実行できるようになった時点では、まだネットワークアドレスが
// 割り当てられていないことがある。パッケージの導入を伴うステップが
// 初回から失敗しないよう、アドレスの割り当ても待つ。
//
// CLI実装とAPI実装で共通の処理であるため、Client を受け取る形にしている。
func waitReady(ctx context.Context, c Client, name string, opt WaitOptions) error {
	if opt.Timeout <= 0 {
		opt.Timeout = 60 * time.Second
	}
	if opt.NetworkTimeout <= 0 {
		opt.NetworkTimeout = 30 * time.Second
	}
	if opt.IPv4Grace <= 0 {
		opt.IPv4Grace = 5 * time.Second
	}
	if opt.Interval <= 0 {
		opt.Interval = 500 * time.Millisecond
	}

	if err := waitExec(ctx, c, name, opt); err != nil {
		return err
	}
	return waitNetwork(ctx, c, name, opt)
}

// waitExec はコンテナ内でコマンドを実行できるようになるまで待つ。
func waitExec(ctx context.Context, c Client, name string, opt WaitOptions) error {
	deadline := time.Now().Add(opt.Timeout)
	for {
		code, err := c.Exec(ctx, name, []string{"true"}, ExecOptions{})
		if err == nil && code == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			// 最後の試行が何で失敗したかを示す。これが無いと
			// 「起動しない」以上のことが分からず、対処のしようがない。
			if err != nil {
				return fmt.Errorf("instance %s did not become ready within %s: %w", name, opt.Timeout, err)
			}
			return fmt.Errorf("instance %s did not become ready within %s (last exit code %d)",
				name, opt.Timeout, code)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opt.Interval):
		}
	}
}

// waitNetwork は外部と通信できる状態になるまで待つ。
//
// Incusの既定のブリッジではIPv6(ULA)が先に付き、IPv4のDHCPが完了して
// デフォルトルートが入るまでは外へ出られない。パッケージ導入を伴う
// ステップが初回から失敗しないよう、IPv4の割り当てまで待つ。
//
// IPv6のみの環境で無駄に待たないよう、IPv6が付いた後は IPv4Grace までしか
// 待たない。NICを持たないinstanceでは待たない。アドレスが1つも
// 付かないまま時間切れになった場合は ErrNetworkNotReady を返す
// （静的設定などもありうるため、致命的な失敗とするかは呼び出し側が判断する）。
func waitNetwork(ctx context.Context, c Client, name string, opt WaitOptions) error {
	limit := time.Now().Add(opt.NetworkTimeout)
	var graceLimit time.Time

	for {
		inst, err := c.Instance(ctx, name)
		if err != nil {
			return err
		}

		switch {
		case !inst.HasNIC(), inst.HasIPv4Address():
			return nil

		case inst.HasGlobalAddress():
			// IPv6だけ付いた状態。IPv4を待つが、無期限には待たない。
			if graceLimit.IsZero() {
				graceLimit = time.Now().Add(opt.IPv4Grace)
			}
			if time.Now().After(graceLimit) {
				return nil
			}

		case time.Now().After(limit):
			return fmt.Errorf("%w: instance %s has no address after %s",
				ErrNetworkNotReady, name, opt.NetworkTimeout)
		}

		// どの経路でも NetworkTimeout を超えたら待たない。
		if time.Now().After(limit) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opt.Interval):
		}
	}
}
