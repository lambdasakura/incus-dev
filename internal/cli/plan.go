package cli

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
)

// The instance config idev sets for its own bookkeeping (spec 05-incus.md 5.2).
const (
	managedProjectKey = config.ReservedConfigPrefix + "project"
	managedRootKey    = config.ReservedConfigPrefix + "root"
	managedSchemaKey  = config.ReservedConfigPrefix + "schema"
	// managedKeysKey records which instance config keys idev applied.
	managedKeysKey = config.ReservedConfigPrefix + "managed"
	// managedDevicesKey records which devices idev created.
	managedDevicesKey = config.ReservedConfigPrefix + "devices"
)

// idmapConfigKey is the key that maps uids and gids in an unprivileged
// container.
const idmapConfigKey = "raw.idmap"

// desiredConfig builds the instance config to apply, from dev.yml.
func desiredConfig(cfg *config.Config, plan idmapPlan) map[string]string {
	out := make(map[string]string, len(cfg.Instance.Config)+4)
	for k, v := range cfg.Instance.Config {
		out[k] = v
	}

	out[managedProjectKey] = cfg.Project.Name
	out[managedRootKey] = cfg.Root
	out[managedSchemaKey] = strconv.Itoa(cfg.Schema)

	// The raw strategy maps the invoking host user onto root in the container.
	if v := plan.rawIDMap(); v != "" {
		out[idmapConfigKey] = v
	}

	// Record the keys and devices applied, so that dropping one from the
	// declaration can be followed (spec 05-incus.md 5.4.4). The bookkeeping
	// keys themselves are not included.
	out[managedKeysKey] = strings.Join(managedNames(out), ",")
	out[managedDevicesKey] = strings.Join(managedDeviceNames(cfg), ",")

	return out
}

// managedDeviceNames returns the devices idev creates.
func managedDeviceNames(cfg *config.Config) []string {
	names := []string{config.WorkspaceDeviceName}
	names = append(names, slices.Collect(maps.Keys(cfg.Instance.Devices))...)
	names = append(names, slices.Collect(maps.Keys(cfg.Volumes))...)
	slices.Sort(names)

	return names
}

// managedNames returns the config keys to record, excluding idev's own
// bookkeeping keys.
func managedNames(desired map[string]string) []string {
	out := make([]string, 0, len(desired))
	for _, k := range slices.Sorted(maps.Keys(desired)) {
		if !strings.HasPrefix(k, config.ReservedConfigPrefix) {
			out = append(out, k)
		}
	}
	return out
}

// staleConfigKeys returns the idev-applied config keys the declaration
// dropped.
//
// On an older instance with no record (user.incus-dev.managed), only the
// idmap key idev set itself is in scope.
func staleConfigKeys(current, desired map[string]string, plan idmapPlan) []string {
	recorded, ok := current[managedKeysKey]
	if !ok {
		return staleIDMapKeys(current, plan)
	}

	var out []string
	for _, k := range splitList(recorded) {
		if _, want := desired[k]; !want {
			if _, exists := current[k]; exists {
				out = append(out, k)
			}
		}
	}
	return out
}

// staleDevices returns the idev-created devices the declaration dropped.
func staleDevices(current *incus.Instance, desired map[string]incus.Device) []string {
	var out []string
	for _, name := range splitList(current.Config[managedDevicesKey]) {
		if _, want := desired[name]; want {
			continue
		}
		if _, exists := current.Devices[name]; exists {
			out = append(out, name)
		}
	}
	return out
}

func splitList(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// desiredDevices builds the devices to apply, from dev.yml. The workspace is
// added as a disk device under the reserved name. The instance name is needed
// to keep persistent volume names unique.
func desiredDevices(cfg *config.Config, plan idmapPlan, instance string) map[string]incus.Device {
	out := make(map[string]incus.Device, len(cfg.Instance.Devices)+1)

	for name, dev := range cfg.Instance.Devices {
		copied := maps.Clone(incus.Device(dev))

		// A device's source is resolved from the project root (spec 3.11).
		if src, ok := copied["source"]; ok && src != "" && !isVolumeSource(copied) {
			copied["source"] = cfg.ResolvePath(src)
		}
		applyShift(copied, plan)

		out[name] = copied
	}

	// Persistent volumes are attached as devices too.
	for _, name := range slices.Sorted(maps.Keys(cfg.Volumes)) {
		vol := cfg.Volumes[name]
		out[name] = incus.Device{
			"type":   "disk",
			"pool":   vol.PoolOrDefault(),
			"source": volumeName(instance, name),
			"path":   vol.Path,
		}
	}

	ws := cfg.WorkspaceOrDefault()
	workspace := incus.Device{
		"type":   "disk",
		"source": cfg.WorkspaceSourcePath(),
		"path":   ws.Target,
	}
	applyShift(workspace, plan)

	out[config.WorkspaceDeviceName] = workspace
	return out
}

// volumeName returns a persistent volume's name in Incus.
//
// Making it unique per instance keeps several checkouts from sharing one
// volume.
func volumeName(instance, key string) string {
	return instance + "-" + key
}

// applyShift puts the idmap strategy onto a disk that mounts a host directory.
//
// Extra mounts get the same treatment as the workspace; without that, a host
// using the shift strategy ends up able to write to the workspace but not to
// anything else. It is always set explicitly, so switching strategies leaves
// nothing stale behind.
//
// A shift the project set explicitly wins.
func applyShift(dev incus.Device, plan idmapPlan) {
	if !plan.Managed {
		return
	}
	if _, explicit := dev["shift"]; explicit {
		return
	}
	if !isHostPathMount(dev) {
		return
	}
	dev["shift"] = strconv.FormatBool(plan.shiftEnabled())
}

// isHostPathMount reports whether a disk mounts a host directory.
//
// Storage volumes (those with a pool), root disks and non-disk devices are out
// of scope.
func isHostPathMount(dev incus.Device) bool {
	return dev.Type() == "disk" && dev["source"] != "" && !isVolumeSource(dev)
}

// isVolumeSource reports whether source names a storage volume rather than a
// path on the host.
func isVolumeSource(dev incus.Device) bool {
	return dev.Type() == "disk" && dev["pool"] != ""
}

// staleIDMapKeys returns the idev-set config keys the current strategy no
// longer needs.
func staleIDMapKeys(current map[string]string, plan idmapPlan) []string {
	if !plan.Managed || plan.Mode == config.IDMapRaw {
		// Leave it alone when the user manages it, or when it should still be set.
		return nil
	}
	if _, ok := current[idmapConfigKey]; !ok {
		return nil
	}
	return []string{idmapConfigKey}
}

// instanceSpec builds what to pass when creating an instance.
func instanceSpec(cfg *config.Config, name string, plan idmapPlan) incus.InstanceSpec {
	profiles := cfg.ProfileNames()
	return incus.InstanceSpec{
		Name:       name,
		Image:      cfg.Instance.Image,
		Profiles:   profiles,
		NoProfiles: len(profiles) == 0,
		Config:     desiredConfig(cfg, plan),
		Devices:    desiredDevices(cfg, plan, name),
	}
}

// isManagedBy reports whether idev manages the instance for this project.
func isManagedBy(instanceConfig map[string]string, projectName string) bool {
	return instanceConfig[managedProjectKey] == projectName
}
