package incus

import (
	"context"
	"fmt"
	"maps"
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

	GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error)
	CreateStoragePoolVolume(pool string, volume api.StorageVolumesPost) error
	DeleteStoragePoolVolume(pool, volType, name string) error

	GetInstanceSnapshots(name string) ([]api.InstanceSnapshot, error)
	CreateInstanceSnapshot(name string, snapshot api.InstanceSnapshotsPost) (incusclient.Operation, error)
	DeleteInstanceSnapshot(name, snapshot string) (incusclient.Operation, error)
}

// imageResolver は image 参照（例 images:ubuntu/24.04）を解決する。
type imageResolver interface {
	Resolve(ctx context.Context, ref string) (incusclient.ImageServer, *api.Image, error)
}

// API はIncusのHTTP APIを直接呼ぶ Client 実装。
//
// CLI出力のパースが不要になり、型付きで扱える。
// ただし端末を伴う実行だけは、端末制御の都合でCLIへ委譲する
// （仕様 05-incus.md 5.7.1）。
type API struct {
	Server server
	Images imageResolver
	// Terminal は端末を伴う実行に使う。
	Terminal Client
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

// InstanceExists はinstanceの存在を返す。
func (a *API) InstanceExists(ctx context.Context, name string) (bool, error) {
	_, err := a.Instance(ctx, name)
	switch {
	case err == nil:
		return true, nil
	case isNotFound(err):
		return false, nil
	default:
		return false, err
	}
}

// CreateInstance はinstanceを作成する（起動はしない）。
func (a *API) CreateInstance(ctx context.Context, spec InstanceSpec) error {
	source, image, err := a.Images.Resolve(ctx, spec.Image)
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

	op, err := a.Server.CreateInstanceFromImage(source, *image, req)
	if err != nil {
		return fmt.Errorf("create instance %s: %w", spec.Name, err)
	}
	// RemoteOperation は context を受け取らないため、完了を待つ。
	if err := op.Wait(); err != nil {
		return fmt.Errorf("create instance %s: %w", spec.Name, err)
	}
	return nil
}

// StartInstance はinstanceを起動する。
func (a *API) StartInstance(ctx context.Context, name string) error {
	return a.changeState(ctx, name, "start", false)
}

// StopInstance はinstanceを停止する。
func (a *API) StopInstance(ctx context.Context, name string) error {
	return a.changeState(ctx, name, "stop", true)
}

func (a *API) changeState(ctx context.Context, name, action string, force bool) error {
	op, err := a.Server.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  action,
		Force:   force,
		Timeout: -1,
	}, "")
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
	if inst.IsRunning() {
		if err := a.StopInstance(ctx, name); err != nil {
			return err
		}
	}

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
	return a.updateInstance(ctx, name, func(put *api.InstancePut) {
		maps.Copy(put.Config, config)
	})
}

// UnsetConfig は指定されたconfigキーを削除する。
func (a *API) UnsetConfig(ctx context.Context, name string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
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
	return a.updateInstance(ctx, name, func(put *api.InstancePut) {
		if put.Devices == nil {
			put.Devices = map[string]map[string]string{}
		}
		for devName, want := range devices {
			current, exists := put.Devices[devName]
			if !exists || Device(current).Type() != want.Type() {
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
	volumes, err := a.Server.GetStoragePoolVolumes(pool)
	if err != nil {
		return false, fmt.Errorf("list storage volumes on %s: %w", pool, err)
	}
	for _, v := range volumes {
		if v.Name == name && v.Type == storageVolumeType {
			return true, nil
		}
	}
	return false, nil
}

// storageVolumeType はdevkitが扱うstorage volumeの種別。
const storageVolumeType = "custom"

// CreateVolume はstorage volumeを作成する。
func (a *API) CreateVolume(_ context.Context, pool, name string, config map[string]string) error {
	req := api.StorageVolumesPost{Name: name, Type: storageVolumeType}
	req.Config = config

	if err := a.Server.CreateStoragePoolVolume(pool, req); err != nil {
		return fmt.Errorf("create storage volume %s on %s: %w", name, pool, err)
	}
	return nil
}

// DeleteVolume はstorage volumeを削除する。
func (a *API) DeleteVolume(_ context.Context, pool, name string) error {
	if err := a.Server.DeleteStoragePoolVolume(pool, storageVolumeType, name); err != nil {
		return fmt.Errorf("delete storage volume %s on %s: %w", name, pool, err)
	}
	return nil
}

// CreateSnapshot はinstanceのスナップショットを作成する。
func (a *API) CreateSnapshot(ctx context.Context, instance, snapshot string) error {
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
func (a *API) RestoreSnapshot(ctx context.Context, instance, snapshot string) error {
	return a.updateInstance(ctx, instance, func(put *api.InstancePut) {
		put.Restore = snapshot
	})
}

// DeleteSnapshot はスナップショットを削除する。
func (a *API) DeleteSnapshot(ctx context.Context, instance, snapshot string) error {
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
//
// 端末を伴う実行は端末制御（raw mode、ウィンドウサイズ変更）が必要なため、
// CLIへ委譲する（仕様 05-incus.md 5.7.1）。
func (a *API) Exec(ctx context.Context, name string, argv []string, opt ExecOptions) (int, error) {
	if opt.TTY {
		if a.Terminal == nil {
			return 0, fmt.Errorf("interactive exec requires the incus command")
		}
		return a.Terminal.Exec(ctx, name, argv, opt)
	}

	env := maps.Clone(opt.PublicEnv)
	if env == nil {
		env = map[string]string{}
	}
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
			// incusのexecはUIDのみを受け付ける。
			// ユーザー名の解決は呼び出し側の責務であり、黙って無視しない。
			return 0, fmt.Errorf("exec user must be a numeric uid, got %q", opt.User)
		}
		req.User = uint32(uid)
	}

	dataDone := make(chan bool)
	op, err := a.Server.ExecInstance(name, req, &incusclient.InstanceExecArgs{
		Stdin:    opt.Stdin,
		Stdout:   opt.Stdout,
		Stderr:   opt.Stderr,
		DataDone: dataDone,
	})
	if err != nil {
		return 0, fmt.Errorf("exec in %s: %w", name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		return 0, fmt.Errorf("exec in %s: %w", name, err)
	}
	<-dataDone

	return exitCodeOf(op.Get()), nil
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
