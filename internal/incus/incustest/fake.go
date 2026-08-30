// Package incustest はテスト用の incus.Client 実装を提供する。
package incustest

import (
	"context"
	"fmt"
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

func (f *Fake) record(format string, args ...any) {
	f.Calls = append(f.Calls, fmt.Sprintf(format, args...))
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

func (f *Fake) Instance(_ context.Context, name string) (*incus.Instance, error) {
	f.record("instance %s", name)
	inst, ok := f.Instances[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	return inst, nil
}

func (f *Fake) InstanceExists(ctx context.Context, name string) (bool, error) {
	_, err := f.Instance(ctx, name)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func (f *Fake) CreateInstance(_ context.Context, spec incus.InstanceSpec) error {
	f.record("create %s image=%s profiles=%v noprofiles=%v", spec.Name, spec.Image, spec.Profiles, spec.NoProfiles)
	config := map[string]string{}
	for k, v := range spec.Config {
		config[k] = v
	}
	f.Instances[spec.Name] = &incus.Instance{
		Name:     spec.Name,
		Status:   "Stopped",
		Type:     spec.Type,
		Profiles: spec.Profiles,
		Config:   config,
		Devices:  map[string]incus.Device{},
	}
	return nil
}

func (f *Fake) StartInstance(_ context.Context, name string) error {
	f.record("start %s", name)
	if inst, ok := f.Instances[name]; ok {
		inst.Status = "Running"
	}
	return nil
}

func (f *Fake) StopInstance(_ context.Context, name string) error {
	f.record("stop %s", name)
	if inst, ok := f.Instances[name]; ok {
		inst.Status = "Stopped"
	}
	return nil
}

func (f *Fake) DeleteInstance(_ context.Context, name string) error {
	f.record("delete %s", name)
	delete(f.Instances, name)
	return nil
}

func (f *Fake) ApplyConfig(_ context.Context, name string, config map[string]string) error {
	if len(config) == 0 {
		return nil
	}
	f.record("config %s %v", name, sortedPairs(config))
	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	for k, v := range config {
		inst.Config[k] = v
	}
	return nil
}

func (f *Fake) ApplyDevices(_ context.Context, name string, devices map[string]incus.Device) error {
	if len(devices) == 0 {
		return nil
	}
	f.record("devices %s %v", name, sortedDeviceNames(devices))
	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	for devName, dev := range devices {
		inst.Devices[devName] = dev
	}
	return nil
}

func (f *Fake) ProfileExists(_ context.Context, name string) (bool, error) {
	for _, p := range f.Profiles {
		if p == name {
			return true, nil
		}
	}
	return false, nil
}

func (f *Fake) Exec(_ context.Context, name string, argv []string, opt incus.ExecOptions) (int, error) {
	f.record("exec %s %s", name, strings.Join(argv, " "))
	f.Execs = append(f.Execs, argv)
	if f.ExecFunc != nil {
		return f.ExecFunc(name, argv, opt)
	}
	return 0, nil
}

func (f *Fake) WaitReady(_ context.Context, name string, _ incus.WaitOptions) error {
	f.record("waitready %s", name)
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
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
