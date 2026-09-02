package incus

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
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
	GetInstance(name string) (*api.Instance, string, error)
	GetInstances(instType api.InstanceType) ([]api.Instance, error)
	CreateInstanceFromImage(source incusclient.ImageServer, image api.Image, req api.InstancesPost) (incusclient.RemoteOperation, error)
	UpdateInstance(name string, put api.InstancePut, etag string) (incusclient.Operation, error)
	UpdateInstanceState(name string, state api.InstanceStatePut, etag string) (incusclient.Operation, error)
	DeleteInstance(name string) (incusclient.Operation, error)
	ExecInstance(name string, exec api.InstanceExecPost, args *incusclient.InstanceExecArgs) (incusclient.Operation, error)

	GetServer() (*api.Server, string, error)
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
	// Alias splits a reference into its remote and image name.
	Alias(ref string) (remote, name string, err error)
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
	full, etag, err := a.Server.GetInstanceFull(name)
	if err != nil {
		if api.StatusErrorCheck(err, 404) && !missingScope(err) {
			return nil, fmt.Errorf("%w: %s", ErrInstanceNotFound, name)
		}
		return nil, fmt.Errorf("get instance %s: %w", name, err)
	}
	// The etag belongs to this reading, and travels with it: what the caller
	// decides from these bytes is written back against them.
	inst := convertInstance(full)
	inst.ETag = etag
	return inst, nil
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
	_, alias, err := a.Images.Alias(spec.Image)
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
		// The alias, so the library prefers it over the fingerprint this
		// package resolved. A simplestreams remote cannot be queried by
		// fingerprint, so a daemon asked for one answers only from its local
		// cache -- which made every run fail once the upstream image was
		// rebuilt, and a first run on a clean host fail outright.
		//
		// Only the alias: the library fills in the mode, the protocol, the
		// remote's certificate, a secret for a private image, and the
		// same-server case where nothing is pulled at all.
		Source: api.InstanceSource{Alias: alias},
	}
	if spec.NoProfiles {
		req.Profiles = []string{}
	}

	a.log("create instance", "name", spec.Name, "image", spec.Image)

	op, err := a.Server.CreateInstanceFromImage(source, *image, req)
	if err != nil {
		return createError(spec.Name, err)
	}
	// The operation takes no context, and the daemon fetching an image can
	// take minutes, so wait for it here and cancel the transfer if the run is
	// interrupted.
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

// waitDelete waits for a delete, telling apart the outcomes its caller has to
// tell apart.
//
// The operation's own answer is definitive: an error means the daemon did not
// delete the instance, and nil means it did. Only abandoning the wait is
// ambiguous, because the daemon does not stop when idev does -- so only that
// is marked ErrOutcomeUnknown.
//
// op.WaitContext cannot make the distinction: it returns the operation's error
// before it consults the context, so a delete the daemon refused would be
// stamped unknown if the user interrupted in that window.
//
// A cancellation that arrives in the same instant as the operation's answer is
// still reported as unknown, which is why the caller tells the user to look
// before deleting anything: "unknown" has to be safe to act on.
func waitDelete(ctx context.Context, op incusclient.Operation) error {
	done := make(chan error, 1)
	go func() { done <- op.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ctx.Err(), ErrOutcomeUnknown)
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
	if err := waitDelete(ctx, op); err != nil {
		return fmt.Errorf("delete instance %s: %w", name, err)
	}
	return nil
}

// UpdateInstance applies a whole set of changes in one write.
//
// etag, when not empty, is from the reading the caller decided on: the write
// is refused with ErrChanged if the instance has changed since. That is what
// stops a second idev, whose snapshot was taken earlier and written later,
// from erasing a volume out of this one's record.
//
// One write for all of it, because it is one decision taken from one reading.
// Two writes would leave the second judged against an etag the first had
// spent, and a failure between them would leave the record describing devices
// the instance does not have -- which nothing would ever put right, because
// what idev removes is what the record says it has.
func (a *API) UpdateInstance(ctx context.Context, name string, change InstanceChange, etag string) error {
	if change.Empty() {
		return nil
	}
	if len(change.UnsetConfig) > 0 {
		a.log("unset config", "name", name, "keys", change.UnsetConfig)
	}
	if len(change.SetConfig) > 0 {
		a.log("set config", "name", name, "keys", sortedKeys(change.SetConfig))
	}
	if len(change.RemoveDevices) > 0 {
		a.log("remove devices", "name", name, "devices", change.RemoveDevices)
	}
	if len(change.SetDevices) > 0 {
		a.log("set devices", "name", name, "devices", sortedKeys(change.SetDevices))
	}

	return a.updateInstance(ctx, name, etag, func(put *api.InstancePut) {
		for _, key := range change.UnsetConfig {
			delete(put.Config, key)
		}
		maps.Copy(put.Config, change.SetConfig)

		if put.Devices == nil {
			put.Devices = map[string]map[string]string{}
		}
		for _, device := range change.RemoveDevices {
			delete(put.Devices, device)
		}
		for device, config := range change.SetDevices {
			put.Devices[device] = map[string]string(config)
		}
	})
}

// updateInstance fetches the current state, applies a change and writes it
// back.
//
// The plain instance rather than the full one: a write needs what it is about
// to send back, which is the writable part. The full reading adds the runtime
// state, every snapshot and every backup, none of which a write looks at and
// all of which travel over the socket.
func (a *API) updateInstance(ctx context.Context, name, etag string, change func(*api.InstancePut)) error {
	current, fresh, err := a.Server.GetInstance(name)
	if err != nil {
		if api.StatusErrorCheck(err, 404) && !missingScope(err) {
			return fmt.Errorf("%w: %s", ErrInstanceNotFound, name)
		}
		return fmt.Errorf("get instance %s: %w", name, err)
	}

	put := current.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	change(&put)

	// The caller's etag when it gave one, so the write is judged against the
	// reading the caller decided from rather than the one taken just now --
	// which is the whole window another idev writes into.
	if etag == "" {
		etag = fresh
	}

	op, err := a.Server.UpdateInstance(name, put, etag)
	if err != nil {
		if api.StatusErrorCheck(err, http.StatusPreconditionFailed) {
			return fmt.Errorf("update instance %s: %w", name, ErrChanged)
		}
		return fmt.Errorf("update instance %s: %w", name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("update instance %s: %w", name, err)
	}
	return nil
}

// SupportsIDMappedMounts reports whether the kernel can shift ids on a mount.
func (a *API) SupportsIDMappedMounts(_ context.Context) (bool, error) {
	server, _, err := a.Server.GetServer()
	if err != nil {
		return false, fmt.Errorf("read the server info: %w", err)
	}
	// Absent rather than "false" on a daemon that does not report the feature
	// at all; taking that for a no would refuse shift where it may work.
	value, reported := server.Environment.KernelFeatures["idmapped_mounts"]
	return !reported || value == "true", nil
}

// ProfileNames lists the profiles on the host. idev never creates one
// (REQ-007).
func (a *API) ProfileNames(_ context.Context) ([]string, error) {
	names, err := a.Server.GetProfileNames()
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	return names, nil
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
		// The same sentinel Instance uses. A caller that has to tell "not
		// there" from "would not run" should not have to read the text.
		if api.StatusErrorCheck(err, 404) && !missingScope(err) {
			return 0, fmt.Errorf("exec in %s: %w", name, ErrInstanceNotFound)
		}
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
	if opt.Group != "" {
		gid, err := strconv.ParseUint(opt.Group, 10, 32)
		if err != nil {
			return req, fmt.Errorf("exec group must be a numeric gid, got %q", opt.Group)
		}
		req.Group = uint32(gid)
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
