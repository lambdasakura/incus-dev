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

// WaitContext は本物同様、中断されたら待たずに戻る。
func (o *fakeOp) WaitContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return o.err
	}
}

// fakeRemoteOp は転送を伴う操作を表す。
type fakeRemoteOp struct {
	err error
	// block が非nilの場合、閉じるまで完了しない。
	block chan struct{}
	// canceled は取り消しが要求されたか。
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
	// beforeExec は ExecInstance の直前に呼ばれる。
	beforeExec func()
	// holdData が非nilの場合、閉じるまで出力の中継が終わらない。
	holdData chan struct{}
	// beforeInstance は GetInstanceFull の直前に呼ばれる。
	beforeInstance func()
	// beforeState は UpdateInstanceState の直前に呼ばれる。
	beforeState func()
	// lastExec は最後の exec 要求。
	lastExec api.InstanceExecPost
	// lastExecArgs は最後の exec に渡された入出力・制御の指定。
	lastExecArgs *incusclient.InstanceExecArgs
	// lastCreate は最後の作成要求。
	lastCreate api.InstancesPost
	// lastImage / lastSource は作成に使ったimageと取得元。
	lastImage  api.Image
	lastSource incusclient.ImageServer
	// createOp は CreateInstanceFromImage が返す操作。nilなら既定。
	createOp *fakeRemoteOp
	// lastUpdate は最後に送った更新内容。
	lastUpdate api.InstancePut
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
	// 本物はレスポンスを毎回組み立てる。保持している値を共有しない。
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
		// 本物は送った内容で置き換える。呼び出し側が保持しているマップを
		// そのまま共有すると、更新を送っていない実装でもテストが通ってしまう。
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
		// 本物は出力の中継が終わってから閉じる。
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
	// ref / instanceType は解決を求められた内容。
	ref          string
	instanceType string
	// server は取得元として返すサーバ。
	server incusclient.ImageServer
}

func (f *fakeImages) Resolve(_ context.Context, ref, instanceType string) (incusclient.ImageServer, *api.Image, error) {
	f.ref = ref
	f.instanceType = instanceType
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

	if images.ref != "images:alpine/3.21" || images.instanceType != "container" {
		t.Errorf("image解決 = %q / %q", images.ref, images.instanceType)
	}
	// 解決結果をそのままIncusへ渡すこと（fingerprintが違えば別のimageになる）
	if f.lastImage.Fingerprint != "abc123" {
		t.Errorf("image = %+v, 解決したimageを渡すこと", f.lastImage)
	}
	if f.lastSource != images.server {
		t.Errorf("取得元 = %v, 解決した取得元を渡すこと", f.lastSource)
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
	if f.lastState.Action != "stop" || f.lastState.Force {
		t.Errorf("state = %+v, 作業中のプロセスを殺さないよう正常停止すること", f.lastState)
	}
	if f.lastState.Timeout <= 0 {
		t.Errorf("timeout = %d, 応答しないinstanceで待ち続けないこと", f.lastState.Timeout)
	}

	if err := a.StartInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("StartInstance() error = %v", err)
	}
	if f.lastState.Action != "start" || f.lastState.Force {
		t.Errorf("state = %+v", f.lastState)
	}
}

// 正常停止できないinstanceは強制停止する。
// idev up --restart / destroy が固まってしまうのを避ける。
func TestAPIStopFallsBackToForce(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.opErr["UpdateInstanceState"] = errAPI
	a, _ := newAPI(f)

	// 2回目（強制停止）は成功させる
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
		t.Errorf("呼び出し回数 = %d, 正常停止のあとに強制停止すること", calls)
	}
	if !f.lastState.Force {
		t.Errorf("state = %+v, 2回目は強制停止すること", f.lastState)
	}
}

// 停止中のinstanceを停止しようとしない
func TestAPIStopAlreadyStopped(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{}).Status = "Stopped"
	a, _ := newAPI(f)

	if err := a.StopInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("StopInstance() error = %v", err)
	}
	for _, c := range f.calls {
		if c == "UpdateInstanceState" {
			t.Error("停止中のinstanceへ停止要求を出している")
		}
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

// Running 以外の非停止状態でも削除できること
func TestAPIDeleteStopsNonRunningInstance(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{}).Status = "Frozen"
	a, _ := newAPI(f)

	if err := a.DeleteInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("DeleteInstance() error = %v", err)
	}
	if _, ok := f.instances["dev-x"]; ok {
		t.Error("削除されていない")
	}
	if !f.lastState.Force {
		t.Errorf("state = %+v, 削除前の停止は強制でよい", f.lastState)
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
		t.Errorf("送信したconfig = %v, 宣言外のキーは残すこと", got)
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
	got := f.lastUpdate.Config
	if _, ok := got["a"]; ok {
		t.Errorf("送信したconfig = %v, 削除されていない", got)
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

	got := f.lastUpdate.Devices
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
	got := f.lastUpdate.Devices
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
	// WaitForWS が無いとIncusは出力用のwebsocketを用意しない
	if !req.WaitForWS {
		t.Error("WaitForWS = false, 出力を受け取れない")
	}
}

// 同名の変数はプロジェクト指定が優先される（仕様 06-provisioning.md 6.4）
func TestAPIExecEnvPrecedence(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if _, err := a.Exec(context.Background(), "dev-x", []string{"true"}, ExecOptions{
		PublicEnv: map[string]string{"DEVKIT_WORKSPACE": "/workspace"},
		Env:       map[string]string{"DEVKIT_WORKSPACE": "/elsewhere"},
	}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if got := f.lastExec.Environment["DEVKIT_WORKSPACE"]; got != "/elsewhere" {
		t.Errorf("DEVKIT_WORKSPACE = %q, プロジェクト指定を優先すること", got)
	}
}

// 出力の中継が終わるまで待つ。待たないと最後の出力を取りこぼす。
func TestAPIExecWaitsForOutput(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	// 出力の中継が終わるまで Exec が返らないこと
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
		t.Fatal("出力の中継を待たずに返っている")
	case <-time.After(20 * time.Millisecond):
	}

	close(f.holdData)
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Exec() が返らない")
	}
}

// 中断されたら待ち続けない
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

// imageの取得中に中断されたら取り消す
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
		t.Error("転送を取り消していない")
	}
	close(f.createOp.block)
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

// 通信できないことを「存在しない」と混同しないこと
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
		t.Error("通信失敗を「存在しない」として扱っている")
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

// --verbose でIncusへの操作を追えること。
// ただしSecretを含みうる値は決して出さない（仕様 04-cli.md 4.10）。
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
			t.Errorf("ログ = %q, %q を含むこと", log, want)
		}
	}
	// 値は出さない
	for _, leak := range []string{"s3cret", "/home/u/.ssh", "deploy"} {
		if strings.Contains(log, leak) {
			t.Errorf("ログ = %q, %q を含めないこと", log, leak)
		}
	}
}

// ログ出力先を設定しなくても動くこと
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
		"要求が拒否される": func(f *fakeServer) { f.err["UpdateInstance"] = errAPI },
		"復元に失敗する":  func(f *fakeServer) { f.opErr["UpdateInstance"] = errAPI },
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

// 復元は現在の設定を送り返さない（取得と書き戻しの間の変更を巻き戻さないため）
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
		t.Errorf("config = %v, 復元先だけを送ること", f.lastUpdate.Config)
	}
}

// 出力の中継を待っている間に中断された場合
func TestAPIExecCancelledWhileWaitingForOutput(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	f.holdData = make(chan struct{})
	defer close(f.holdData)

	a, _ := newAPI(f)
	ctx, cancel := context.WithCancel(context.Background())
	f.beforeExec = cancel

	// 操作の完了待ちは通るが、中継の待ちで中断される
	f.opErr["ExecInstance"] = nil
	if _, err := a.Exec(ctx, "dev-x", []string{"true"}, ExecOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// instanceの状態を取得できなければ削除しない
func TestAPIDeleteKeepsInstanceWhenStopFails(t *testing.T) {
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

func TestProgram(t *testing.T) {
	if got := program(nil); got != "" {
		t.Errorf("program(nil) = %q, want empty", got)
	}
	if got := program([]string{"/bin/sh", "-c", "x"}); got != "/bin/sh" {
		t.Errorf("program() = %q", got)
	}
}
