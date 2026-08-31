package incus

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/websocket"
	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

var errAPI = errors.New("api failed")

// fakeOp は完了済みの操作を表す。
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
func (o *fakeOp) WaitContext(context.Context) error                                { return o.err }

// fakeRemoteOp は転送を伴う操作を表す。
type fakeRemoteOp struct{ err error }

func (o *fakeRemoteOp) AddHandler(func(api.Operation)) (*incusclient.EventTarget, error) {
	return nil, nil
}
func (o *fakeRemoteOp) CancelTarget() error                { return nil }
func (o *fakeRemoteOp) GetTarget() (*api.Operation, error) { return nil, nil }
func (o *fakeRemoteOp) Wait() error                        { return o.err }

// fakeServer はIncus APIのfake。
type fakeServer struct {
	instances map[string]*api.InstanceFull
	profiles  []string
	volumes   map[string][]api.StorageVolume
	snapshots map[string][]api.InstanceSnapshot

	// calls は呼び出しの記録。
	calls []string
	// err は操作名に対して返すエラー。
	err map[string]error
	// opErr は操作の完了時に返すエラー。
	opErr map[string]error
	// execMeta は ExecInstance の完了時メタデータ。
	execMeta map[string]any
	// lastExec は最後の exec 要求。
	lastExec api.InstanceExecPost
	// lastCreate は最後の作成要求。
	lastCreate api.InstancesPost
	// lastVolume は最後のボリューム作成要求。
	lastVolume api.StorageVolumesPost
	// lastState は最後の状態変更要求。
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

func (f *fakeServer) GetInstanceFull(name string) (*api.InstanceFull, string, error) {
	if err := f.record("GetInstanceFull"); err != nil {
		return nil, "", err
	}
	inst, ok := f.instances[name]
	if !ok {
		return nil, "", api.StatusErrorf(404, "Instance not found")
	}
	return inst, "etag", nil
}

func (f *fakeServer) CreateInstanceFromImage(_ incusclient.ImageServer, _ api.Image, req api.InstancesPost) (incusclient.RemoteOperation, error) {
	f.lastCreate = req
	if err := f.record("CreateInstanceFromImage"); err != nil {
		return nil, err
	}
	f.addInstance(req.Name, req.InstancePut)

	return &fakeRemoteOp{err: f.opErr["CreateInstanceFromImage"]}, nil
}

func (f *fakeServer) UpdateInstance(name string, put api.InstancePut, _ string) (incusclient.Operation, error) {
	if err := f.record("UpdateInstance"); err != nil {
		return nil, err
	}
	if inst, ok := f.instances[name]; ok {
		inst.Config = put.Config
		inst.Devices = put.Devices
		inst.Profiles = put.Profiles
	}
	return f.op("UpdateInstance"), nil
}

func (f *fakeServer) UpdateInstanceState(name string, state api.InstanceStatePut, _ string) (incusclient.Operation, error) {
	f.lastState = state
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
	if err := f.record("ExecInstance"); err != nil {
		return nil, err
	}
	if args != nil && args.DataDone != nil {
		close(args.DataDone)
	}
	return &fakeOp{err: f.opErr["ExecInstance"], metadata: f.execMeta}, nil
}

func (f *fakeServer) GetProfileNames() ([]string, error) {
	if err := f.record("GetProfileNames"); err != nil {
		return nil, err
	}
	return f.profiles, nil
}

func (f *fakeServer) GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error) {
	if err := f.record("GetStoragePoolVolumes"); err != nil {
		return nil, err
	}
	return f.volumes[pool], nil
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
	// 本物は "instance/snapshot" 形式で返すことがあるため、名前部分で比較する。
	kept := f.snapshots[name][:0]
	for _, s := range f.snapshots[name] {
		if snapshotName(s.Name) != snapshot {
			kept = append(kept, s)
		}
	}
	f.snapshots[name] = kept

	return f.op("DeleteInstanceSnapshot"), nil
}

// fakeImages はimage参照の解決をfakeする。
type fakeImages struct {
	err error
	ref string
}

func (f *fakeImages) Resolve(_ context.Context, ref string) (incusclient.ImageServer, *api.Image, error) {
	f.ref = ref
	if f.err != nil {
		return nil, nil, f.err
	}
	return nil, &api.Image{Fingerprint: "abc123"}, nil
}

func newAPI(f *fakeServer) (*API, *fakeImages) {
	images := &fakeImages{}
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
		t.Error("HasNIC() = false, expanded_devices を読めていない")
	}
	if !got.HasIPv4Address() {
		t.Error("HasIPv4Address() = false, state を読めていない")
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

	ok, err := a.InstanceExists(context.Background(), "dev-missing")
	if ok || err != nil {
		t.Errorf("InstanceExists() = %v, %v", ok, err)
	}
}

func TestAPICreateInstance(t *testing.T) {
	f := newFakeServer()
	a, images := newAPI(f)

	err := a.CreateInstance(context.Background(), InstanceSpec{
		Name:     "dev-x",
		Image:    "images:alpine/3.21",
		Type:     "container",
		Profiles: []string{"default"},
		Config:   map[string]string{"limits.cpu": "2"},
		Devices:  map[string]Device{"workspace": {"type": "disk"}},
	})
	if err != nil {
		t.Fatalf("CreateInstance() error = %v", err)
	}

	if images.ref != "images:alpine/3.21" {
		t.Errorf("image参照 = %q", images.ref)
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

// profiles: [] は「Profileを適用しない」を意味する
func TestAPICreateInstanceWithoutProfiles(t *testing.T) {
	f := newFakeServer()
	a, _ := newAPI(f)

	if err := a.CreateInstance(context.Background(), InstanceSpec{
		Name: "dev-x", Image: "img", NoProfiles: true,
	}); err != nil {
		t.Fatalf("CreateInstance() error = %v", err)
	}
	if got := f.lastCreate.Profiles; len(got) != 0 || got == nil {
		t.Errorf("Profiles = %v, 空リストを渡すこと", got)
	}
}

func TestAPIStartStop(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if err := a.StopInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("StopInstance() error = %v", err)
	}
	if f.lastState.Action != "stop" || !f.lastState.Force {
		t.Errorf("state = %+v, 停止は強制すること", f.lastState)
	}

	if err := a.StartInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("StartInstance() error = %v", err)
	}
	if f.lastState.Action != "start" || f.lastState.Force {
		t.Errorf("state = %+v", f.lastState)
	}
}

// 稼働中のinstanceは停止してから削除する
func TestAPIDeleteStopsRunningInstance(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if err := a.DeleteInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("DeleteInstance() error = %v", err)
	}
	if _, ok := f.instances["dev-x"]; ok {
		t.Error("削除されていない")
	}

	var stopped, deleted bool
	for _, c := range f.calls {
		switch c {
		case "UpdateInstanceState":
			stopped = true
		case "DeleteInstance":
			if !stopped {
				t.Error("停止する前に削除している")
			}
			deleted = true
		}
	}
	if !stopped || !deleted {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestAPIApplyConfig(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{Config: map[string]string{"keep": "yes"}})
	a, _ := newAPI(f)

	if err := a.ApplyConfig(context.Background(), "dev-x", map[string]string{"limits.cpu": "8"}); err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}

	got := f.instances["dev-x"].Config
	if got["limits.cpu"] != "8" || got["keep"] != "yes" {
		t.Errorf("config = %v, 宣言外のキーは残すこと", got)
	}

	// 空なら呼び出さない
	before := len(f.calls)
	if err := a.ApplyConfig(context.Background(), "dev-x", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != before {
		t.Errorf("calls = %v, 空のconfigでは呼び出さないこと", f.calls[before:])
	}
}

func TestAPIUnsetConfig(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{Config: map[string]string{"a": "1", "b": "2"}})
	a, _ := newAPI(f)

	if err := a.UnsetConfig(context.Background(), "dev-x", []string{"a"}); err != nil {
		t.Fatalf("UnsetConfig() error = %v", err)
	}
	got := f.instances["dev-x"].Config
	if _, ok := got["a"]; ok {
		t.Errorf("config = %v, 削除されていない", got)
	}
	if got["b"] != "2" {
		t.Errorf("config = %v, 指定外のキーを消している", got)
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
		"data":      {"type": "proxy", "listen": "tcp:1"}, // 型が変わる
		"new":       {"type": "nic"},
	})
	if err != nil {
		t.Fatalf("ApplyDevices() error = %v", err)
	}

	got := f.instances["dev-x"].Devices
	if got["workspace"]["path"] != "/workspace2" || got["workspace"]["shift"] != "true" {
		t.Errorf("workspace = %v, 宣言外のキーは残すこと", got["workspace"])
	}
	if got["data"]["type"] != "proxy" {
		t.Errorf("data = %v, 型変更時は作り直すこと", got["data"])
	}
	if _, ok := got["data"]["source"]; ok {
		t.Errorf("data = %v, 作り直したのに古いキーが残っている", got["data"])
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
	got := f.instances["dev-x"].Devices
	if _, ok := got["gone"]; ok {
		t.Error("削除されていない")
	}
	if _, ok := got["keep"]; !ok {
		t.Error("指定外のdeviceを消している")
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
		t.Error("存在しないProfileを存在すると判定している")
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
		t.Error("作成したボリュームが見つからない")
	}

	// custom以外は対象外
	f.volumes["default"] = append(f.volumes["default"], api.StorageVolume{Name: "img", Type: "image"})
	if ok, _ := a.VolumeExists(ctx, "default", "img"); ok {
		t.Error("custom以外のvolumeを存在扱いしないこと")
	}

	if err := a.DeleteVolume(ctx, "default", "vol"); err != nil {
		t.Fatalf("DeleteVolume() error = %v", err)
	}
	if ok, _ := a.VolumeExists(ctx, "default", "vol"); ok {
		t.Error("削除されていない")
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

	// APIは "instance/snapshot" 形式で返すことがある
	f.snapshots["dev-x"] = []api.InstanceSnapshot{{Name: "dev-x/s2"}}
	got, _ = a.Snapshots(ctx, "dev-x")
	if len(got) != 1 || got[0].Name != "s2" {
		t.Errorf("Snapshots() = %+v, instance名を取り除くこと", got)
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
		PublicEnv: map[string]string{"DEVKIT_INSTANCE": "dev-x"},
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
	want := map[string]string{"DEVKIT_INSTANCE": "dev-x", "TOKEN": "s3cret"}
	if diff := cmp.Diff(want, maps.Clone(req.Environment)); diff != "" {
		t.Errorf("Environment mismatch (-want +got):\n%s", diff)
	}
	if req.Interactive {
		t.Error("非対話実行で Interactive を立てないこと")
	}
}

// ユーザー名は解決できないため呼び出し側の責務とする
func TestAPIExecRejectsNonNumericUser(t *testing.T) {
	f := newFakeServer()
	a, _ := newAPI(f)

	_, err := a.Exec(context.Background(), "dev-x", []string{"true"}, ExecOptions{User: "developer"})
	if err == nil || !strings.Contains(err.Error(), "numeric uid") {
		t.Errorf("error = %v", err)
	}
}

// 端末を伴う実行はCLIへ委譲する
func TestAPIExecDelegatesTTY(t *testing.T) {
	terminal := &recordingClient{}
	a, _ := newAPI(newFakeServer())
	a.Terminal = terminal

	if _, err := a.Exec(context.Background(), "dev-x", []string{"/bin/sh"}, ExecOptions{TTY: true}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if !terminal.called {
		t.Error("端末を伴う実行がCLIへ委譲されていない")
	}

	a.Terminal = nil
	if _, err := a.Exec(context.Background(), "dev-x", []string{"/bin/sh"}, ExecOptions{TTY: true}); err == nil {
		t.Error("委譲先が無ければエラーになること")
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
		"volumes":    {"GetStoragePoolVolumes", func(a *API) error { _, err := a.VolumeExists(ctx, "p", "v"); return err }},
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

// 操作の完了待ちで失敗した場合も伝播すること
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
		{"メタデータ無し", nil, 0},
		{"想定外の型", map[string]any{"return": "x"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeOf(api.Operation{Metadata: tt.meta}); got != tt.want {
				t.Errorf("exitCodeOf() = %d, want %d", got, tt.want)
			}
		})
	}
}

// recordingClient は委譲先の呼び出しを記録する。
type recordingClient struct {
	Client
	called bool
}

func (r *recordingClient) Exec(context.Context, string, []string, ExecOptions) (int, error) {
	r.called = true
	return 0, nil
}

func TestAPIInstanceExists(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if ok, err := a.InstanceExists(context.Background(), "dev-x"); !ok || err != nil {
		t.Errorf("InstanceExists() = %v, %v", ok, err)
	}

	f.err["GetInstanceFull"] = errAPI
	ok, err := a.InstanceExists(context.Background(), "dev-x")
	if ok || !errors.Is(err, errAPI) {
		t.Errorf("InstanceExists() = %v, %v, 通信失敗を「存在しない」と混同しないこと", ok, err)
	}
}

// 変更が無ければIncusを呼ばない
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

// instanceが無い場合は、原因が分かるエラーにする
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

// 停止に失敗したら削除へ進まない
func TestAPIDeleteStopError(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.err["UpdateInstanceState"] = errAPI
	a, _ := newAPI(f)

	if err := a.DeleteInstance(context.Background(), "dev-x"); !errors.Is(err, errAPI) {
		t.Errorf("error = %v, want %v", err, errAPI)
	}
	if _, ok := f.instances["dev-x"]; !ok {
		t.Error("停止に失敗したのに削除している")
	}
}

// 操作は受理されても完了時に失敗しうる
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

// WaitReady は共通の待機処理へ委ねる
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
