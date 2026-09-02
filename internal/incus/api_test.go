package incus

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/websocket"
	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

var errAPI = errors.New("api failed")

// fakeOp is an operation that has already finished.
type fakeOp struct {
	err      error
	metadata map[string]any
}

func (o *fakeOp) AddHandler(func(api.Operation)) (*incusclient.EventTarget, error) { return nil, nil }
func (o *fakeOp) Cancel() error                                                    { return nil }
func (o *fakeOp) Get() api.Operation                                               { return api.Operation{Metadata: o.metadata} }
func (o *fakeOp) GetWebsocket(string) (*websocket.Conn, error)                     { return nil, nil }
func (o *fakeOp) RemoveHandler(*incusclient.EventTarget) error                     { return nil }
func (o *fakeOp) Refresh() error                                                   { return nil }
func (o *fakeOp) Wait() error                                                      { return o.err }

// WaitContext returns without waiting once interrupted, as the real one does.
func (o *fakeOp) WaitContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return o.err
	}
}

// fakeRemoteOp is an operation that involves a transfer.
type fakeRemoteOp struct {
	err error
	// block, when non-nil, holds the operation open until it is closed.
	block chan struct{}
	// canceled reports whether a cancellation was requested.
	canceled chan struct{}
}

func (o *fakeRemoteOp) AddHandler(func(api.Operation)) (*incusclient.EventTarget, error) {
	return nil, nil
}
func (o *fakeRemoteOp) CancelTarget() error {
	if o.canceled != nil {
		close(o.canceled)
	}
	return nil
}
func (o *fakeRemoteOp) GetTarget() (*api.Operation, error) { return nil, nil }
func (o *fakeRemoteOp) Wait() error {
	if o.block != nil {
		<-o.block
	}
	return o.err
}

// fakeServer is a fake of the Incus API.
type fakeServer struct {
	instances map[string]*api.InstanceFull
	profiles  []string
	volumes   map[string][]api.StorageVolume
	snapshots map[string][]api.InstanceSnapshot

	// calls records the calls.
	calls []string
	// err maps an operation name to the error to return.
	err map[string]error
	// opErr maps an operation name to the error to return on completion.
	opErr map[string]error
	// execMeta is the metadata ExecInstance completes with.
	execMeta map[string]any
	// beforeExec runs just before ExecInstance.
	beforeExec func()
	// holdData, when non-nil, keeps the output streaming until it is closed.
	holdData chan struct{}
	// beforeInstance runs just before GetInstanceFull.
	beforeInstance func()
	// beforeState runs just before UpdateInstanceState.
	beforeState func()
	// lastExec is the most recent exec request.
	lastExec api.InstanceExecPost
	// lastExecArgs are the stream and control settings the last exec was given.
	lastExecArgs *incusclient.InstanceExecArgs
	// lastCreate is the most recent create request.
	lastCreate api.InstancesPost
	// lastImage and lastSource are the image and its source used to create.
	lastImage  api.Image
	lastSource incusclient.ImageServer
	// createOp is the operation CreateInstanceFromImage returns. nil means the
	// default.
	createOp *fakeRemoteOp
	// lastUpdate is the most recent update that was sent.
	lastUpdate api.InstancePut
	// lastVolume is the most recent volume creation request.
	lastVolume api.StorageVolumesPost
	// lastState is the most recent state change request.
	lastState api.InstanceStatePut
}

func newFakeServer() *fakeServer {
	return &fakeServer{
		instances: map[string]*api.InstanceFull{},
		profiles:  []string{"default"},
		volumes:   map[string][]api.StorageVolume{},
		snapshots: map[string][]api.InstanceSnapshot{},
		err:       map[string]error{},
		opErr:     map[string]error{},
	}
}

func (f *fakeServer) record(op string) error {
	f.calls = append(f.calls, op)
	return f.err[op]
}

func (f *fakeServer) op(name string) incusclient.Operation { return &fakeOp{err: f.opErr[name]} }

func (f *fakeServer) addInstance(name string, put api.InstancePut) *api.InstanceFull {
	full := &api.InstanceFull{}
	full.Name = name
	full.Status = "Running"
	full.Config = put.Config
	full.Devices = put.Devices
	full.Profiles = put.Profiles
	f.instances[name] = full

	return full
}

func (f *fakeServer) GetInstances(api.InstanceType) ([]api.Instance, error) {
	if err := f.record("GetInstances"); err != nil {
		return nil, err
	}
	out := make([]api.Instance, 0, len(f.instances))
	for name, full := range f.instances {
		inst := full.Instance
		inst.Name = name
		out = append(out, inst)
	}
	return out, nil
}

func (f *fakeServer) GetInstanceFull(name string) (*api.InstanceFull, string, error) {
	if f.beforeInstance != nil {
		f.beforeInstance()
	}
	if err := f.record("GetInstanceFull"); err != nil {
		return nil, "", err
	}
	inst, ok := f.instances[name]
	if !ok {
		return nil, "", api.StatusErrorf(404, "Instance not found")
	}
	// The real thing builds a response every time, so do not share the value
	// we hold.
	copied := *inst
	copied.Config = maps.Clone(inst.Config)
	copied.Devices = cloneDevices(inst.Devices)
	copied.Profiles = slices.Clone(inst.Profiles)

	return &copied, "etag", nil
}

func (f *fakeServer) CreateInstanceFromImage(source incusclient.ImageServer, image api.Image, req api.InstancesPost) (incusclient.RemoteOperation, error) {
	f.lastCreate = req
	f.lastImage = image
	f.lastSource = source
	if err := f.record("CreateInstanceFromImage"); err != nil {
		return nil, err
	}
	f.addInstance(req.Name, req.InstancePut)

	if f.createOp != nil {
		return f.createOp, nil
	}
	return &fakeRemoteOp{err: f.opErr["CreateInstanceFromImage"]}, nil
}

func (f *fakeServer) UpdateInstance(name string, put api.InstancePut, _ string) (incusclient.Operation, error) {
	f.lastUpdate = put
	if err := f.record("UpdateInstance"); err != nil {
		return nil, err
	}
	if inst, ok := f.instances[name]; ok {
		// The real thing replaces it with what was sent. Sharing the caller's
		// map would let an implementation that never sends an update pass.
		inst.Config = maps.Clone(put.Config)
		inst.Devices = cloneDevices(put.Devices)
		inst.Profiles = slices.Clone(put.Profiles)
	}
	return f.op("UpdateInstance"), nil
}

func cloneDevices(devices map[string]map[string]string) map[string]map[string]string {
	if devices == nil {
		return nil
	}
	out := make(map[string]map[string]string, len(devices))
	for name, dev := range devices {
		out[name] = maps.Clone(dev)
	}
	return out
}

func (f *fakeServer) UpdateInstanceState(name string, state api.InstanceStatePut, _ string) (incusclient.Operation, error) {
	f.lastState = state
	if f.beforeState != nil {
		f.beforeState()
	}
	if err := f.record("UpdateInstanceState"); err != nil {
		return nil, err
	}
	if inst, ok := f.instances[name]; ok {
		if state.Action == "start" {
			inst.Status = "Running"
		} else {
			inst.Status = "Stopped"
		}
	}
	return f.op("UpdateInstanceState"), nil
}

func (f *fakeServer) DeleteInstance(name string) (incusclient.Operation, error) {
	if err := f.record("DeleteInstance"); err != nil {
		return nil, err
	}
	delete(f.instances, name)

	return f.op("DeleteInstance"), nil
}

func (f *fakeServer) ExecInstance(_ string, exec api.InstanceExecPost, args *incusclient.InstanceExecArgs) (incusclient.Operation, error) {
	f.lastExec = exec
	f.lastExecArgs = args
	if f.beforeExec != nil {
		f.beforeExec()
	}
	if err := f.record("ExecInstance"); err != nil {
		return nil, err
	}
	if args != nil && args.DataDone != nil {
		// The real thing closes only once the output has finished streaming.
		done, hold := args.DataDone, f.holdData
		go func() {
			if hold != nil {
				<-hold
			}
			close(done)
		}()
	}
	return &fakeOp{err: f.opErr["ExecInstance"], metadata: f.execMeta}, nil
}

func (f *fakeServer) GetProfileNames() ([]string, error) {
	if err := f.record("GetProfileNames"); err != nil {
		return nil, err
	}
	return f.profiles, nil
}

func (f *fakeServer) GetStoragePoolVolume(pool, volType, name string) (*api.StorageVolume, string, error) {
	if err := f.record("GetStoragePoolVolume"); err != nil {
		return nil, "", err
	}
	for _, v := range f.volumes[pool] {
		if v.Name == name && v.Type == volType {
			return &v, "etag", nil
		}
	}
	return nil, "", api.StatusErrorf(404, "Storage volume not found")
}

func (f *fakeServer) CreateStoragePoolVolume(pool string, volume api.StorageVolumesPost) error {
	f.lastVolume = volume
	if err := f.record("CreateStoragePoolVolume"); err != nil {
		return err
	}
	f.volumes[pool] = append(f.volumes[pool], api.StorageVolume{
		Name: volume.Name,
		Type: volume.Type,
	})
	return nil
}

func (f *fakeServer) DeleteStoragePoolVolume(pool, _, name string) error {
	if err := f.record("DeleteStoragePoolVolume"); err != nil {
		return err
	}
	kept := f.volumes[pool][:0]
	for _, v := range f.volumes[pool] {
		if v.Name != name {
			kept = append(kept, v)
		}
	}
	f.volumes[pool] = kept

	return nil
}

func (f *fakeServer) GetInstanceSnapshots(name string) ([]api.InstanceSnapshot, error) {
	if err := f.record("GetInstanceSnapshots"); err != nil {
		return nil, err
	}
	return f.snapshots[name], nil
}

func (f *fakeServer) CreateInstanceSnapshot(name string, snapshot api.InstanceSnapshotsPost) (incusclient.Operation, error) {
	if err := f.record("CreateInstanceSnapshot"); err != nil {
		return nil, err
	}
	f.snapshots[name] = append(f.snapshots[name], api.InstanceSnapshot{
		InstanceSnapshotPut: api.InstanceSnapshotPut{},
		Name:                snapshot.Name,
	})
	return f.op("CreateInstanceSnapshot"), nil
}

func (f *fakeServer) DeleteInstanceSnapshot(name, snapshot string) (incusclient.Operation, error) {
	if err := f.record("DeleteInstanceSnapshot"); err != nil {
		return nil, err
	}
	// The real thing may return the "instance/snapshot" form, so compare on
	// the name alone.
	kept := f.snapshots[name][:0]
	for _, s := range f.snapshots[name] {
		if snapshotName(s.Name) != snapshot {
			kept = append(kept, s)
		}
	}
	f.snapshots[name] = kept

	return f.op("DeleteInstanceSnapshot"), nil
}

// fakeImages fakes resolving an image reference.
type fakeImages struct {
	err error
	// ref is what it was asked to resolve.
	ref string
	// server is the source it returns.
	server incusclient.ImageServer
}

func (f *fakeImages) Resolve(_ context.Context, ref string) (incusclient.ImageServer, *api.Image, error) {
	f.ref = ref
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.server, &api.Image{Fingerprint: "abc123"}, nil
}

func newAPI(f *fakeServer) (*API, *fakeImages) {
	images := &fakeImages{server: &fakeImageServer{}}
	return &API{Server: f, Images: images}, images
}

func TestAPIInstance(t *testing.T) {
	f := newFakeServer()
	full := f.addInstance("dev-x", api.InstancePut{
		Config:   map[string]string{"limits.cpu": "8"},
		Devices:  map[string]map[string]string{"workspace": {"type": "disk", "path": "/workspace"}},
		Profiles: []string{"default"},
	})
	full.ExpandedDevices = map[string]map[string]string{"eth0": {"type": "nic"}}
	full.State = &api.InstanceState{Network: map[string]api.InstanceStateNetwork{
		"eth0": {Addresses: []api.InstanceStateNetworkAddress{
			{Family: "inet", Address: "10.0.0.2", Scope: "global"},
		}},
		"lo": {Addresses: []api.InstanceStateNetworkAddress{
			{Family: "inet", Address: "127.0.0.1", Scope: "local"},
		}},
	}}

	a, _ := newAPI(f)
	got, err := a.Instance(context.Background(), "dev-x")
	if err != nil {
		t.Fatalf("Instance() error = %v", err)
	}

	if got.Name != "dev-x" || !got.IsRunning() {
		t.Errorf("Instance() = %+v", got)
	}
	if got.Config["limits.cpu"] != "8" {
		t.Errorf("Config = %v", got.Config)
	}
	if got.Devices["workspace"]["path"] != "/workspace" {
		t.Errorf("Devices = %v", got.Devices)
	}
	if !got.HasNIC() {
		t.Error("HasNIC() = false, expanded_devices was not read")
	}
	if !got.HasIPv4Address() {
		t.Error("HasIPv4Address() = false, state was not read")
	}
	if diff := cmp.Diff([]string{"default"}, got.Profiles); diff != "" {
		t.Errorf("Profiles mismatch (-want +got):\n%s", diff)
	}
}

func TestAPIInstanceNotFound(t *testing.T) {
	a, _ := newAPI(newFakeServer())

	if _, err := a.Instance(context.Background(), "dev-missing"); !errors.Is(err, ErrInstanceNotFound) {
		t.Errorf("error = %v, want ErrInstanceNotFound", err)
	}

}

func TestAPICreateInstance(t *testing.T) {
	f := newFakeServer()
	a, images := newAPI(f)

	err := a.CreateInstance(context.Background(), InstanceSpec{
		Name:     "dev-x",
		Image:    "images:alpine/3.21",
		Profiles: []string{"default"},
		Config:   map[string]string{"limits.cpu": "2"},
		Devices:  map[string]Device{"workspace": {"type": "disk"}},
	})
	if err != nil {
		t.Fatalf("CreateInstance() error = %v", err)
	}

	if images.ref != "images:alpine/3.21" {
		t.Errorf("image resolution = %q", images.ref)
	}
	// What was resolved goes to Incus as it is: a different fingerprint is a
	// different image.
	if f.lastImage.Fingerprint != "abc123" {
		t.Errorf("image = %+v, want the resolved image passed", f.lastImage)
	}
	if f.lastSource != images.server {
		t.Errorf("source = %v, want the resolved source passed", f.lastSource)
	}
	req := f.lastCreate
	if req.Name != "dev-x" || string(req.Type) != "container" {
		t.Errorf("request = %+v", req)
	}
	if req.Config["limits.cpu"] != "2" || req.Devices["workspace"]["type"] != "disk" {
		t.Errorf("request = %+v", req)
	}
	if diff := cmp.Diff([]string{"default"}, req.Profiles); diff != "" {
		t.Errorf("Profiles mismatch (-want +got):\n%s", diff)
	}
}

// profiles: [] means "apply no profile".
func TestAPICreateInstanceWithoutProfiles(t *testing.T) {
	f := newFakeServer()
	a, _ := newAPI(f)

	if err := a.CreateInstance(context.Background(), InstanceSpec{
		Name: "dev-x", Image: "img", NoProfiles: true,
	}); err != nil {
		t.Fatalf("CreateInstance() error = %v", err)
	}
	if got := f.lastCreate.Profiles; len(got) != 0 || got == nil {
		t.Errorf("Profiles = %v, want an empty list passed", got)
	}
}

func TestAPIStartStop(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if err := a.StopInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("StopInstance() error = %v", err)
	}
	if f.lastState.Action != "stop" || f.lastState.Force {
		t.Errorf("state = %+v, want a graceful stop so running work is not killed", f.lastState)
	}
	if f.lastState.Timeout <= 0 {
		t.Errorf("timeout = %d, want a bound so an unresponsive instance is not waited on forever", f.lastState.Timeout)
	}

	if err := a.StartInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("StartInstance() error = %v", err)
	}
	if f.lastState.Action != "start" || f.lastState.Force {
		t.Errorf("state = %+v", f.lastState)
	}
}

// An instance that will not stop gracefully is forced, so idev up --restart
// and destroy do not hang.
func TestAPIStopFallsBackToForce(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.opErr["UpdateInstanceState"] = errAPI
	a, _ := newAPI(f)

	// Let the second call, the forced stop, succeed.
	calls := 0
	f.beforeState = func() {
		calls++
		if calls >= 2 {
			delete(f.opErr, "UpdateInstanceState")
		}
	}

	if err := a.StopInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("StopInstance() error = %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want a forced stop after the graceful one", calls)
	}
	if !f.lastState.Force {
		t.Errorf("state = %+v, want the second attempt forced", f.lastState)
	}
}

// A stopped instance is not asked to stop.
// Interrupting a graceful stop must not escalate to killing the instance.
//
// The user pressing Ctrl-C during `idev up --restart` is asking for the
// restart to stop, not for whatever is running inside to be killed
// (spec 05-incus.md 5.4.5).
func TestAPIStopDoesNotForceAfterCancellation(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	ctx, cancel := context.WithCancel(context.Background())

	calls := 0
	f.beforeState = func() {
		calls++
		cancel()
	}
	f.opErr["UpdateInstanceState"] = context.Canceled

	err := a.StopInstance(ctx, "dev-x")

	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation reported", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want no forced stop after a cancelled graceful one", calls)
	}
}

// The image is resolved without creating anything, so rebuild can check it
// while the old instance is still there.
func TestAPICheckImage(t *testing.T) {
	f := newFakeServer()
	a, images := newAPI(f)

	if err := a.CheckImage(context.Background(), "images:alpine/3.21"); err != nil {
		t.Errorf("CheckImage() error = %v", err)
	}
	if images.ref != "images:alpine/3.21" {
		t.Errorf("resolved = %q, want the declared reference", images.ref)
	}
	if len(f.instances) != 0 {
		t.Error("CheckImage created something")
	}

	images.err = errAPI
	if err := a.CheckImage(context.Background(), "images:alpine/3.21"); !errors.Is(err, errAPI) {
		t.Errorf("error = %v, want %v", err, errAPI)
	}
}

func TestAPIStopAlreadyStopped(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{}).Status = "Stopped"
	a, _ := newAPI(f)

	if err := a.StopInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("StopInstance() error = %v", err)
	}
	for _, c := range f.calls {
		if c == "UpdateInstanceState" {
			t.Error("sent a stop request to an already-stopped instance")
		}
	}
}

// A running instance is stopped before it is deleted.
func TestAPIDeleteStopsRunningInstance(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if err := a.DeleteInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("DeleteInstance() error = %v", err)
	}
	if _, ok := f.instances["dev-x"]; ok {
		t.Error("it was not removed")
	}

	var stopped, deleted bool
	for _, c := range f.calls {
		switch c {
		case "UpdateInstanceState":
			stopped = true
		case "DeleteInstance":
			if !stopped {
				t.Error("deleted it before stopping it")
			}
			deleted = true
		}
	}
	if !stopped || !deleted {
		t.Errorf("calls = %v", f.calls)
	}
}

// It deletes from a non-stopped state other than Running too.
func TestAPIDeleteStopsNonRunningInstance(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{}).Status = "Frozen"
	a, _ := newAPI(f)

	if err := a.DeleteInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("DeleteInstance() error = %v", err)
	}
	if _, ok := f.instances["dev-x"]; ok {
		t.Error("it was not removed")
	}
	if !f.lastState.Force {
		t.Errorf("state = %+v, forcing the stop before a delete is fine", f.lastState)
	}
}

func TestAPIApplyConfig(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{Config: map[string]string{"keep": "yes"}})
	a, _ := newAPI(f)

	if err := a.ApplyConfig(context.Background(), "dev-x", map[string]string{"limits.cpu": "8"}); err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}

	got := f.lastUpdate.Config
	if got["limits.cpu"] != "8" || got["keep"] != "yes" {
		t.Errorf("config sent = %v, want undeclared keys kept", got)
	}

	// Nothing to apply means no call.
	before := len(f.calls)
	if err := a.ApplyConfig(context.Background(), "dev-x", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != before {
		t.Errorf("calls = %v, want no call for an empty config", f.calls[before:])
	}
}

func TestAPIUnsetConfig(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{Config: map[string]string{"a": "1", "b": "2"}})
	a, _ := newAPI(f)

	if err := a.UnsetConfig(context.Background(), "dev-x", []string{"a"}); err != nil {
		t.Fatalf("UnsetConfig() error = %v", err)
	}
	got := f.lastUpdate.Config
	if _, ok := got["a"]; ok {
		t.Errorf("config sent = %v, it was not removed", got)
	}
	if got["b"] != "2" {
		t.Errorf("config = %v, a key that was not named got removed", got)
	}
}

// A key dropped from a device's declaration is removed with it.
//
// Merging instead leaves it behind forever, and some combinations Incus then
// rejects: a disk naming both a pool and a host path is one, and no edit to
// dev.yml can undo it (spec 05-incus.md 5.4.4).
func TestAPIApplyDevicesDropsUndeclaredKeys(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{Devices: map[string]map[string]string{
		"data": {"type": "disk", "pool": "fast", "source": "myvol", "path": "/data"},
	}})
	a, _ := newAPI(f)

	// The same device, now a host-path mount: no pool any more.
	err := a.ApplyDevices(context.Background(), "dev-x", map[string]Device{
		"data": {"type": "disk", "source": "/srv/data", "path": "/data"},
	})
	if err != nil {
		t.Fatalf("ApplyDevices() error = %v", err)
	}

	got := f.lastUpdate.Devices["data"]
	if _, ok := got["pool"]; ok {
		t.Errorf("data = %v, want the pool gone with the declaration", got)
	}
	if got["source"] != "/srv/data" {
		t.Errorf("data = %v, want the new source", got)
	}
}

func TestAPIApplyDevices(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{Devices: map[string]map[string]string{
		"workspace": {"type": "disk", "path": "/workspace", "shift": "true"},
		"data":      {"type": "disk", "source": "/old"},
	}})
	a, _ := newAPI(f)

	err := a.ApplyDevices(context.Background(), "dev-x", map[string]Device{
		"workspace": {"type": "disk", "path": "/workspace2"},
		"data":      {"type": "proxy", "listen": "tcp:1"}, // the type changes
		"new":       {"type": "nic"},
	})
	if err != nil {
		t.Fatalf("ApplyDevices() error = %v", err)
	}

	got := f.lastUpdate.Devices
	if got["workspace"]["path"] != "/workspace2" {
		t.Errorf("workspace = %v, want the declared path", got["workspace"])
	}
	if _, ok := got["workspace"]["shift"]; ok {
		t.Errorf("workspace = %v, want a key that left the declaration gone", got["workspace"])
	}
	if got["data"]["type"] != "proxy" {
		t.Errorf("data = %v, want it recreated when the type changed", got["data"])
	}
	if _, ok := got["data"]["source"]; ok {
		t.Errorf("data = %v, an old key survived the recreation", got["data"])
	}
	if got["new"]["type"] != "nic" {
		t.Errorf("new = %v", got["new"])
	}
}

func TestAPIRemoveDevices(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{Devices: map[string]map[string]string{
		"gone": {"type": "disk"}, "keep": {"type": "disk"},
	}})
	a, _ := newAPI(f)

	if err := a.RemoveDevices(context.Background(), "dev-x", []string{"gone"}); err != nil {
		t.Fatalf("RemoveDevices() error = %v", err)
	}
	got := f.lastUpdate.Devices
	if _, ok := got["gone"]; ok {
		t.Error("it was not removed")
	}
	if _, ok := got["keep"]; !ok {
		t.Error("a device that was not named got removed")
	}
}

func TestAPIProfileExists(t *testing.T) {
	f := newFakeServer()
	f.profiles = []string{"default", "gpu"}
	a, _ := newAPI(f)

	if ok, err := a.ProfileExists(context.Background(), "gpu"); !ok || err != nil {
		t.Errorf("ProfileExists(gpu) = %v, %v", ok, err)
	}
	if ok, _ := a.ProfileExists(context.Background(), "missing"); ok {
		t.Error("reported a profile that does not exist as existing")
	}
}

func TestAPIVolumes(t *testing.T) {
	f := newFakeServer()
	a, _ := newAPI(f)
	ctx := context.Background()

	if ok, err := a.VolumeExists(ctx, "default", "vol"); ok || err != nil {
		t.Errorf("VolumeExists() = %v, %v", ok, err)
	}

	if err := a.CreateVolume(ctx, "default", "vol", map[string]string{"size": "10GiB"}); err != nil {
		t.Fatalf("CreateVolume() error = %v", err)
	}
	if f.lastVolume.Name != "vol" || f.lastVolume.Type != "custom" || f.lastVolume.Config["size"] != "10GiB" {
		t.Errorf("request = %+v", f.lastVolume)
	}
	if ok, _ := a.VolumeExists(ctx, "default", "vol"); !ok {
		t.Error("the volume that was created cannot be found")
	}

	// Anything but custom is out of scope.
	f.volumes["default"] = append(f.volumes["default"], api.StorageVolume{Name: "img", Type: "image"})
	if ok, _ := a.VolumeExists(ctx, "default", "img"); ok {
		t.Error("want a non-custom volume not treated as existing")
	}

	if err := a.DeleteVolume(ctx, "default", "vol"); err != nil {
		t.Fatalf("DeleteVolume() error = %v", err)
	}
	if ok, _ := a.VolumeExists(ctx, "default", "vol"); ok {
		t.Error("it was not removed")
	}
}

func TestAPISnapshots(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)
	ctx := context.Background()

	if err := a.CreateSnapshot(ctx, "dev-x", "s1"); err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}

	got, err := a.Snapshots(ctx, "dev-x")
	if err != nil {
		t.Fatalf("Snapshots() error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "s1" {
		t.Errorf("Snapshots() = %+v", got)
	}

	// The API may return the "instance/snapshot" form.
	f.snapshots["dev-x"] = []api.InstanceSnapshot{{Name: "dev-x/s2"}}
	got, _ = a.Snapshots(ctx, "dev-x")
	if len(got) != 1 || got[0].Name != "s2" {
		t.Errorf("Snapshots() = %+v, want the instance name stripped", got)
	}

	if err := a.RestoreSnapshot(ctx, "dev-x", "s1"); err != nil {
		t.Fatalf("RestoreSnapshot() error = %v", err)
	}
	if err := a.DeleteSnapshot(ctx, "dev-x", "s2"); err != nil {
		t.Fatalf("DeleteSnapshot() error = %v", err)
	}
	if len(f.snapshots["dev-x"]) != 0 {
		t.Errorf("snapshots = %v", f.snapshots["dev-x"])
	}
}

func TestAPIExec(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.execMeta = map[string]any{"return": float64(7)}
	a, _ := newAPI(f)

	code, err := a.Exec(context.Background(), "dev-x", []string{"sh", "-c", "exit 7"}, ExecOptions{
		Cwd:       "/workspace",
		User:      "1000",
		PublicEnv: map[string]string{"IDEV_INSTANCE": "dev-x"},
		Env:       map[string]string{"TOKEN": "s3cret"},
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}

	req := f.lastExec
	if diff := cmp.Diff([]string{"sh", "-c", "exit 7"}, req.Command); diff != "" {
		t.Errorf("Command mismatch (-want +got):\n%s", diff)
	}
	if req.Cwd != "/workspace" || req.User != 1000 {
		t.Errorf("request = %+v", req)
	}
	want := map[string]string{"IDEV_INSTANCE": "dev-x", "TOKEN": "s3cret"}
	if diff := cmp.Diff(want, maps.Clone(req.Environment)); diff != "" {
		t.Errorf("Environment mismatch (-want +got):\n%s", diff)
	}
	if req.Interactive {
		t.Error("want Interactive unset for a non-interactive run")
	}
	// Without WaitForWS, Incus sets up no websocket for the output.
	if !req.WaitForWS {
		t.Error("WaitForWS = false, the output cannot be received")
	}
}

// For a variable of the same name, what the project set wins
// (spec 06-provisioning.md 6.4).
func TestAPIExecEnvPrecedence(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if _, err := a.Exec(context.Background(), "dev-x", []string{"true"}, ExecOptions{
		PublicEnv: map[string]string{"IDEV_WORKSPACE": "/workspace"},
		Env:       map[string]string{"IDEV_WORKSPACE": "/elsewhere"},
	}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if got := f.lastExec.Environment["IDEV_WORKSPACE"]; got != "/elsewhere" {
		t.Errorf("IDEV_WORKSPACE = %q, want what the project set to win", got)
	}
}

// It waits for the output to finish streaming; otherwise the last of it is
// lost.
func TestAPIExecWaitsForOutput(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	// Exec must not return until the output has finished streaming.
	f.holdData = make(chan struct{})

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		if _, err := a.Exec(context.Background(), "dev-x", []string{"true"}, ExecOptions{}); err != nil {
			t.Errorf("Exec() error = %v", err)
		}
	}()

	select {
	case <-returned:
		t.Fatal("returned without waiting for the output")
	case <-time.After(20 * time.Millisecond):
	}

	close(f.holdData)
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Exec() never returned")
	}
}

// Once interrupted, it stops waiting.
func TestAPIOperationsHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ops := map[string]func(*API) error{
		"start":  func(a *API) error { return a.StartInstance(ctx, "dev-x") },
		"delete": func(a *API) error { return a.DeleteInstance(ctx, "dev-x") },
		"config": func(a *API) error { return a.ApplyConfig(ctx, "dev-x", map[string]string{"a": "1"}) },
		"snapshot": func(a *API) error {
			return a.CreateSnapshot(ctx, "dev-x", "s1")
		},
		"restore": func(a *API) error { return a.RestoreSnapshot(ctx, "dev-x", "s1") },
	}

	for name, fn := range ops {
		t.Run(name, func(t *testing.T) {
			f := newFakeServer()
			f.addInstance("dev-x", api.InstancePut{}).Status = "Stopped"
			a, _ := newAPI(f)

			if err := fn(a); !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v, want context.Canceled", err)
			}
		})
	}
}

// Starting is refused outright once the context is cancelled, rather than
// sent and then reported as cancelled.
//
// The other mutations deliberately go ahead: they are the second half of a
// cleanup, and stopping between the halves loses more than it saves.
func TestStartIsNotSentAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{}).Status = "Stopped"
	a, _ := newAPI(f)

	if err := a.StartInstance(ctx, "dev-x"); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %v, want the request never sent", f.calls)
	}
}

// An interruption while fetching the image cancels it.
func TestAPICreateInstanceCancellation(t *testing.T) {
	f := newFakeServer()
	f.createOp = &fakeRemoteOp{block: make(chan struct{}), canceled: make(chan struct{})}
	a, _ := newAPI(f)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	err := a.CreateInstance(ctx, InstanceSpec{Name: "dev-x", Image: "img"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	select {
	case <-f.createOp.canceled:
	case <-time.After(2 * time.Second):
		t.Error("the transfer was not cancelled")
	}
	close(f.createOp.block)
}

// User names cannot be resolved here, so that is the caller's job.
func TestAPIExecRejectsNonNumericUser(t *testing.T) {
	f := newFakeServer()
	a, _ := newAPI(f)

	_, err := a.Exec(context.Background(), "dev-x", []string{"true"}, ExecOptions{User: "developer"})
	if err == nil || !strings.Contains(err.Error(), "numeric uid") {
		t.Errorf("error = %v", err)
	}
}

func TestAPIPropagatesErrors(t *testing.T) {
	ctx := context.Background()

	ops := map[string]struct {
		call string
		fn   func(*API) error
	}{
		"instance":   {"GetInstanceFull", func(a *API) error { _, err := a.Instance(ctx, "dev-x"); return err }},
		"create":     {"CreateInstanceFromImage", func(a *API) error { return a.CreateInstance(ctx, InstanceSpec{Name: "dev-x"}) }},
		"start":      {"UpdateInstanceState", func(a *API) error { return a.StartInstance(ctx, "dev-x") }},
		"delete":     {"DeleteInstance", func(a *API) error { return a.DeleteInstance(ctx, "dev-x") }},
		"get":        {"GetInstanceFull", func(a *API) error { return a.ApplyConfig(ctx, "dev-x", map[string]string{"a": "1"}) }},
		"config":     {"UpdateInstance", func(a *API) error { return a.ApplyConfig(ctx, "dev-x", map[string]string{"a": "1"}) }},
		"devices":    {"UpdateInstance", func(a *API) error { return a.ApplyDevices(ctx, "dev-x", map[string]Device{"d": {"type": "disk"}}) }},
		"profiles":   {"GetProfileNames", func(a *API) error { _, err := a.ProfileExists(ctx, "p"); return err }},
		"volumes":    {"GetStoragePoolVolume", func(a *API) error { _, err := a.VolumeExists(ctx, "p", "v"); return err }},
		"volcreate":  {"CreateStoragePoolVolume", func(a *API) error { return a.CreateVolume(ctx, "p", "v", nil) }},
		"voldelete":  {"DeleteStoragePoolVolume", func(a *API) error { return a.DeleteVolume(ctx, "p", "v") }},
		"snapcreate": {"CreateInstanceSnapshot", func(a *API) error { return a.CreateSnapshot(ctx, "dev-x", "s") }},
		"snaplist":   {"GetInstanceSnapshots", func(a *API) error { _, err := a.Snapshots(ctx, "dev-x"); return err }},
		"snapdelete": {"DeleteInstanceSnapshot", func(a *API) error { return a.DeleteSnapshot(ctx, "dev-x", "s") }},
		"exec":       {"ExecInstance", func(a *API) error { _, err := a.Exec(ctx, "dev-x", []string{"true"}, ExecOptions{}); return err }},
	}

	for name, tt := range ops {
		t.Run(name, func(t *testing.T) {
			f := newFakeServer()
			f.addInstance("dev-x", api.InstancePut{})
			f.err[tt.call] = errAPI

			if err := tt.fn(&API{Server: f, Images: &fakeImages{}}); !errors.Is(err, errAPI) {
				t.Errorf("error = %v, want %v", err, errAPI)
			}
		})
	}
}

// A failure while waiting for an operation propagates too.
func TestAPIPropagatesOperationErrors(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.opErr["UpdateInstanceState"] = errAPI
	a, _ := newAPI(f)

	if err := a.StartInstance(context.Background(), "dev-x"); !errors.Is(err, errAPI) {
		t.Errorf("error = %v, want %v", err, errAPI)
	}
}

func TestAPICreateInstanceImageError(t *testing.T) {
	a, images := newAPI(newFakeServer())
	images.err = errAPI

	if err := a.CreateInstance(context.Background(), InstanceSpec{Name: "dev-x", Image: "bad"}); !errors.Is(err, errAPI) {
		t.Errorf("error = %v, want %v", err, errAPI)
	}
}

func TestExitCodeOf(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want int
	}{
		{"float64", map[string]any{"return": float64(3)}, 3},
		{"int", map[string]any{"return": 4}, 4},
		{"no metadata", nil, 0},
		{"an unexpected type", map[string]any{"return": "x"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeOf(api.Operation{Metadata: tt.meta}); got != tt.want {
				t.Errorf("exitCodeOf() = %d, want %d", got, tt.want)
			}
		})
	}
}

// Failing to reach Incus is not confused with "does not exist".
func TestAPIInstanceDistinguishesFailureFromAbsence(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.err["GetInstanceFull"] = errAPI
	a, _ := newAPI(f)

	_, err := a.Instance(context.Background(), "dev-x")
	if !errors.Is(err, errAPI) {
		t.Errorf("error = %v, want %v", err, errAPI)
	}
	if errors.Is(err, ErrInstanceNotFound) {
		t.Error("treated a communication failure as \"does not exist\"")
	}
}

// With nothing to change, Incus is not called.
func TestAPIUpdateSkipsEmptyChanges(t *testing.T) {
	tests := map[string]func(*API) error{
		"UnsetConfig":   func(a *API) error { return a.UnsetConfig(context.Background(), "dev-x", nil) },
		"ApplyDevices":  func(a *API) error { return a.ApplyDevices(context.Background(), "dev-x", nil) },
		"RemoveDevices": func(a *API) error { return a.RemoveDevices(context.Background(), "dev-x", nil) },
	}

	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFakeServer()
			a, _ := newAPI(f)

			if err := fn(a); err != nil {
				t.Fatalf("error = %v", err)
			}
			if len(f.calls) != 0 {
				t.Errorf("calls = %v", f.calls)
			}
		})
	}
}

// A missing instance produces an error that says why.
func TestAPIUpdateMissingInstance(t *testing.T) {
	a, _ := newAPI(newFakeServer())

	err := a.ApplyConfig(context.Background(), "dev-missing", map[string]string{"a": "1"})
	if !errors.Is(err, ErrInstanceNotFound) {
		t.Errorf("error = %v, want ErrInstanceNotFound", err)
	}
}

func TestAPIDeleteMissingInstance(t *testing.T) {
	a, _ := newAPI(newFakeServer())

	if err := a.DeleteInstance(context.Background(), "dev-missing"); !errors.Is(err, ErrInstanceNotFound) {
		t.Errorf("error = %v, want ErrInstanceNotFound", err)
	}
}

// A failed stop does not go on to delete.
func TestAPIDeleteStopError(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.err["UpdateInstanceState"] = errAPI
	a, _ := newAPI(f)

	if err := a.DeleteInstance(context.Background(), "dev-x"); !errors.Is(err, errAPI) {
		t.Errorf("error = %v, want %v", err, errAPI)
	}
	if _, ok := f.instances["dev-x"]; !ok {
		t.Error("deleted it despite the stop failing")
	}
}

// An accepted operation can still fail on completion.
func TestAPIPropagatesAsyncErrors(t *testing.T) {
	ops := map[string]struct {
		call string
		fn   func(*API) error
	}{
		"create":     {"CreateInstanceFromImage", func(a *API) error { return a.CreateInstance(context.Background(), InstanceSpec{Name: "dev-x"}) }},
		"delete":     {"DeleteInstance", func(a *API) error { return a.DeleteInstance(context.Background(), "dev-x") }},
		"update":     {"UpdateInstance", func(a *API) error { return a.ApplyConfig(context.Background(), "dev-x", map[string]string{"a": "1"}) }},
		"snapcreate": {"CreateInstanceSnapshot", func(a *API) error { return a.CreateSnapshot(context.Background(), "dev-x", "s") }},
		"snapdelete": {"DeleteInstanceSnapshot", func(a *API) error { return a.DeleteSnapshot(context.Background(), "dev-x", "s") }},
		"exec": {"ExecInstance", func(a *API) error {
			_, err := a.Exec(context.Background(), "dev-x", []string{"true"}, ExecOptions{})
			return err
		}},
	}

	for name, tt := range ops {
		t.Run(name, func(t *testing.T) {
			f := newFakeServer()
			inst := f.addInstance("dev-x", api.InstancePut{})
			inst.Status = "Stopped"
			f.opErr[tt.call] = errAPI
			a, _ := newAPI(f)

			if err := tt.fn(a); !errors.Is(err, errAPI) {
				t.Errorf("error = %v, want %v", err, errAPI)
			}
		})
	}
}

// WaitReady delegates to the shared waiting logic.
func TestAPIWaitReady(t *testing.T) {
	f := newFakeServer()
	full := f.addInstance("dev-x", api.InstancePut{})
	full.ExpandedDevices = map[string]map[string]string{"eth0": {"type": "nic"}}
	full.State = &api.InstanceState{Network: map[string]api.InstanceStateNetwork{
		"eth0": {Addresses: []api.InstanceStateNetworkAddress{
			{Family: "inet", Address: "10.0.0.2", Scope: "global"},
		}},
	}}
	f.execMeta = map[string]any{"return": float64(0)}
	a, _ := newAPI(f)

	if err := a.WaitReady(context.Background(), "dev-x", WaitOptions{Timeout: time.Second}); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
}

// --verbose makes the Incus operations visible, while values that may be
// secrets are never printed (spec 04-cli.md 4.10).
func TestAPILogsOperationsWithoutValues(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})

	var out bytes.Buffer
	a, _ := newAPI(f)
	a.Logger = slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx := context.Background()
	if err := a.ApplyConfig(ctx, "dev-x", map[string]string{"limits.cpu": "8"}); err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyDevices(ctx, "dev-x", map[string]Device{
		"secret-mount": {"type": "disk", "source": "/home/u/.ssh"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, "dev-x", []string{"/bin/sh", "-c", "deploy --token s3cret"}, ExecOptions{
		Env: map[string]string{"API_TOKEN": "s3cret"},
	}); err != nil {
		t.Fatal(err)
	}

	log := out.String()
	for _, want := range []string{"set config", "limits.cpu", "set devices", "secret-mount", "exec", "dev-x"} {
		if !strings.Contains(log, want) {
			t.Errorf("log = %q, want it to contain %q", log, want)
		}
	}
	// Values stay out.
	for _, leak := range []string{"s3cret", "/home/u/.ssh", "deploy"} {
		if strings.Contains(log, leak) {
			t.Errorf("log = %q, want it not to contain %q", log, leak)
		}
	}
}

// It works with no logger set.
func TestAPIWithoutLogger(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if err := a.ApplyConfig(context.Background(), "dev-x", map[string]string{"a": "1"}); err != nil {
		t.Errorf("ApplyConfig() error = %v", err)
	}
}

func TestAPIRestoreSnapshotErrors(t *testing.T) {
	tests := map[string]func(*fakeServer){
		"the request is refused": func(f *fakeServer) { f.err["UpdateInstance"] = errAPI },
		"the restore fails":      func(f *fakeServer) { f.opErr["UpdateInstance"] = errAPI },
	}

	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFakeServer()
			f.addInstance("dev-x", api.InstancePut{})
			setup(f)
			a, _ := newAPI(f)

			if err := a.RestoreSnapshot(context.Background(), "dev-x", "s1"); !errors.Is(err, errAPI) {
				t.Errorf("error = %v, want %v", err, errAPI)
			}
		})
	}
}

// A restore does not send the current configuration back, so a change made
// between reading and writing is not undone.
func TestAPIRestoreSnapshotSendsOnlyRestore(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{Config: map[string]string{"limits.cpu": "8"}})
	a, _ := newAPI(f)

	if err := a.RestoreSnapshot(context.Background(), "dev-x", "s1"); err != nil {
		t.Fatalf("RestoreSnapshot() error = %v", err)
	}
	if f.lastUpdate.Restore != "s1" {
		t.Errorf("Restore = %q, want s1", f.lastUpdate.Restore)
	}
	if len(f.lastUpdate.Config) != 0 {
		t.Errorf("config = %v, want nothing but the restore target sent", f.lastUpdate.Config)
	}
}

// Interrupted while waiting for the output to stream.
func TestAPIExecCancelledWhileWaitingForOutput(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.holdData = make(chan struct{})
	defer close(f.holdData)

	a, _ := newAPI(f)
	ctx, cancel := context.WithCancel(context.Background())
	f.beforeExec = cancel

	// Waiting for the operation succeeds; the interruption lands on the
	// streaming wait.
	f.opErr["ExecInstance"] = nil
	if _, err := a.Exec(ctx, "dev-x", []string{"true"}, ExecOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// Without being able to read the instance state, it does not delete.
func TestAPIDeleteKeepsInstanceWhenStopFails(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.err["UpdateInstanceState"] = errAPI
	a, _ := newAPI(f)

	if err := a.DeleteInstance(context.Background(), "dev-x"); !errors.Is(err, errAPI) {
		t.Errorf("error = %v, want %v", err, errAPI)
	}
	if _, ok := f.instances["dev-x"]; !ok {
		t.Error("deleted it despite the stop failing")
	}
}

func TestProgram(t *testing.T) {
	if got := program(nil); got != "" {
		t.Errorf("program(nil) = %q, want empty", got)
	}
	if got := program([]string{"/bin/sh", "-c", "x"}); got != "/bin/sh" {
		t.Errorf("program() = %q", got)
	}
}

// Incus answers 404 for a missing project or storage pool as well as for a
// missing instance or volume. Treating those alike turns a typo in
// incus.project into "run 'idev up' first", and makes a pool that is
// temporarily gone look like a volume that no longer exists.
func TestNotFoundDistinguishesTheScope(t *testing.T) {
	t.Run("a missing project is not a missing instance", func(t *testing.T) {
		f := newFakeServer()
		f.err = map[string]error{"GetInstanceFull": api.StatusErrorf(404, "Project not found")}
		a, _ := newAPI(f)

		_, err := a.Instance(context.Background(), "dev-x")
		if errors.Is(err, ErrInstanceNotFound) {
			t.Errorf("error = %v, want the missing project reported instead", err)
		}
		if err == nil || !strings.Contains(err.Error(), "Project not found") {
			t.Errorf("error = %v, want it to name the project", err)
		}
	})

	t.Run("a missing pool is not a missing volume", func(t *testing.T) {
		f := newFakeServer()
		f.err = map[string]error{
			"GetStoragePoolVolume": api.StatusErrorf(404, "Storage pool not found"),
		}
		a, _ := newAPI(f)

		exists, err := a.VolumeExists(context.Background(), "ghostpool", "v")
		if err == nil {
			t.Errorf("VolumeExists() = %v, nil; want the missing pool reported", exists)
		}
		if exists {
			t.Error("VolumeExists() = true, want false")
		}
	})

	t.Run("a missing instance still is one", func(t *testing.T) {
		f := newFakeServer()
		a, _ := newAPI(f)

		if _, err := a.Instance(context.Background(), "dev-x"); !errors.Is(err, ErrInstanceNotFound) {
			t.Errorf("error = %v, want ErrInstanceNotFound", err)
		}
	})

	t.Run("a missing volume still is one", func(t *testing.T) {
		f := newFakeServer()
		a, _ := newAPI(f)

		exists, err := a.VolumeExists(context.Background(), "default", "v")
		if err != nil || exists {
			t.Errorf("VolumeExists() = %v, %v; want false, nil", exists, err)
		}
	})
}

// Two idev runs can race to create the same instance. The loser gets an
// Incus database error, which says nothing a user can act on.
func TestCreateInstanceReportsALostRace(t *testing.T) {
	f := newFakeServer()
	f.err = map[string]error{
		"CreateInstanceFromImage": errors.New(
			"Failed instance creation: Failed creating instance record: " +
				`Add instance info to the database: This "instances" entry already exists`),
	}
	a, _ := newAPI(f)

	err := a.CreateInstance(context.Background(), InstanceSpec{Name: "dev-x", Image: "images:x"})
	if err == nil {
		t.Fatal("CreateInstance() = nil error, want the race reported")
	}
	if !errors.Is(err, ErrInstanceExists) {
		t.Errorf("error = %v, want ErrInstanceExists", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want it to say the instance is already there", err.Error())
	}
}

// 'idev snapshot create' with no name derives one from the clock to the
// second, so two in a row collide. The database error says nothing.
func TestCreateSnapshotReportsACollision(t *testing.T) {
	f := newFakeServer()
	f.err = map[string]error{
		"CreateInstanceSnapshot": errors.New(
			`Failed creating instance snapshot record "20260901-125602": ` +
				`Add snapshot info to the database: This "instances_snapshots" entry already exists`),
	}
	a, _ := newAPI(f)

	err := a.CreateSnapshot(context.Background(), "dev-x", "20260901-125602")
	if !errors.Is(err, ErrSnapshotExists) {
		t.Errorf("error = %v, want ErrSnapshotExists", err)
	}
}

// ListInstances is how idev finds an instance of its own under a name it no
// longer derives, so it has to carry the markers.
func TestListInstances(t *testing.T) {
	f := newFakeServer()
	inst := f.addInstance("dev-x", api.InstancePut{})
	inst.Config = map[string]string{"user.incus-dev.project": "x"}
	a, _ := newAPI(f)

	got, err := a.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances() error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "dev-x" {
		t.Fatalf("ListInstances() = %v, want the one instance", got)
	}
	if got[0].Config["user.incus-dev.project"] != "x" {
		t.Errorf("Config = %v, want the markers carried", got[0].Config)
	}

	f.err = map[string]error{"GetInstances": errors.New("boom")}
	if _, err := a.ListInstances(context.Background()); err == nil {
		t.Error("ListInstances() = nil error, want the failure reported")
	}
}

// A cancelled Exec whose ExecInstance never succeeded has no control socket,
// so there is no signal on the wire to wait for. Waiting anyway makes Ctrl-C
// during the boot-time retry loop hang for the whole grace period, with SIGINT
// already trapped so a second one does not help.
func TestExecDoesNotWaitWhenItNeverStarted(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.err = map[string]error{"ExecInstance": errors.New("not ready")}
	a, _ := newAPI(f)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if _, err := a.Exec(ctx, "dev-x", []string{"true"}, ExecOptions{}); err == nil {
		t.Fatal("Exec() = nil error, want the failure reported")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Exec() took %v, want it not to wait for a signal it never sent", elapsed)
	}
}
