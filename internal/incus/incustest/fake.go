// Package incustest provides an incus.Client implementation for tests.
package incustest

import (
	"context"
	"fmt"
	"io"
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
	// snapshotConfig is what each snapshot captured, keyed "instance/name",
	// so a restore can put it back.
	snapshotConfig map[string]map[string]string
	// Pools are the storage pools that exist. A pool with no row holds
	// nothing, which Incus reports differently from a missing volume.
	Pools []string
	// Images are the references that resolve. nil accepts any, which is what
	// most tests want; set it to refuse one.
	Images []string

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

	// versions is the fake's etag: one counter per instance, moved on by every
	// write, so a reading taken before someone else's write is refused.
	versions map[string]int

	// Calls records the calls in order, such as "create dev-x", "start dev-x".
	Calls []string
	// Users are the container's passwd entries, keyed by the name or uid a
	// lookup asks for: "developer" -> "developer:x:1001:1001::/home/dev:/bin/sh".
	Users map[string]string
	// Execs records the argv of each execution.
	Execs [][]string
}

var _ incus.Client = (*Fake)(nil)

// New returns a Fake that has the default profile.
func New() *Fake {
	return &Fake{
		Instances:           map[string]*incus.Instance{},
		SnapshotsByInstance: map[string][]incus.Snapshot{},
		Volumes:             map[string]bool{},
		Profiles:            []string{"default"},
		Pools:               []string{"default"},
		// The account the tests that set shell.user name. A container image
		// ships accounts; a fake with none would make every such test set one
		// up before it could say what it is about.
		Users: map[string]string{
			"developer": "developer:x:1001:1001::/home/developer:/bin/sh",
			"root":      "root:x:0:0::/root:/bin/sh",
		},
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
	out := detach(inst)
	out.ETag = f.etagOf(name)
	return out, nil
}

// detach copies an instance, because the real client builds a fresh value out
// of the API response and callers hold the result across other calls.
//
// Handing back the live one made every snapshot agree with the state it was
// taken from, so no test could express a stale one -- which is exactly what a
// second idev running at the same time produces.
func detach(inst *incus.Instance) *incus.Instance {
	out := *inst
	out.Config = maps.Clone(inst.Config)
	out.Profiles = slices.Clone(inst.Profiles)
	out.Devices = make(map[string]incus.Device, len(inst.Devices))
	for name, device := range inst.Devices {
		out.Devices[name] = maps.Clone(device)
	}
	return &out
}

// ListInstances lists the registered instances.
func (f *Fake) ListInstances(_ context.Context) ([]incus.Instance, error) {
	if err := f.record("instances"); err != nil {
		return nil, err
	}
	// What the real one builds: a name, a status and a config, and nothing
	// shared with the state behind it. GetInstances does return devices and
	// profiles; API.ListInstances drops them, so a caller reading a device
	// off a listing here would read a field that is empty in production --
	// and handing back the live maps would let a test change an instance
	// through a listing of it.
	out := make([]incus.Instance, 0, len(f.Instances))
	for _, name := range slices.Sorted(maps.Keys(f.Instances)) {
		inst := f.Instances[name]
		out = append(out, incus.Instance{
			Name:   inst.Name,
			Status: inst.Status,
			Config: maps.Clone(inst.Config),
		})
	}
	return out, nil
}

// CreateInstance registers an instance, in the Stopped state.
func (f *Fake) CreateInstance(_ context.Context, spec incus.InstanceSpec) error {
	if err := f.record("create %s image=%s profiles=%v noprofiles=%v config=%v devices=%v",
		spec.Name, spec.Image, spec.Profiles, spec.NoProfiles,
		sortedPairs(spec.Config), sortedDeviceNames(spec.Devices)); err != nil {
		return err
	}
	if _, exists := f.Instances[spec.Name]; exists {
		return fmt.Errorf("instance %s: %w", spec.Name, incus.ErrInstanceExists)
	}
	config := map[string]string{}
	for k, v := range spec.Config {
		config[k] = v
	}
	devices := map[string]incus.Device{}
	for name, dev := range spec.Devices {
		// Cloned, as UpdateInstance does and as the real client cannot avoid
		// doing: what the caller changes afterwards must not reach here.
		devices[name] = maps.Clone(dev)
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
	f.bumpETag(spec.Name)
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
	f.bumpETag(name)
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
	f.bumpETag(name)
	return nil
}

// DeleteInstance removes an instance.
func (f *Fake) DeleteInstance(ctx context.Context, name string) error {
	if err := f.record("delete %s", name); err != nil {
		return err
	}
	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	if err := ctx.Err(); err != nil {
		// The real client force-stops an instance that is running, and that
		// refuses outright once the context is done -- so a running instance
		// never reaches the delete and its outcome is never in doubt.
		if !inst.IsStopped() {
			return fmt.Errorf("stop instance %s: %w", name, err)
		}
		// Stopped: the request has reached the daemon by the time the wait
		// can be cut short, and the daemon does not stop because idev did.
		// Whether the instance survives is genuinely unknown, so this leaves
		// it and says so (see incus.ErrOutcomeUnknown).
		return fmt.Errorf("delete instance %s: %w: %w", name, err, incus.ErrOutcomeUnknown)
	}
	delete(f.Instances, name)
	return nil
}

// Touch records that something outside this Fake changed the instance, which
// is what another idev in another terminal amounts to. Readings taken before
// it are stale, and a write judged against one is refused.
func (f *Fake) Touch(name string) { f.bumpETag(name) }

// etagOf returns the current version of an instance, as an opaque string.
func (f *Fake) etagOf(name string) string {
	if f.versions == nil {
		return "v0"
	}
	return fmt.Sprintf("v%d", f.versions[name])
}

// bumpETag moves an instance on a version, invalidating readings taken before.
func (f *Fake) bumpETag(name string) {
	if f.versions == nil {
		f.versions = map[string]int{}
	}
	f.versions[name]++
}

// UpdateInstance applies a whole set of changes in one write.
//
// The version counter is the fake's etag: every write moves it on, so a caller
// writing against a reading taken before someone else's write is refused --
// which is what the real daemon does with an If-Match that no longer holds.
// Refused means nothing is applied, config and devices alike.
func (f *Fake) UpdateInstance(_ context.Context, name string, change incus.InstanceChange, etag string) error {
	if change.Empty() {
		return nil
	}
	// One call, several log lines: the calls a test asserts on are the
	// operations, and they did not stop being separate operations.
	if len(change.UnsetConfig) > 0 {
		if err := f.record("unset %s %v", name, change.UnsetConfig); err != nil {
			return err
		}
	}
	if len(change.SetConfig) > 0 {
		if err := f.record("config %s %v", name, sortedPairs(change.SetConfig)); err != nil {
			return err
		}
	}
	if len(change.RemoveDevices) > 0 {
		if err := f.record("removedevices %s %v", name, change.RemoveDevices); err != nil {
			return err
		}
	}
	if len(change.SetDevices) > 0 {
		if err := f.record("devices %s %v", name, sortedDeviceNames(change.SetDevices)); err != nil {
			return err
		}
	}

	inst, ok := f.Instances[name]
	if !ok {
		return fmt.Errorf("%w: %s", incus.ErrInstanceNotFound, name)
	}
	if etag != "" && etag != f.etagOf(name) {
		return fmt.Errorf("update instance %s: %w", name, incus.ErrChanged)
	}

	for _, key := range change.UnsetConfig {
		delete(inst.Config, key)
	}
	for key, value := range change.SetConfig {
		inst.Config[key] = value
	}
	if inst.Devices == nil {
		inst.Devices = map[string]incus.Device{}
	}
	for _, device := range change.RemoveDevices {
		delete(inst.Devices, device)
	}
	for deviceName, device := range change.SetDevices {
		// Cloned: the real client serialises the request, so what the caller
		// does to its own map afterwards cannot reach the instance.
		inst.Devices[deviceName] = maps.Clone(device)
	}
	f.bumpETag(name)
	return nil
}

// ProfileNames returns Profiles.
func (f *Fake) ProfileNames(_ context.Context) ([]string, error) {
	if err := f.record("profiles %v", f.Profiles); err != nil {
		return nil, err
	}
	// Cloned: the caller holds the result while it decides what is missing,
	// and the real client returns a fresh slice of its own.
	return slices.Clone(f.Profiles), nil
}

// CheckImage reports whether the image resolves.
func (f *Fake) CheckImage(_ context.Context, ref string) error {
	if err := f.record("image check %s", ref); err != nil {
		return err
	}
	if f.Images != nil && !slices.Contains(f.Images, ref) {
		return fmt.Errorf("image %s: not found", ref)
	}
	return nil
}

// hasPool reports whether the pool exists. A pool with no row holds nothing,
// which Incus answers differently from a volume that is merely absent.
func (f *Fake) hasPool(pool string) bool {
	return f.Pools == nil || slices.Contains(f.Pools, pool)
}

// VolumeExists reports whether the volume is registered.
func (f *Fake) VolumeExists(_ context.Context, pool, name string) (bool, error) {
	if err := f.record("volume exists %s %s", pool, name); err != nil {
		return false, err
	}
	if !f.hasPool(pool) {
		return false, fmt.Errorf("storage pool %s: %w", pool, incus.ErrPoolNotFound)
	}
	return f.Volumes[pool+"/"+name], nil
}

// CreateVolume registers a volume.
func (f *Fake) CreateVolume(_ context.Context, pool, name string, config map[string]string) error {
	if err := f.record("volume create %s %s %v", pool, name, sortedPairs(config)); err != nil {
		return err
	}
	if !f.hasPool(pool) {
		return fmt.Errorf("storage pool %s: %w", pool, incus.ErrPoolNotFound)
	}
	// Incus answers "Volume by that name already exists". Succeeding here
	// would hide a caller that creates without checking.
	if f.Volumes[pool+"/"+name] {
		return fmt.Errorf("storage volume %q already exists on pool %q", name, pool)
	}
	f.Volumes[pool+"/"+name] = true
	return nil
}

// DeleteVolume removes a volume.
func (f *Fake) DeleteVolume(_ context.Context, pool, name string) error {
	if err := f.record("volume delete %s %s", pool, name); err != nil {
		return err
	}
	ref := pool + "/" + name
	if !f.Volumes[ref] {
		// Incus answers 404. Succeeding here would hide a caller that deletes
		// from a record without checking the volume is still there.
		return fmt.Errorf("storage volume %q not found on pool %q", name, pool)
	}
	delete(f.Volumes, ref)
	return nil
}

// CreateSnapshot registers a snapshot.
func (f *Fake) CreateSnapshot(_ context.Context, instance, snapshot string) error {
	if err := f.record("snapshot create %s %s", instance, snapshot); err != nil {
		return err
	}
	// Incus refuses a name with a "/" or a space, whatever idev checks first.
	if strings.ContainsAny(snapshot, "/ \t\n") {
		return fmt.Errorf("invalid snapshot name %q", snapshot)
	}
	for _, s := range f.SnapshotsByInstance[instance] {
		if s.Name == snapshot {
			return fmt.Errorf("%s already has a snapshot named %q: %w",
				instance, snapshot, incus.ErrSnapshotExists)
		}
	}
	f.SnapshotsByInstance[instance] = append(f.SnapshotsByInstance[instance],
		incus.Snapshot{Name: snapshot})
	// What the instance held, so RestoreSnapshot can put it back the way the
	// daemon does. Without it the fake accepted a restore and changed
	// nothing, which no test noticed for eighteen rounds.
	if inst, ok := f.Instances[instance]; ok {
		if f.snapshotConfig == nil {
			f.snapshotConfig = map[string]map[string]string{}
		}
		f.snapshotConfig[instance+"/"+snapshot] = maps.Clone(inst.Config)
	}
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
	if err := f.record("snapshot restore %s %s", instance, snapshot); err != nil {
		return err
	}
	if !slices.ContainsFunc(f.SnapshotsByInstance[instance], func(s incus.Snapshot) bool {
		return s.Name == snapshot
	}) {
		return fmt.Errorf("%s has no snapshot named %q", instance, snapshot)
	}
	// Only a snapshot this fake took captured the config; one a test
	// registered directly restores to nothing, which is what it asked for.
	if config, ok := f.snapshotConfig[instance+"/"+snapshot]; ok {
		if inst, ok := f.Instances[instance]; ok {
			inst.Config = maps.Clone(config)
		}
	}
	f.bumpETag(instance)
	return nil
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
	// idev asks the container who a user is before running as them, so a fake
	// container has to be able to answer. Nothing is invented: a name the test
	// did not put in Users is one the container does not have, which is what
	// getent says by exiting non-zero.
	if len(argv) == 3 && argv[0] == "getent" && argv[1] == "passwd" {
		entry, ok := f.Users[argv[2]]
		if !ok {
			return 2, nil
		}
		if opt.Stdout != nil {
			_, _ = io.WriteString(opt.Stdout, entry+"\n")
		}
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
