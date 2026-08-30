// Package incustest はテスト用の incus.Client 実装を提供する。
package incustest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
)

// Fake はIncus daemonを必要としない incus.Client 実装。
type Fake struct {
	// Instances は存在するinstance。キーはinstance名。
	Instances map[string]*incus.Instance
	// Profiles は存在するProfile名。
	Profiles []string

	// ExecFunc が設定されていれば Exec の応答に使用する。
	ExecFunc func(name string, argv []string, opt incus.ExecOptions) (int, error)
	// FailReady が真の場合 WaitReady が失敗する。
	FailReady bool
	// FailOn は操作名のprefixに対して返すエラー。
	// 例: {"create": errBoom} とすると CreateInstance が失敗する。
	FailOn map[string]error
	// Hook は各操作の直前に呼ばれる。非nilのエラーを返すとその操作が失敗する。
	// 「2回目の呼び出しだけ失敗させる」といった制御に使う。
	Hook func(call string) error

	// Calls は呼び出し順の記録（例: "create dev-x", "start dev-x"）。
	Calls []string
	// Execs は実行されたargvの記録。
	Execs [][]string
}

var _ incus.Client = (*Fake)(nil)

// New は default profile を持つFakeを返す。
func New() *Fake {
	return &Fake{
		Instances: map[string]*incus.Instance{},
		Profiles:  []string{"default"},
	}
}

// AddInstance はinstanceを登録する。
func (f *Fake) AddInstance(inst *incus.Instance) *Fake {
	if inst.Config == nil {
		inst.Config = map[string]string{}
	}
	if inst.Devices == nil {
		inst.Devices = map[string]incus.Device{}
	}
	f.Instances[inst.Name] = inst
	return f
}

// record は呼び出しを記録し、FailOn に一致すればそのエラーを返す。
func (f *Fake) record(format string, args ...any) error {
	call := fmt.Sprintf(format, args...)
	f.Calls = append(f.Calls, call)

	for prefix, err := range f.FailOn {
		if strings.HasPrefix(call, prefix) {
			return err
		}
	}
	if f.Hook != nil {
		return f.Hook(call)
	}
	return nil
}

// Called は指定のprefixで始まる呼び出しがあったかを返す。
func (f *Fake) Called(prefix string) bool {
	for _, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// Instance は登録済みinstanceを返す。存在しなければ incus.ErrInstanceNotFound を返す。
func (f *Fake) Instance(_ context.Context, name string) (*incus.Instance, error) {
	if err := f.record("instance %s", name); err != nil {
		return nil, err
	}
	inst, ok := f.Instances[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	return inst, nil
}

// InstanceExists はinstanceの存在を返す。
func (f *Fake) InstanceExists(ctx context.Context, name string) (bool, error) {
	if _, err := f.Instance(ctx, name); err != nil {
		//nolint:nilerr // 存在しないことはエラーではない
		return false, nil
	}
	return true, nil
}

// CreateInstance はinstanceを登録する。状態は Stopped になる。
func (f *Fake) CreateInstance(_ context.Context, spec incus.InstanceSpec) error {
	if err := f.record("create %s image=%s profiles=%v noprofiles=%v", spec.Name, spec.Image, spec.Profiles, spec.NoProfiles); err != nil {
		return err
	}
	config := map[string]string{}
	for k, v := range spec.Config {
		config[k] = v
	}
	devices := map[string]incus.Device{}
	for name, dev := range spec.Devices {
		devices[name] = dev
	}
	f.Instances[spec.Name] = &incus.Instance{
		Name:     spec.Name,
		Status:   "Stopped",
		Type:     spec.Type,
		Profiles: spec.Profiles,
		Config:   config,
		Devices:  devices,
	}
	return nil
}

// StartInstance はinstanceの状態を Running にする。
func (f *Fake) StartInstance(_ context.Context, name string) error {
	if err := f.record("start %s", name); err != nil {
		return err
	}
	if inst, ok := f.Instances[name]; ok {
		inst.Status = "Running"
	}
	return nil
}

// StopInstance はinstanceの状態を Stopped にする。
func (f *Fake) StopInstance(_ context.Context, name string) error {
	if err := f.record("stop %s", name); err != nil {
		return err
	}
	if inst, ok := f.Instances[name]; ok {
		inst.Status = "Stopped"
	}
	return nil
}

// DeleteInstance はinstanceを削除する。
func (f *Fake) DeleteInstance(_ context.Context, name string) error {
	if err := f.record("delete %s", name); err != nil {
		return err
	}
	delete(f.Instances, name)
	return nil
}

// ApplyConfig は指定されたconfigキーを反映する。
func (f *Fake) ApplyConfig(_ context.Context, name string, config map[string]string) error {
	if len(config) == 0 {
		return nil
	}
	if err := f.record("config %s %v", name, sortedPairs(config)); err != nil {
		return err
	}
	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	for k, v := range config {
		inst.Config[k] = v
	}
	return nil
}

// UnsetConfig は指定されたconfigキーを削除する。
func (f *Fake) UnsetConfig(_ context.Context, name string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := f.record("unset %s %v", name, keys); err != nil {
		return err
	}
	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	for _, k := range keys {
		delete(inst.Config, k)
	}
	return nil
}

// ApplyDevices は指定されたdeviceを反映する。
func (f *Fake) ApplyDevices(_ context.Context, name string, devices map[string]incus.Device) error {
	if len(devices) == 0 {
		return nil
	}
	if err := f.record("devices %s %v", name, sortedDeviceNames(devices)); err != nil {
		return err
	}
	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	// 本物の ApplyDevices は want に無いキーを消さない。fakeも同じ挙動にする。
	for devName, dev := range devices {
		current, ok := inst.Devices[devName]
		if !ok || current.Type() != dev.Type() {
			inst.Devices[devName] = maps.Clone(dev)
			continue
		}
		for k, v := range dev {
			current[k] = v
		}
	}
	return nil
}

// ProfileExists は Profiles に含まれるかを返す。
func (f *Fake) ProfileExists(_ context.Context, name string) (bool, error) {
	if err := f.record("profile %s", name); err != nil {
		return false, err
	}
	for _, p := range f.Profiles {
		if p == name {
			return true, nil
		}
	}
	return false, nil
}

// Exec は実行内容を記録し、ExecFunc があればその結果を返す。
func (f *Fake) Exec(_ context.Context, name string, argv []string, opt incus.ExecOptions) (int, error) {
	if err := f.record("exec %s %s", name, strings.Join(argv, " ")); err != nil {
		return 1, err
	}

	// 本物は停止中・不在のinstanceに対して失敗する。
	inst, ok := f.Instances[name]
	if !ok {
		return 1, fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	if !inst.IsRunning() {
		return 1, fmt.Errorf("instance %s is not running", name)
	}

	f.Execs = append(f.Execs, argv)
	if f.ExecFunc != nil {
		return f.ExecFunc(name, argv, opt)
	}
	return 0, nil
}

// WaitReady は FailReady が真の場合にエラーを返す。
func (f *Fake) WaitReady(_ context.Context, name string, _ incus.WaitOptions) error {
	if err := f.record("waitready %s", name); err != nil {
		return err
	}
	if f.FailReady {
		return fmt.Errorf("instance %s did not become ready", name)
	}
	return nil
}

func sortedPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, k+"="+m[k])
	}
	return out
}

func sortedDeviceNames(m map[string]incus.Device) []string {
	return sortedKeys(m)
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
