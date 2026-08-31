package cli

import (
	"fmt"
	"maps"
	"strconv"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
)

// devkitが管理用に設定するinstance config（仕様 05-incus.md 5.2）。
const (
	managedProjectKey = config.ReservedConfigPrefix + "project"
	managedRootKey    = config.ReservedConfigPrefix + "root"
	managedSchemaKey  = config.ReservedConfigPrefix + "schema"
)

// idmapConfigKey は非特権コンテナでのuid/gid対応付けに使うキー。
const idmapConfigKey = "raw.idmap"

// desiredConfig は dev.yml から適用すべきinstance configを組み立てる。
// mode は解決済みのidmap方式、uid/gid はホストの実行ユーザー。
func desiredConfig(cfg *config.Config, mode config.IDMapMode, uid, gid int) map[string]string {
	out := make(map[string]string, len(cfg.Instance.Config)+4)
	for k, v := range cfg.Instance.Config {
		out[k] = v
	}

	out[managedProjectKey] = cfg.Project.Name
	out[managedRootKey] = cfg.Root
	out[managedSchemaKey] = strconv.Itoa(cfg.Schema)

	// raw方式ではホストの実行ユーザーをコンテナのrootへ対応付ける。
	// uidとgidは異なりうるため個別に指定する。
	// プロジェクトが raw.idmap を明示している場合は尊重する。
	if _, explicit := cfg.Instance.Config[idmapConfigKey]; !explicit && mode == config.IDMapRaw {
		out[idmapConfigKey] = fmt.Sprintf("uid %d 0\ngid %d 0", uid, gid)
	}
	return out
}

// desiredDevices は dev.yml から適用すべきdeviceを組み立てる。
// workspaceは予約名のdisk deviceとして追加する。
func desiredDevices(cfg *config.Config, mode config.IDMapMode) map[string]incus.Device {
	out := make(map[string]incus.Device, len(cfg.Instance.Devices)+1)

	for name, dev := range cfg.Instance.Devices {
		copied := maps.Clone(incus.Device(dev))

		// deviceのsourceはproject rootを基準に解決する（仕様 3.11）。
		if src, ok := copied["source"]; ok && src != "" && !isVolumeSource(copied) {
			copied["source"] = cfg.ResolvePath(src)
		}
		applyShift(copied, mode)

		out[name] = copied
	}

	ws := cfg.WorkspaceOrDefault()
	workspace := incus.Device{
		"type":   "disk",
		"source": cfg.WorkspaceSourcePath(),
		"path":   ws.Target,
	}
	applyShift(workspace, mode)

	out[config.WorkspaceDeviceName] = workspace
	return out
}

// applyShift はホストのディレクトリをマウントするdiskへidmap方式を反映する。
//
// workspace以外の追加マウントにも同じ扱いを適用しないと、
// shift方式のホストで「workspaceだけ書けて追加マウントは書けない」状態になる。
// 方式を切り替えたときに古い設定が残らないよう、常に明示的に設定する。
//
// プロジェクトが shift を明示している場合は、そちらを尊重する。
func applyShift(dev incus.Device, mode config.IDMapMode) {
	if _, explicit := dev["shift"]; explicit {
		return
	}
	if !isHostPathMount(dev) {
		return
	}
	dev["shift"] = strconv.FormatBool(mode == config.IDMapShift)
}

// isHostPathMount はホストのディレクトリをマウントするdiskかを返す。
//
// storage volume（poolを伴うもの）やroot disk、disk以外のdeviceは対象外。
func isHostPathMount(dev incus.Device) bool {
	return dev.Type() == "disk" && dev["source"] != "" && !isVolumeSource(dev)
}

// isVolumeSource は source がホストのパスではなくストレージボリューム名かを返す。
func isVolumeSource(dev incus.Device) bool {
	return dev["pool"] != ""
}

// staleIDMapKeys は現在の方式では不要になった、devkit設定のconfigキーを返す。
func staleIDMapKeys(cfg *config.Config, current map[string]string, mode config.IDMapMode) []string {
	if _, explicit := cfg.Instance.Config[idmapConfigKey]; explicit {
		return nil // 利用者が書いたキーには触れない
	}
	if mode == config.IDMapRaw {
		return nil
	}
	if _, ok := current[idmapConfigKey]; !ok {
		return nil
	}
	return []string{idmapConfigKey}
}

// instanceSpec はinstance作成時の指定を組み立てる。
func instanceSpec(cfg *config.Config, name string, mode config.IDMapMode, uid, gid int) incus.InstanceSpec {
	profiles := cfg.ProfileNames()
	return incus.InstanceSpec{
		Name:       name,
		Image:      cfg.Instance.Image,
		Type:       cfg.Instance.TypeOrDefault(),
		Profiles:   profiles,
		NoProfiles: len(profiles) == 0,
		Config:     desiredConfig(cfg, mode, uid, gid),
		Devices:    desiredDevices(cfg, mode),
	}
}

// isManagedBy はinstanceが当該プロジェクトのdevkit管理下かを返す。
func isManagedBy(instanceConfig map[string]string, projectName string) bool {
	return instanceConfig[managedProjectKey] == projectName
}
