package cli

import (
	"fmt"
	"os"
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
func desiredConfig(cfg *config.Config) map[string]string {
	out := make(map[string]string, len(cfg.Instance.Config)+4)
	for k, v := range cfg.Instance.Config {
		out[k] = v
	}

	out[managedProjectKey] = cfg.Project.Name
	out[managedRootKey] = cfg.Root
	out[managedSchemaKey] = strconv.Itoa(cfg.Schema)

	// workspace.idmap: auto の場合、ホストの実行ユーザーをコンテナのrootへ対応付ける。
	// プロジェクトが raw.idmap を明示している場合は尊重する。
	if _, explicit := cfg.Instance.Config[idmapConfigKey]; !explicit {
		if cfg.WorkspaceOrDefault().IDMap == config.IDMapAuto {
			out[idmapConfigKey] = fmt.Sprintf("both %d 0", os.Getuid())
		}
	}
	return out
}

// desiredDevices は dev.yml から適用すべきdeviceを組み立てる。
// workspaceは予約名のdisk deviceとして追加する。
func desiredDevices(cfg *config.Config) map[string]incus.Device {
	out := make(map[string]incus.Device, len(cfg.Instance.Devices)+1)

	for name, dev := range cfg.Instance.Devices {
		copied := make(incus.Device, len(dev))
		for k, v := range dev {
			copied[k] = v
		}
		// deviceのsourceはproject rootを基準に解決する（仕様 3.11）。
		if src, ok := copied["source"]; ok && src != "" {
			copied["source"] = cfg.ResolvePath(src)
		}
		out[name] = copied
	}

	ws := cfg.WorkspaceOrDefault()
	out[config.WorkspaceDeviceName] = incus.Device{
		"type":   "disk",
		"source": cfg.WorkspaceSourcePath(),
		"path":   ws.Target,
	}
	return out
}

// instanceSpec はinstance作成時の指定を組み立てる。
func instanceSpec(cfg *config.Config, name string) incus.InstanceSpec {
	profiles := cfg.ProfileNames()
	return incus.InstanceSpec{
		Name:       name,
		Image:      cfg.Instance.Image,
		Type:       cfg.Instance.TypeOrDefault(),
		Profiles:   profiles,
		NoProfiles: len(profiles) == 0,
		Config:     desiredConfig(cfg),
		Devices:    desiredDevices(cfg),
	}
}

// isManagedBy はinstanceが当該プロジェクトのdevkit管理下かを返す。
func isManagedBy(instanceConfig map[string]string, projectName string) bool {
	return instanceConfig[managedProjectKey] == projectName
}
