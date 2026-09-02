// Package incustest provides an incus.Client implementation for tests.
package incustest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/incus"
)

// Fake is an incus.Client implementation that needs no Incus daemon.
type Fake struct {
	// Instances are the instances that exist, keyed by name.
	Instances map[string]*incus.Instance
	// Profiles are the names of the profiles that exist.
	Profiles []string
	// SnapshotsByInstance holds each instance's snapshots.
	SnapshotsByInstance map[string][]incus.Snapshot
	// Volumes are the storage volumes that exist, as "pool/name".
	Volumes map[string]bool

	// ExecFunc, when set, decides what Exec returns.
	ExecFunc func(name string, argv []string, opt incus.ExecOptions) (int, error)
	// FailReady makes WaitReady fail.
	FailReady bool
	// NetworkNotReady makes WaitReady return incus.ErrNetworkNotReady.
	NetworkNotReady bool
	// FailOn maps an operation-name prefix to the error to return. For
	// example {"create": errBoom} makes CreateInstance fail.
	FailOn map[string]error
	// Hook is called just before each operation; returning a non-nil error
	// makes that operation fail. Use it for things like "fail only the second
	// call".
	Hook func(call string) error

	// Calls records the calls in order, such as "create dev-x", "start dev-x".
	Calls []string
	// Execs records the argv of each execution.
	Execs [][]string
}

var _ incus.Client = (*Fake)(nil)

// New returns a Fake that has the default profile.
func New() *Fake {
	return &Fake{
		Instances:           map[string]*incus.Instance{},
		SnapshotsByInstance: map[string][]incus.Snapshot{},
		Profiles:            []string{"default"},
	}
}

// AddInstance registers an instance.
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

// record notes the call and returns the matching FailOn error, if any.
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

// Called reports whether any call began with the given prefix.
func (f *Fake) Called(prefix string) bool {
	for _, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// Instance returns a registered instance, or incus.ErrInstanceNotFound.
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

// CreateInstance registers an instance, in the Stopped state.
func (f *Fake) CreateInstance(_ context.Context, spec incus.InstanceSpec) error {
	if err := f.record("create %s image=%s profiles=%v noprofiles=%v config=%v devices=%v",
		spec.Name, spec.Image, spec.Profiles, spec.NoProfiles,
		sortedPairs(spec.Config), sortedDeviceNames(spec.Devices)); err != nil {
		return err
	}
	if _, exists := f.Instances[spec.Name]; exists {
		return fmt.Errorf("instance %s already exists", spec.Name)
	}
	config := map[string]string{}
	for k, v := range spec.Config {
		config[k] = v
	}
	devices := map[string]incus.Device{}
	for name, dev := range spec.Devices {
		devices[name] = dev
	}
	profiles := spec.Profiles
	if spec.NoProfiles {
		profiles = []string{}
	}
	f.Instances[spec.Name] = &incus.Instance{
		Name:     spec.Name,
		Status:   "Stopped",
		Profiles: profiles,
		Config:   config,
		Devices:  devices,
	}
	return nil
}

// StartInstance moves an instance to Running.
func (f *Fake) StartInstance(_ context.Context, name string) error {
	if err := f.record("start %s", name); err != nil {
		return err
	}
	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	inst.Status = "Running"
	return nil
}

// StopInstance moves an instance to Stopped.
func (f *Fake) StopInstance(_ context.Context, name string) error {
	if err := f.record("stop %s", name); err != nil {
		return err
	}
	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	inst.Status = "Stopped"
	return nil
}

// DeleteInstance removes an instance.
func (f *Fake) DeleteInstance(_ context.Context, name string) error {
	if err := f.record("delete %s", name); err != nil {
		return err
	}
	if _, ok := f.Instances[name]; !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	delete(f.Instances, name)
	return nil
}

// ApplyConfig applies the given config keys.
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

// UnsetConfig removes the given config keys.
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

// ApplyDevices applies the given devices.
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
	// The real ApplyDevices does not remove keys absent from want, so neither
	// does the fake.
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

// RemoveDevices removes the given devices.
func (f *Fake) RemoveDevices(_ context.Context, name string, devices []string) error {
	if len(devices) == 0 {
		return nil
	}
	if err := f.record("removedevices %s %v", name, devices); err != nil {
		return err
	}
	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	for _, dev := range devices {
		delete(inst.Devices, dev)
	}
	return nil
}

// ProfileExists reports whether the name is in Profiles.
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

// VolumeExists reports whether the volume is registered.
func (f *Fake) VolumeExists(_ context.Context, pool, name string) (bool, error) {
	if err := f.record("volume exists %s %s", pool, name); err != nil {
		return false, err
	}
	return f.Volumes[pool+"/"+name], nil
}

// CreateVolume registers a volume.
func (f *Fake) CreateVolume(_ context.Context, pool, name string, config map[string]string) error {
	if err := f.record("volume create %s %s %v", pool, name, sortedPairs(config)); err != nil {
		return err
	}
	if f.Volumes == nil {
		f.Volumes = map[string]bool{}
	}
	f.Volumes[pool+"/"+name] = true
	return nil
}

// DeleteVolume removes a volume.
func (f *Fake) DeleteVolume(_ context.Context, pool, name string) error {
	if err := f.record("volume delete %s %s", pool, name); err != nil {
		return err
	}
	delete(f.Volumes, pool+"/"+name)
	return nil
}

// CreateSnapshot registers a snapshot.
func (f *Fake) CreateSnapshot(_ context.Context, instance, snapshot string) error {
	if err := f.record("snapshot create %s %s", instance, snapshot); err != nil {
		return err
	}
	f.SnapshotsByInstance[instance] = append(f.SnapshotsByInstance[instance],
		incus.Snapshot{Name: snapshot})
	return nil
}

// Snapshots returns the registered snapshots.
func (f *Fake) Snapshots(_ context.Context, instance string) ([]incus.Snapshot, error) {
	if err := f.record("snapshot list %s", instance); err != nil {
		return nil, err
	}
	return f.SnapshotsByInstance[instance], nil
}

// RestoreSnapshot records a restore.
func (f *Fake) RestoreSnapshot(_ context.Context, instance, snapshot string) error {
	return f.record("snapshot restore %s %s", instance, snapshot)
}

// DeleteSnapshot removes a snapshot.
func (f *Fake) DeleteSnapshot(_ context.Context, instance, snapshot string) error {
	if err := f.record("snapshot delete %s %s", instance, snapshot); err != nil {
		return err
	}
	existing := f.SnapshotsByInstance[instance]

	// A fresh slice: the one Snapshots handed out earlier must not be
	// rewritten underneath its caller, as the real API never does.
	kept := make([]incus.Snapshot, 0, len(existing))
	for _, s := range existing {
		if s.Name != snapshot {
			kept = append(kept, s)
		}
	}
	if len(kept) == len(existing) {
		return fmt.Errorf("snapshot %s of %s not found", snapshot, instance)
	}
	f.SnapshotsByInstance[instance] = kept

	return nil
}

// Exec records the execution and returns what ExecFunc says, if it is set.
func (f *Fake) Exec(_ context.Context, name string, argv []string, opt incus.ExecOptions) (int, error) {
	if err := f.record("exec %s %s", name, strings.Join(argv, " ")); err != nil {
		return 1, err
	}

	// The real thing fails against a stopped or absent instance.
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

// WaitReady returns an error when FailReady is set.
func (f *Fake) WaitReady(_ context.Context, name string, _ incus.WaitOptions) error {
	if err := f.record("waitready %s", name); err != nil {
		return err
	}
	if f.FailReady {
		return fmt.Errorf("instance %s did not become ready", name)
	}
	if f.NetworkNotReady {
		return fmt.Errorf("%w: instance %s", incus.ErrNetworkNotReady, name)
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
