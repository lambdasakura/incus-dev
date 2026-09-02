package incus

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

// server lists just the Incus API calls idev uses.
//
// incus.InstanceServer satisfies it as it is. Tests replace it with a fake.
type server interface {
	GetInstanceFull(name string) (*api.InstanceFull, string, error)
	GetInstances(instType api.InstanceType) ([]api.Instance, error)
	CreateInstanceFromImage(source incusclient.ImageServer, image api.Image, req api.InstancesPost) (incusclient.RemoteOperation, error)
	UpdateInstance(name string, put api.InstancePut, etag string) (incusclient.Operation, error)
	UpdateInstanceState(name string, state api.InstanceStatePut, etag string) (incusclient.Operation, error)
	DeleteInstance(name string) (incusclient.Operation, error)
	ExecInstance(name string, exec api.InstanceExecPost, args *incusclient.InstanceExecArgs) (incusclient.Operation, error)

	GetProfileNames() ([]string, error)

	GetStoragePoolVolume(pool, volType, name string) (*api.StorageVolume, string, error)
	CreateStoragePoolVolume(pool string, volume api.StorageVolumesPost) error
	DeleteStoragePoolVolume(pool, volType, name string) error

	GetInstanceSnapshots(name string) ([]api.InstanceSnapshot, error)
	CreateInstanceSnapshot(name string, snapshot api.InstanceSnapshotsPost) (incusclient.Operation, error)
	DeleteInstanceSnapshot(name, snapshot string) (incusclient.Operation, error)
}

// imageResolver resolves an image reference such as images:ubuntu/24.04.
type imageResolver interface {
	Resolve(ctx context.Context, ref string) (incusclient.ImageServer, *api.Image, error)
}

// API is the Client implementation that calls the Incus HTTP API.
type API struct {
	Server server
	Images imageResolver
	// Console is the host terminal used when running with one. nil means the
	// process's own standard streams.
	Console Console
	// Logger is where operations are recorded. nil records nothing.
	Logger *slog.Logger
}

// log records what was done, visible under --verbose.
//
// Values may be secrets, so only the operation and its target are printed.
// Never pass config or env values in.
func (a *API) log(op string, args ...any) {
	if a.Logger != nil {
		a.Logger.Debug("incus "+op, args...)
	}
}

// console returns the terminal to use when running with one.
func (a *API) console() Console {
	if a.Console != nil {
		return a.Console
	}
	return &osConsole{In: os.Stdin, Out: os.Stdout}
}

var _ Client = (*API)(nil)

// ListInstances lists the containers in the project with their config.
//
// idev needs it to find an instance of its own under a name it no longer
// derives: the naming rules have changed, and a project.scope: branch
// checkout upgraded across one of those changes would otherwise get a second,
// empty environment while the provisioned one kept running unreachable.
func (a *API) ListInstances(_ context.Context) ([]Instance, error) {
	list, err := a.Server.GetInstances(api.InstanceTypeContainer)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}

	out := make([]Instance, 0, len(list))
	for _, inst := range list {
		out = append(out, Instance{Name: inst.Name, Status: inst.Status, Config: inst.Config})
	}
	return out, nil
}

// missingScope reports whether a 404 is about the project or the storage pool
// the object was asked for in, rather than the object itself.
//
// Incus answers 404 for all four. Mapping every 404 to "it is not there" turns
// a typo in incus.project into "run 'idev up' first", and makes a pool that is
// merely unreachable look like a volume that has been deleted -- enough for up
// to drop it from the record that names it (spec 03-configuration.md 3.13).
// Only the message tells them apart.
func missingScope(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Project not found") || missingPool(err)
}

// missingPool reports whether a 404 is about the storage pool rather than the
// volume asked for on it.
func missingPool(err error) bool {
	return strings.Contains(err.Error(), "Storage pool not found")
}

// Instance fetches an instance's state, returning ErrInstanceNotFound when it
// does not exist.
func (a *API) Instance(_ context.Context, name string) (*Instance, error) {
	full, _, err := a.Server.GetInstanceFull(name)
	if err != nil {
		if api.StatusErrorCheck(err, 404) && !missingScope(err) {
			return nil, fmt.Errorf("%w: %s", ErrInstanceNotFound, name)
		}
		return nil, fmt.Errorf("get instance %s: %w", name, err)
	}
	return convertInstance(full), nil
}

// convertInstance turns the API's representation into idev's.
func convertInstance(full *api.InstanceFull) *Instance {
	inst := &Instance{
		Name:            full.Name,
		Status:          full.Status,
		LastUsedAt:      full.LastUsedAt,
		Profiles:        full.Profiles,
		Config:          full.Config,
		Devices:         convertDevices(full.Devices),
		ExpandedDevices: convertDevices(full.ExpandedDevices),
	}

	if full.State != nil {
		inst.State = &InstanceState{Network: map[string]NetworkState{}}
		for iface, net := range full.State.Network {
			addresses := make([]NetworkAddress, 0, len(net.Addresses))
			for _, addr := range net.Addresses {
				addresses = append(addresses, NetworkAddress{
					Family:  addr.Family,
					Address: addr.Address,
					Scope:   addr.Scope,
				})
			}
			inst.State.Network[iface] = NetworkState{Addresses: addresses}
		}
	}
	return inst
}

func convertDevices(devices map[string]map[string]string) map[string]Device {
	out := make(map[string]Device, len(devices))
	for name, dev := range devices {
		out[name] = Device(dev)
	}
	return out
}

func toAPIDevices(devices map[string]Device) map[string]map[string]string {
	out := make(map[string]map[string]string, len(devices))
	for name, dev := range devices {
		out[name] = dev
	}
	return out
}

// createError reports a failed creation, recognising the one failure a user
// can act on: another idev run got there first.
//
// Incus reports it from the database layer, and the raw text ("Add instance
// info to the database: This \"instances\" entry already exists") says nothing
// about what happened or what to do.
func createError(name string, err error) error {
	if strings.Contains(err.Error(), `This "instances" entry already exists`) {
		return fmt.Errorf("create instance %s: %w; another 'idev up' is creating it, "+
			"or it was created since this run started", name, ErrInstanceExists)
	}
	return fmt.Errorf("create instance %s: %w", name, err)
}

// CreateInstance creates an instance without starting it.
func (a *API) CreateInstance(ctx context.Context, spec InstanceSpec) error {
	source, image, err := a.Images.Resolve(ctx, spec.Image)
	if err != nil {
		return err
	}

	req := api.InstancesPost{
		Name: spec.Name,
		Type: api.InstanceTypeContainer,
		InstancePut: api.InstancePut{
			Config:   spec.Config,
			Devices:  toAPIDevices(spec.Devices),
			Profiles: spec.Profiles,
		},
	}
	if spec.NoProfiles {
		req.Profiles = []string{}
	}

	a.log("create instance", "name", spec.Name, "image", spec.Image)

	op, err := a.Server.CreateInstanceFromImage(source, *image, req)
	if err != nil {
		return createError(spec.Name, err)
	}
	// RemoteOperation takes no context, and fetching an image can take
	// minutes, so wait for it ourselves and stay interruptible.
	if err := waitOp(ctx, op); err != nil {
		return createError(spec.Name, err)
	}
	return nil
}

// waitOp waits for an operation involving a transfer, cancelling it if
// interrupted.
func waitOp(ctx context.Context, op incusclient.RemoteOperation) error {
	done := make(chan error, 1)
	go func() { done <- op.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = op.CancelTarget()
		return ctx.Err()
	}
}

// StartInstance starts an instance.
func (a *API) StartInstance(ctx context.Context, name string) error {
	return a.changeState(ctx, name, "start", false)
}

// StopInstance stops an instance.
//
// It tries a graceful stop first, so it does not casually kill whatever the
// user was running. In case nothing responds there is an upper bound on the
// wait, after which it forces the stop (spec 05-incus.md 5.4.5).
func (a *API) StopInstance(ctx context.Context, name string) error {
	inst, err := a.Instance(ctx, name)
	if err != nil {
		return err
	}
	if inst.IsStopped() {
		return nil
	}

	err = a.changeState(ctx, name, "stop", false)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		// Interrupted, not unresponsive. Forcing here would kill what the user
		// was running, which is what the graceful attempt exists to avoid.
		return err
	}
	return a.forceStop(ctx, name)
}

// stopTimeout is how many seconds a graceful stop is given.
const stopTimeout = 30

// forceStop kills an instance that is not responding.
func (a *API) forceStop(ctx context.Context, name string) error {
	return a.changeState(ctx, name, "stop", true)
}

// changeState starts or stops the instance.
//
// It refuses to run once the context is cancelled, and it is the only
// mutation that does. The rest go ahead: they are the second half of work
// that has already changed something, and stopping between the halves loses
// more than it saves -- destroy deletes the instance before its volumes, and
// the instance carries the only record naming them.
//
// DeleteInstance inherits the refusal indirectly, since it force-stops a
// running instance first. That is harmless: nothing has been deleted yet.
func (a *API) changeState(ctx context.Context, name, action string, force bool) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s instance %s: %w", action, name, err)
	}

	a.log(action+" instance", "name", name, "force", force)

	state := api.InstanceStatePut{Action: action, Force: force, Timeout: -1}
	if action == "stop" && !force {
		state.Timeout = stopTimeout
	}

	op, err := a.Server.UpdateInstanceState(name, state, "")
	if err != nil {
		return fmt.Errorf("%s instance %s: %w", action, name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("%s instance %s: %w", action, name, err)
	}
	return nil
}

// DeleteInstance deletes an instance, stopping it first when it is running.
func (a *API) DeleteInstance(ctx context.Context, name string) error {
	inst, err := a.Instance(ctx, name)
	if err != nil {
		return err
	}
	// It is about to be deleted, so there is no reason to wait for a graceful
	// stop. Intermediate states such as Frozen and Starting count too.
	if !inst.IsStopped() {
		if err := a.forceStop(ctx, name); err != nil {
			return err
		}
	}

	a.log("delete instance", "name", name)

	op, err := a.Server.DeleteInstance(name)
	if err != nil {
		return fmt.Errorf("delete instance %s: %w", name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("delete instance %s: %w", name, err)
	}
	return nil
}

// ApplyConfig sets the given config keys, leaving keys that were not declared
// alone (spec 05-incus.md 5.4.4).
func (a *API) ApplyConfig(ctx context.Context, name string, config map[string]string) error {
	if len(config) == 0 {
		return nil
	}
	a.log("set config", "name", name, "keys", sortedKeys(config))

	return a.updateInstance(ctx, name, func(put *api.InstancePut) {
		maps.Copy(put.Config, config)
	})
}

// UnsetConfig removes the given config keys.
func (a *API) UnsetConfig(ctx context.Context, name string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	a.log("unset config", "name", name, "keys", keys)

	return a.updateInstance(ctx, name, func(put *api.InstancePut) {
		for _, k := range keys {
			delete(put.Config, k)
		}
	})
}

// ApplyDevices sets the declared devices.
//
// A declared device is replaced rather than merged into: every device idev
// passes here is one it owns entirely — the workspace, a volume, or an
// instance.devices entry — so the declaration is the whole truth about it. A
// merge would leave a key that has left the declaration on the instance
// forever, and Incus rejects some of the combinations that produces, such as a
// disk naming both a pool and a host path (spec 05-incus.md 5.4.4).
func (a *API) ApplyDevices(ctx context.Context, name string, devices map[string]Device) error {
	if len(devices) == 0 {
		return nil
	}
	a.log("set devices", "name", name, "devices", sortedKeys(devices))

	return a.updateInstance(ctx, name, func(put *api.InstancePut) {
		if put.Devices == nil {
			put.Devices = map[string]map[string]string{}
		}
		for devName, want := range devices {
			put.Devices[devName] = maps.Clone(want)
		}
	})
}

// RemoveDevices removes the given devices.
func (a *API) RemoveDevices(ctx context.Context, name string, devices []string) error {
	if len(devices) == 0 {
		return nil
	}
	a.log("remove devices", "name", name, "devices", devices)

	return a.updateInstance(ctx, name, func(put *api.InstancePut) {
		for _, dev := range devices {
			delete(put.Devices, dev)
		}
	})
}

// updateInstance fetches the current state, applies a change and writes it
// back.
func (a *API) updateInstance(ctx context.Context, name string, change func(*api.InstancePut)) error {
	full, etag, err := a.Server.GetInstanceFull(name)
	if err != nil {
		if api.StatusErrorCheck(err, 404) && !missingScope(err) {
			return fmt.Errorf("%w: %s", ErrInstanceNotFound, name)
		}
		return fmt.Errorf("get instance %s: %w", name, err)
	}

	put := full.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	change(&put)

	op, err := a.Server.UpdateInstance(name, put, etag)
	if err != nil {
		return fmt.Errorf("update instance %s: %w", name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("update instance %s: %w", name, err)
	}
	return nil
}

// ProfileExists reports whether a profile exists. idev never creates one
// (REQ-007).
func (a *API) ProfileExists(_ context.Context, name string) (bool, error) {
	names, err := a.Server.GetProfileNames()
	if err != nil {
		return false, fmt.Errorf("list profiles: %w", err)
	}
	return slices.Contains(names, name), nil
}

// CheckImage reports whether an image reference resolves.
//
// rebuild destroys before it creates, so it has to know the image is there
// while the instance still is (spec 04-cli.md 4.6).
func (a *API) CheckImage(ctx context.Context, ref string) error {
	_, _, err := a.Images.Resolve(ctx, ref)
	return err
}

// VolumeExists reports whether a storage volume exists.
func (a *API) VolumeExists(_ context.Context, pool, name string) (bool, error) {
	_, _, err := a.Server.GetStoragePoolVolume(pool, storageVolumeType, name)
	switch {
	case err == nil:
		return true, nil
	case api.StatusErrorCheck(err, 404) && missingPool(err):
		// Distinguished from a missing volume so callers can decide: nothing
		// can exist on a pool with no row, but that is a reason to delete a
		// record and a reason to refuse to create.
		return false, fmt.Errorf("storage pool %s: %w", pool, ErrPoolNotFound)
	case api.StatusErrorCheck(err, 404) && !missingScope(err):
		return false, nil
	default:
		return false, fmt.Errorf("get storage volume %s on %s: %w", name, pool, err)
	}
}

// storageVolumeType is the kind of storage volume idev deals in.
const storageVolumeType = "custom"

// CreateVolume creates a storage volume.
func (a *API) CreateVolume(_ context.Context, pool, name string, config map[string]string) error {
	req := api.StorageVolumesPost{Name: name, Type: storageVolumeType}
	req.Config = config

	a.log("create volume", "pool", pool, "name", name)

	if err := a.Server.CreateStoragePoolVolume(pool, req); err != nil {
		return fmt.Errorf("create storage volume %s on %s: %w", name, pool, err)
	}
	return nil
}

// DeleteVolume deletes a storage volume.
func (a *API) DeleteVolume(_ context.Context, pool, name string) error {
	a.log("delete volume", "pool", pool, "name", name)

	if err := a.Server.DeleteStoragePoolVolume(pool, storageVolumeType, name); err != nil {
		return fmt.Errorf("delete storage volume %s on %s: %w", name, pool, err)
	}
	return nil
}

// snapshotError reports a failed snapshot, recognising the collision Incus
// reports from its database layer.
//
// 'idev snapshot create' with no name derives one from the clock to the
// second, so two in a row collide -- and the raw text says nothing about
// what happened.
func snapshotError(name string, err error) error {
	if strings.Contains(err.Error(), `This "instances_snapshots" entry already exists`) {
		return fmt.Errorf("create snapshot %s: %w", name, ErrSnapshotExists)
	}
	return fmt.Errorf("create snapshot %s: %w", name, err)
}

// CreateSnapshot takes a snapshot of an instance.
func (a *API) CreateSnapshot(ctx context.Context, instance, snapshot string) error {
	a.log("create snapshot", "instance", instance, "snapshot", snapshot)

	op, err := a.Server.CreateInstanceSnapshot(instance, api.InstanceSnapshotsPost{Name: snapshot})
	if err != nil {
		return snapshotError(snapshot, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return snapshotError(snapshot, err)
	}
	return nil
}

// Snapshots lists an instance's snapshots.
func (a *API) Snapshots(_ context.Context, instance string) ([]Snapshot, error) {
	snapshots, err := a.Server.GetInstanceSnapshots(instance)
	if err != nil {
		return nil, fmt.Errorf("list snapshots of %s: %w", instance, err)
	}

	out := make([]Snapshot, 0, len(snapshots))
	for _, s := range snapshots {
		out = append(out, Snapshot{Name: snapshotName(s.Name), CreatedAt: s.CreatedAt})
	}
	return out, nil
}

// snapshotName pulls the snapshot name out of the "instance/snapshot" form.
func snapshotName(name string) string {
	if _, snap, ok := strings.Cut(name, "/"); ok {
		return snap
	}
	return name
}

// RestoreSnapshot rolls an instance back to a snapshot.
//
// Sending the current configuration back would undo any change made between
// reading it and writing it. Incus ignores the other fields when Restore is
// set, so send nothing but the target.
func (a *API) RestoreSnapshot(ctx context.Context, instance, snapshot string) error {
	a.log("restore snapshot", "instance", instance, "snapshot", snapshot)

	op, err := a.Server.UpdateInstance(instance, api.InstancePut{Restore: snapshot}, "")
	if err != nil {
		return fmt.Errorf("restore snapshot %s: %w", snapshot, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("restore snapshot %s: %w", snapshot, err)
	}
	return nil
}

// DeleteSnapshot deletes a snapshot.
func (a *API) DeleteSnapshot(ctx context.Context, instance, snapshot string) error {
	a.log("delete snapshot", "instance", instance, "snapshot", snapshot)

	op, err := a.Server.DeleteInstanceSnapshot(instance, snapshot)
	if err != nil {
		return fmt.Errorf("delete snapshot %s: %w", snapshot, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("delete snapshot %s: %w", snapshot, err)
	}
	return nil
}

// Exec runs a command inside the container and returns its exit code.
func (a *API) Exec(ctx context.Context, name string, argv []string, opt ExecOptions) (int, error) {
	req, err := newExecRequest(argv, opt)
	if err != nil {
		return 0, err
	}

	dataDone := make(chan bool)
	args := &incusclient.InstanceExecArgs{
		Stdin:    opt.Stdin,
		Stdout:   opt.Stdout,
		Stderr:   opt.Stderr,
		DataDone: dataDone,
	}

	// Always open the control channel, so an interruption can reach the
	// process in the container.
	done := make(chan struct{})
	defer close(done)
	sent := make(chan struct{})
	ctrl := control{ctx: ctx, done: done, sent: sent}

	// On an interruption the process exits almost at once, which would leave
	// the signal unsent on the wire.
	//
	// started, not just ctx.Err(): waitExec retries Exec while the instance
	// boots, so ExecInstance legitimately fails in that window. With no
	// control socket there is no signal to wait for, and waiting anyway hangs
	// Ctrl-C for the whole grace period -- with SIGINT already trapped, so a
	// second one does not help.
	started := false
	defer func() {
		if started && ctx.Err() != nil {
			awaitSignalSent(sent)
		}
	}()

	if opt.TTY {
		// With a terminal allocated, put the host terminal into raw mode so
		// keystrokes reach the container untouched. Failing to restore it
		// leaves the user's shell broken.
		console := a.console()

		restore, err := console.MakeRaw()
		if err != nil {
			return 0, err
		}
		defer restore()

		req.Interactive = true
		// When it cannot be read, send nothing and let Incus use its default.
		if width, height, err := console.Size(); err == nil {
			req.Width, req.Height = width, height
		}

		resized, stop := console.Resized()
		defer stop()

		ctrl.console = console
		ctrl.resized = resized
	}
	args.Control = controlHandler(ctrl)

	// argv can hold a whole script, so name only the program being run. The
	// step's own error carries what failed.
	a.log("exec", "name", name, "program", program(argv))

	op, err := a.Server.ExecInstance(name, req, args)
	if err != nil {
		return 0, fmt.Errorf("exec in %s: %w", name, err)
	}
	// From here the control socket exists, so an interruption has somewhere
	// to go and is worth waiting for.
	started = true
	if err := op.WaitContext(ctx); err != nil {
		return 0, fmt.Errorf("exec in %s: %w", name, err)
	}

	// Wait for the output to finish streaming. A half-open websocket would
	// never close, so stay interruptible.
	select {
	case <-dataDone:
	case <-ctx.Done():
		return 0, fmt.Errorf("exec in %s: %w", name, ctx.Err())
	}

	return exitCodeOf(op.Get()), nil
}

// signalGrace bounds the wait for an interruption to reach the container.
//
// The control websocket is not always open — the exec can fail before it is
// established — so the wait cannot be unbounded.
const signalGrace = 2 * time.Second

// awaitSignalSent waits until the interruption has been forwarded, or until
// waiting is no longer worth it.
func awaitSignalSent(sent <-chan struct{}) {
	select {
	case <-sent:
	case <-time.After(signalGrace):
	}
}

// newExecRequest builds the exec request to send to Incus. It runs nothing.
func newExecRequest(argv []string, opt ExecOptions) (api.InstanceExecPost, error) {
	env := map[string]string{}
	if opt.TTY && opt.Term != "" {
		// Incus does not set TERM by default, which breaks terminal-oriented
		// programs, so carry the host's value across.
		env["TERM"] = opt.Term
	}
	maps.Copy(env, opt.PublicEnv)
	maps.Copy(env, opt.Env)

	req := api.InstanceExecPost{
		Command:     argv,
		Environment: env,
		WaitForWS:   true,
		Cwd:         opt.Cwd,
	}
	if opt.User != "" {
		uid, err := strconv.ParseUint(opt.User, 10, 32)
		if err != nil {
			// The Incus exec API only accepts a uid. Resolving a user name is
			// the caller's job, and is not silently ignored here.
			return req, fmt.Errorf("exec user must be a numeric uid, got %q", opt.User)
		}
		req.User = uint32(uid)
	}
	return req, nil
}

// exitCodeOf pulls the exit code out of a finished exec operation.
func exitCodeOf(op api.Operation) int {
	value, ok := op.Metadata["return"]
	if !ok {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

// WaitReady waits until the instance can be provisioned.
func (a *API) WaitReady(ctx context.Context, name string, opt WaitOptions) error {
	return waitReady(ctx, a, name, opt)
}

// program returns the name of the program being run.
func program(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}

// sortedKeys returns a map's keys in ascending order, so log output is stable.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
