package incus

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lambdasakura/incus-dev/internal/runner"
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
	if c.Remote == "" || c.Remote == "local" {
		return name
	}
	return c.Remote + ":" + name
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
	return err != nil && strings.Contains(err.Error(), ErrInstanceNotFound.Error())
}

// CreateInstance はinstanceを作成する（起動はしない）。
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
	for _, k := range sortedKeys(spec.Config) {
		args = append(args, "-c", k+"="+spec.Config[k])
	}
	if spec.Type == "virtual-machine" {
		args = append(args, "--vm")
	}

	_, err := c.run(ctx, "create instance "+spec.Name, args)
	return err
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
	args := c.args([]string{"config", "set"}, c.qualify(name))
	for _, k := range sortedKeys(config) {
		args = append(args, k+"="+config[k])
	}
	_, err := c.run(ctx, "set config on "+name, args)
	return err
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
			args := c.args([]string{"config", "device", "add"}, c.qualify(name), devName, want.Type())
			for _, k := range sortedKeys(want) {
				if k == "type" {
					continue
				}
				args = append(args, k+"="+want[k])
			}
			if _, err := c.run(ctx, "add device "+devName, args); err != nil {
				return err
			}
			continue
		}

		var changed []string
		for _, k := range sortedKeys(want) {
			if k == "type" {
				continue
			}
			if current[k] != want[k] {
				changed = append(changed, k+"="+want[k])
			}
		}
		if len(changed) == 0 {
			continue
		}
		args := append(c.args([]string{"config", "device", "set"}, c.qualify(name), devName), changed...)
		if _, err := c.run(ctx, "set device "+devName, args); err != nil {
			return err
		}
	}
	return nil
}

// ProfileExists はProfileの存在を返す。devkitはProfileを作成しない（REQ-007）。
func (c *CLI) ProfileExists(ctx context.Context, name string) (bool, error) {
	res, err := c.run(ctx, "list profiles", c.args([]string{"profile", "list"}, "--format", "json"))
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
	args := c.args([]string{"exec"}, c.qualify(name))

	if opt.Cwd != "" {
		args = append(args, "--cwd", opt.Cwd)
	}
	for _, k := range sortedKeys(opt.Env) {
		args = append(args, "--env", k+"="+opt.Env[k])
	}
	if uid, ok := numericUser(opt.User); ok {
		args = append(args, "--user", uid)
	}
	if opt.TTY {
		args = append(args, "-t")
	} else {
		args = append(args, "-T")
	}
	args = append(args, "--")
	args = append(args, argv...)

	res, err := c.Runner.Run(ctx, runner.Command{
		Label:       "exec in " + name,
		Name:        "incus",
		Args:        args,
		Stdin:       opt.Stdin,
		Stdout:      opt.Stdout,
		Stderr:      opt.Stderr,
		Interactive: opt.TTY,
	})
	return res.ExitCode, err
}

// numericUser は数値のユーザー指定を返す。
// incus exec --user はUIDのみを受け付けるため、名前指定は呼び出し側で扱う。
func numericUser(user string) (string, bool) {
	if user == "" {
		return "", false
	}
	if _, err := strconv.Atoi(user); err != nil {
		return "", false
	}
	return user, true
}

// WaitReady はコンテナ内でコマンドを実行できるようになるまで待つ。
func (c *CLI) WaitReady(ctx context.Context, name string, opt WaitOptions) error {
	if opt.Timeout <= 0 {
		opt.Timeout = 60 * time.Second
	}
	if opt.Interval <= 0 {
		opt.Interval = 500 * time.Millisecond
	}

	deadline := time.Now().Add(opt.Timeout)
	var lastErr error
	for {
		code, err := c.Exec(ctx, name, []string{"true"}, ExecOptions{})
		if err == nil && code == 0 {
			return nil
		}
		lastErr = err

		if time.Now().After(deadline) {
			return fmt.Errorf("instance %s did not become ready within %s: %w", name, opt.Timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opt.Interval):
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedDeviceNames(m map[string]Device) []string {
	return sortedKeys(m)
}
