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

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

// server は devkit が使用するIncus APIだけを並べた窓口。
//
// incus.InstanceServer がそのまま満たす。テストではfakeへ差し替える。
type server interface {
	GetInstanceFull(name string) (*api.InstanceFull, string, error)
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

// imageResolver は image 参照（例 images:ubuntu/24.04）を解決する。
//
// aliasはinstance種別ごとに別のimageを指すため、種別も渡す。
type imageResolver interface {
	Resolve(ctx context.Context, ref, instanceType string) (incusclient.ImageServer, *api.Image, error)
}

// API はIncusのHTTP APIを呼ぶ Client 実装。
type API struct {
	Server server
	Images imageResolver
	// Console は端末を伴う実行で操作するホスト側の端末。
	// nilならプロセスの標準入出力を使う。
	Console Console
	// Logger は操作の記録先。nilなら記録しない。
	Logger *slog.Logger
}

// log は行った操作を記録する（--verbose で見える）。
//
// 値はSecretを含みうるため、操作名と対象だけを出す。
// configやenvの値は決して渡さない。
func (a *API) log(op string, args ...any) {
	if a.Logger != nil {
		a.Logger.Debug("incus "+op, args...)
	}
}

// console は端末を伴う実行で使う端末を返す。
func (a *API) console() Console {
	if a.Console != nil {
		return a.Console
	}
	return &osConsole{In: os.Stdin, Out: os.Stdout}
}

var _ Client = (*API)(nil)

// Instance はinstanceの状態を取得する。存在しない場合は ErrInstanceNotFound を返す。
func (a *API) Instance(_ context.Context, name string) (*Instance, error) {
	full, _, err := a.Server.GetInstanceFull(name)
	if err != nil {
		if api.StatusErrorCheck(err, 404) {
			return nil, fmt.Errorf("%w: %s", ErrInstanceNotFound, name)
		}
		return nil, fmt.Errorf("get instance %s: %w", name, err)
	}
	return convertInstance(full), nil
}

// convertInstance はAPIの表現をdevkitの表現へ変換する。
func convertInstance(full *api.InstanceFull) *Instance {
	inst := &Instance{
		Name:            full.Name,
		Status:          full.Status,
		Type:            full.Type,
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

// CreateInstance はinstanceを作成する（起動はしない）。
func (a *API) CreateInstance(ctx context.Context, spec InstanceSpec) error {
	source, image, err := a.Images.Resolve(ctx, spec.Image, spec.Type)
	if err != nil {
		return err
	}

	req := api.InstancesPost{
		Name: spec.Name,
		Type: api.InstanceType(spec.Type),
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
		return fmt.Errorf("create instance %s: %w", spec.Name, err)
	}
	// RemoteOperation は context を受け取らない。imageの取得は数分かかる
	// ことがあるため、中断できるよう自前で待つ。
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("create instance %s: %w", spec.Name, err)
	}
	return nil
}

// waitOp は転送を伴う操作の完了を待つ。中断された場合は取り消す。
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

// StartInstance はinstanceを起動する。
func (a *API) StartInstance(ctx context.Context, name string) error {
	return a.changeState(ctx, name, "start", false)
}

// StopInstance はinstanceを停止する。
//
// 利用者の作業中プロセスを不用意に殺さないよう、まず正常停止を試みる。
// 応答しない場合に備えて待ち時間には上限を設け、超えたら強制停止する
// （仕様 05-incus.md 5.4.5）。
func (a *API) StopInstance(ctx context.Context, name string) error {
	inst, err := a.Instance(ctx, name)
	if err != nil {
		return err
	}
	if inst.IsStopped() {
		return nil
	}

	if err := a.changeState(ctx, name, "stop", false); err == nil {
		return nil
	}
	return a.forceStop(ctx, name)
}

// stopTimeout は正常停止を待つ上限（秒）。
const stopTimeout = 30

// forceStop は応答しないinstanceを強制停止する。
func (a *API) forceStop(ctx context.Context, name string) error {
	return a.changeState(ctx, name, "stop", true)
}

func (a *API) changeState(ctx context.Context, name, action string, force bool) error {
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

// DeleteInstance はinstanceを削除する。稼働中であれば停止してから削除する。
func (a *API) DeleteInstance(ctx context.Context, name string) error {
	inst, err := a.Instance(ctx, name)
	if err != nil {
		return err
	}
	// これから消すinstanceなので、正常停止を待たずに落としてよい。
	// Frozen や Starting のような中間状態も対象にする。
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

// ApplyConfig は指定されたconfigキーを設定する。
// 宣言されていないキーには触れない（仕様 05-incus.md 5.4.4）。
func (a *API) ApplyConfig(ctx context.Context, name string, config map[string]string) error {
	if len(config) == 0 {
		return nil
	}
	a.log("set config", "name", name, "keys", sortedKeys(config))

	return a.updateInstance(ctx, name, func(put *api.InstancePut) {
		maps.Copy(put.Config, config)
	})
}

// UnsetConfig は指定されたconfigキーを削除する。
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

// ApplyDevices は宣言されたdeviceを設定する。
// 既存deviceは差分のみ更新し、型が変わった場合は作り直す。
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
			current, exists := put.Devices[devName]
			if !exists || current == nil || Device(current).Type() != want.Type() {
				put.Devices[devName] = maps.Clone(want)
				continue
			}
			maps.Copy(current, want)
		}
	})
}

// RemoveDevices は指定されたdeviceを削除する。
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

// updateInstance は現在の状態を取得し、変更を加えて書き戻す。
func (a *API) updateInstance(ctx context.Context, name string, change func(*api.InstancePut)) error {
	full, etag, err := a.Server.GetInstanceFull(name)
	if err != nil {
		if api.StatusErrorCheck(err, 404) {
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

// ProfileExists はProfileの存在を返す。devkitはProfileを作成しない（REQ-007）。
func (a *API) ProfileExists(_ context.Context, name string) (bool, error) {
	names, err := a.Server.GetProfileNames()
	if err != nil {
		return false, fmt.Errorf("list profiles: %w", err)
	}
	return slices.Contains(names, name), nil
}

// VolumeExists はstorage volumeの存在を返す。
func (a *API) VolumeExists(_ context.Context, pool, name string) (bool, error) {
	_, _, err := a.Server.GetStoragePoolVolume(pool, storageVolumeType, name)
	switch {
	case err == nil:
		return true, nil
	case api.StatusErrorCheck(err, 404):
		return false, nil
	default:
		return false, fmt.Errorf("get storage volume %s on %s: %w", name, pool, err)
	}
}

// storageVolumeType はdevkitが扱うstorage volumeの種別。
const storageVolumeType = "custom"

// CreateVolume はstorage volumeを作成する。
func (a *API) CreateVolume(_ context.Context, pool, name string, config map[string]string) error {
	req := api.StorageVolumesPost{Name: name, Type: storageVolumeType}
	req.Config = config

	a.log("create volume", "pool", pool, "name", name)

	if err := a.Server.CreateStoragePoolVolume(pool, req); err != nil {
		return fmt.Errorf("create storage volume %s on %s: %w", name, pool, err)
	}
	return nil
}

// DeleteVolume はstorage volumeを削除する。
func (a *API) DeleteVolume(_ context.Context, pool, name string) error {
	a.log("delete volume", "pool", pool, "name", name)

	if err := a.Server.DeleteStoragePoolVolume(pool, storageVolumeType, name); err != nil {
		return fmt.Errorf("delete storage volume %s on %s: %w", name, pool, err)
	}
	return nil
}

// CreateSnapshot はinstanceのスナップショットを作成する。
func (a *API) CreateSnapshot(ctx context.Context, instance, snapshot string) error {
	a.log("create snapshot", "instance", instance, "snapshot", snapshot)

	op, err := a.Server.CreateInstanceSnapshot(instance, api.InstanceSnapshotsPost{Name: snapshot})
	if err != nil {
		return fmt.Errorf("create snapshot %s: %w", snapshot, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return fmt.Errorf("create snapshot %s: %w", snapshot, err)
	}
	return nil
}

// Snapshots はinstanceのスナップショット一覧を返す。
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

// snapshotName は "instance/snapshot" 形式からスナップショット名だけを取り出す。
func snapshotName(name string) string {
	if _, snap, ok := strings.Cut(name, "/"); ok {
		return snap
	}
	return name
}

// RestoreSnapshot はinstanceをスナップショットの状態へ戻す。
//
// 現在の設定を送り返すと、取得から書き戻しまでの間に加えられた変更を
// 巻き戻してしまう。Incusは Restore 指定時に他のフィールドを見ないため、
// 復元先だけを送る。
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

// DeleteSnapshot はスナップショットを削除する。
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

// Exec はコンテナ内でコマンドを実行し、終了コードを返す。
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

	// 中断されたときにコンテナ内のプロセスへ伝えるため、制御経路は常に開く。
	done := make(chan struct{})
	defer close(done)
	ctrl := control{ctx: ctx, done: done}

	if opt.TTY {
		// 端末を割り当てる場合、キー入力をそのままコンテナへ渡すため
		// ホスト側の端末をraw modeにする。復元しないとシェルが壊れる。
		console := a.console()

		restore, err := console.MakeRaw()
		if err != nil {
			return 0, err
		}
		defer restore()

		req.Interactive = true
		// 取得できない場合は指定せず、Incus側の既定に任せる。
		if width, height, err := console.Size(); err == nil {
			req.Width, req.Height = width, height
		}

		resized, stop := console.Resized()
		defer stop()

		ctrl.console = console
		ctrl.resized = resized
	}
	args.Control = controlHandler(ctrl)

	// argvにはスクリプト本文が入りうるため、実行するプログラムだけを示す。
	// 失敗した内容はステップ側のエラーが伝える。
	a.log("exec", "name", name, "program", program(argv))

	op, err := a.Server.ExecInstance(name, req, args)
	if err != nil {
		return 0, fmt.Errorf("exec in %s: %w", name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return 0, fmt.Errorf("exec in %s: %w", name, err)
	}

	// 出力の中継が終わるのを待つ。websocketが半開のまま残ると
	// 永久に閉じないため、中断できるようにしておく。
	select {
	case <-dataDone:
	case <-ctx.Done():
		return 0, fmt.Errorf("exec in %s: %w", name, ctx.Err())
	}

	return exitCodeOf(op.Get()), nil
}

// newExecRequest はIncusへ送るexec要求を組み立てる。実行はしない。
func newExecRequest(argv []string, opt ExecOptions) (api.InstanceExecPost, error) {
	env := map[string]string{}
	if opt.TTY && opt.Term != "" {
		// Incusは既定でTERMを設定しない。端末向けのプログラムが
		// 動かなくなるため、ホストの値を引き継ぐ。
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
			// IncusのexecはUIDのみを受け付ける。
			// ユーザー名の解決は呼び出し側の責務であり、黙って無視しない。
			return req, fmt.Errorf("exec user must be a numeric uid, got %q", opt.User)
		}
		req.User = uint32(uid)
	}
	return req, nil
}

// exitCodeOf は完了したexec操作から終了コードを取り出す。
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

// WaitReady はinstanceがprovisioningを受けられる状態になるまで待つ。
func (a *API) WaitReady(ctx context.Context, name string, opt WaitOptions) error {
	return waitReady(ctx, a, name, opt)
}

// program は実行するプログラム名を返す。
func program(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[0]
}

// sortedKeys はマップのキーを昇順で返す。ログの出力順を安定させる。
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
