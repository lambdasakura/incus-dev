package incus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"sigs.k8s.io/yaml"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
)

// CLI は incus コマンドを呼び出す Client 実装。
type CLI struct {
	Runner runner.Runner
	// Remote はIncus remote名。空またはlocalの場合は修飾しない。
	Remote string
	// Project はIncus project名。
	Project string
}

var _ Client = (*CLI)(nil)

// qualify はremoteでinstance名を修飾する。
func (c *CLI) qualify(name string) string {
	if remote := c.remoteRef(); remote != "" {
		return remote + name
	}
	return name
}

// remoteRef は remote 指定の接頭辞（"name:"）を返す。ローカルの場合は空。
func (c *CLI) remoteRef() string {
	if c.Remote == "" || c.Remote == "local" {
		return ""
	}
	return c.Remote + ":"
}

// args はサブコマンドの直後にグローバルフラグを挿入した引数列を返す。
func (c *CLI) args(sub []string, rest ...string) []string {
	out := make([]string, 0, len(sub)+len(rest)+2)
	out = append(out, sub...)
	if c.Project != "" {
		out = append(out, "--project", c.Project)
	}
	return append(out, rest...)
}

func (c *CLI) run(ctx context.Context, label string, args []string) (runner.Result, error) {
	return c.Runner.Run(ctx, runner.Command{Label: label, Name: "incus", Args: args})
}

// Instance はinstanceの状態を取得する。存在しない場合は ErrInstanceNotFound を返す。
func (c *CLI) Instance(ctx context.Context, name string) (*Instance, error) {
	res, err := c.run(ctx, "list instances",
		c.args([]string{"list"}, "--format", "json", c.qualify(name)))
	if err != nil {
		return nil, err
	}

	var instances []Instance
	if err := json.Unmarshal(res.Stdout, &instances); err != nil {
		return nil, fmt.Errorf("parse instance list: %w", err)
	}
	for i := range instances {
		// incus list は前方一致で複数返しうるため、完全一致のみ採用する。
		if instances[i].Name == name {
			return &instances[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrInstanceNotFound, name)
}

// InstanceExists はinstanceの存在を返す。
func (c *CLI) InstanceExists(ctx context.Context, name string) (bool, error) {
	_, err := c.Instance(ctx, name)
	switch {
	case err == nil:
		return true, nil
	case isNotFound(err):
		return false, nil
	default:
		return false, err
	}
}

func isNotFound(err error) bool {
	return errors.Is(err, ErrInstanceNotFound)
}

// CreateInstance はinstanceを作成する（起動はしない）。
//
// config と devices は標準入力へYAMLで渡す。
// incus create の -c / -d フラグはprofile上の既存deviceの上書きしか行えず、
// 新規deviceを作成できないため。
func (c *CLI) CreateInstance(ctx context.Context, spec InstanceSpec) error {
	args := c.args([]string{"create"}, spec.Image, c.qualify(spec.Name))

	switch {
	case spec.NoProfiles:
		args = append(args, "--no-profiles")
	default:
		for _, p := range spec.Profiles {
			args = append(args, "-p", p)
		}
	}
	if spec.Type == "virtual-machine" {
		args = append(args, "--vm")
	}

	payload, err := createPayload(spec)
	if err != nil {
		return err
	}

	_, err = c.Runner.Run(ctx, runner.Command{
		Label: "create instance " + spec.Name,
		Name:  "incus",
		Args:  args,
		Stdin: bytes.NewReader(payload),
	})
	return err
}

// createPayload は incus create へ渡すYAMLを組み立てる。
func createPayload(spec InstanceSpec) ([]byte, error) {
	doc := map[string]any{}
	if len(spec.Config) > 0 {
		doc["config"] = spec.Config
	}
	if len(spec.Devices) > 0 {
		devices := make(map[string]map[string]string, len(spec.Devices))
		for name, dev := range spec.Devices {
			devices[name] = dev
		}
		doc["devices"] = devices
	}
	if len(doc) == 0 {
		return nil, nil
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("build instance payload: %w", err)
	}
	return data, nil
}

// StartInstance はinstanceを起動する。
func (c *CLI) StartInstance(ctx context.Context, name string) error {
	_, err := c.run(ctx, "start instance "+name, c.args([]string{"start"}, c.qualify(name)))
	return err
}

// StopInstance はinstanceを停止する。
func (c *CLI) StopInstance(ctx context.Context, name string) error {
	_, err := c.run(ctx, "stop instance "+name, c.args([]string{"stop"}, c.qualify(name)))
	return err
}

// DeleteInstance はinstanceを削除する。
func (c *CLI) DeleteInstance(ctx context.Context, name string) error {
	_, err := c.run(ctx, "delete instance "+name, c.args([]string{"delete"}, "--force", c.qualify(name)))
	return err
}

// ApplyConfig は指定されたconfigキーを設定する。
// 宣言されていないキーには触れない（仕様 05-incus.md 5.4.3）。
func (c *CLI) ApplyConfig(ctx context.Context, name string, config map[string]string) error {
	if len(config) == 0 {
		return nil
	}
	args := runner.Args(c.args([]string{"config", "set"}, c.qualify(name))...)
	for _, k := range sortedKeys(config) {
		args.AddSecret(k + "=" + config[k])
	}

	cmd := runner.Command{Label: "set config on " + name, Name: "incus"}
	args.Apply(&cmd)

	_, err := c.Runner.Run(ctx, cmd)
	return err
}

// UnsetConfig は指定されたconfigキーを削除する。
//
// devkit自身が設定したキー（idmap方式の切り替えなど）を取り消すために使う。
// 利用者が書いたキーへは使わない（仕様 05-incus.md 5.4.4）。
func (c *CLI) UnsetConfig(ctx context.Context, name string, keys []string) error {
	for _, k := range keys {
		if _, err := c.run(ctx, "unset config on "+name,
			c.args([]string{"config", "unset"}, c.qualify(name), k)); err != nil {
			return err
		}
	}
	return nil
}

// ApplyDevices は宣言されたdeviceを設定する。
// 既存deviceは差分のみ更新し、型が変わった場合は作り直す。
func (c *CLI) ApplyDevices(ctx context.Context, name string, devices map[string]Device) error {
	if len(devices) == 0 {
		return nil
	}
	inst, err := c.Instance(ctx, name)
	if err != nil {
		return err
	}

	for _, devName := range sortedDeviceNames(devices) {
		want := devices[devName]
		current, exists := inst.Devices[devName]

		if exists && current.Type() != want.Type() {
			if _, err := c.run(ctx, "remove device "+devName,
				c.args([]string{"config", "device", "remove"}, c.qualify(name), devName)); err != nil {
				return err
			}
			exists = false
		}

		if !exists {
			args := runner.Args(c.args([]string{"config", "device", "add"},
				c.qualify(name), devName, want.Type())...)
			for _, k := range sortedKeys(want) {
				if k != "type" {
					args.AddSecret(k + "=" + want[k])
				}
			}

			cmd := runner.Command{Label: "add device " + devName, Name: "incus"}
			args.Apply(&cmd)

			if _, err := c.Runner.Run(ctx, cmd); err != nil {
				return err
			}
			continue
		}

		base := c.args([]string{"config", "device", "set"}, c.qualify(name), devName)
		args := runner.Args(base...)

		changed := false
		for _, k := range sortedKeys(want) {
			if k == "type" || current[k] == want[k] {
				continue
			}
			args.AddSecret(k + "=" + want[k])
			changed = true
		}
		if !changed {
			continue
		}

		cmd := runner.Command{Label: "set device " + devName, Name: "incus"}
		args.Apply(&cmd)

		if _, err := c.Runner.Run(ctx, cmd); err != nil {
			return err
		}
	}
	return nil
}

// RemoveDevices は指定されたdeviceを削除する。
//
// devkit自身が作成したdeviceを取り消すために使う。
// 利用者が手で追加したdeviceへは使わない（仕様 05-incus.md 5.4.4）。
func (c *CLI) RemoveDevices(ctx context.Context, name string, devices []string) error {
	for _, dev := range devices {
		if _, err := c.run(ctx, "remove device "+dev,
			c.args([]string{"config", "device", "remove"}, c.qualify(name), dev)); err != nil {
			return err
		}
	}
	return nil
}

// ProfileExists はProfileの存在を返す。devkitはProfileを作成しない（REQ-007）。
func (c *CLI) ProfileExists(ctx context.Context, name string) (bool, error) {
	args := c.args([]string{"profile", "list"}, "--format", "json")
	if remote := c.remoteRef(); remote != "" {
		// incus profile list [<remote>:] — remoteを省略するとローカルを見てしまう
		args = append(args, remote)
	}

	res, err := c.run(ctx, "list profiles", args)
	if err != nil {
		return false, err
	}
	var profiles []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(res.Stdout, &profiles); err != nil {
		return false, fmt.Errorf("parse profile list: %w", err)
	}
	for _, p := range profiles {
		if p.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// Exec はコンテナ内でコマンドを実行し、終了コードを返す。
func (c *CLI) Exec(ctx context.Context, name string, argv []string, opt ExecOptions) (int, error) {
	args := runner.Args(c.args([]string{"exec"}, c.qualify(name))...)

	if opt.Cwd != "" {
		args.Add("--cwd", opt.Cwd)
	}
	for _, k := range sortedKeys(opt.PublicEnv) {
		args.Add("--env", k+"="+opt.PublicEnv[k])
	}
	for _, k := range sortedKeys(opt.Env) {
		args.Add("--env").AddSecret(k + "=" + opt.Env[k])
	}
	if opt.User != "" {
		uid, ok := numericUser(opt.User)
		if !ok {
			// incus exec --user はUIDのみを受け付ける。
			// ユーザー名の解決は呼び出し側の責務であり、黙って無視しない。
			return 0, fmt.Errorf("exec user must be a numeric uid, got %q", opt.User)
		}
		args.Add("--user", uid)
	}
	if opt.TTY {
		args.Add("-t")
	} else {
		args.Add("-T")
	}

	// 実行するコマンドは診断の中心であり、失敗時に表示する（仕様 04-cli.md 4.10）。
	// 複数行のスクリプトは表示時に折り畳まれる（runner.Command.String）。
	args.Add("--")
	args.Add(argv...)

	cmd := runner.Command{
		Label:       "exec in " + name,
		Name:        "incus",
		Stdin:       opt.Stdin,
		Stdout:      opt.Stdout,
		Stderr:      opt.Stderr,
		Interactive: opt.TTY,
	}
	args.Apply(&cmd)

	res, err := c.Runner.Run(ctx, cmd)
	return res.ExitCode, err
}

// numericUser は数値のユーザー指定を返す。
// incus exec --user はUIDのみを受け付けるため、名前指定は呼び出し側で扱う。
func numericUser(user string) (string, bool) {
	if _, err := strconv.Atoi(user); err != nil {
		return "", false
	}
	return user, true
}

// WaitReady はinstanceがprovisioningを受けられる状態になるまで待つ。
//
// コマンドを実行できるようになった時点では、まだネットワークアドレスが
// 割り当てられていないことがある。パッケージの導入を伴うステップが
// 初回から失敗しないよう、アドレスの割り当ても待つ。
func (c *CLI) WaitReady(ctx context.Context, name string, opt WaitOptions) error {
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

	if err := c.waitExec(ctx, name, opt); err != nil {
		return err
	}
	return c.waitNetwork(ctx, name, opt)
}

// waitExec はコンテナ内でコマンドを実行できるようになるまで待つ。
func (c *CLI) waitExec(ctx context.Context, name string, opt WaitOptions) error {
	deadline := time.Now().Add(opt.Timeout)
	var lastErr error
	for {
		code, err := c.Exec(ctx, name, []string{"true"}, ExecOptions{})
		if err == nil && code == 0 {
			return nil
		}
		if err != nil {
			lastErr = err
		}

		if time.Now().After(deadline) {
			if lastErr == nil {
				return fmt.Errorf("instance %s did not become ready within %s", name, opt.Timeout)
			}
			return fmt.Errorf("instance %s did not become ready within %s: %w", name, opt.Timeout, lastErr)
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
func (c *CLI) waitNetwork(ctx context.Context, name string, opt WaitOptions) error {
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

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opt.Interval):
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

func sortedDeviceNames(m map[string]Device) []string {
	return sortedKeys(m)
}
