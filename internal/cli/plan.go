package cli

import (
	"fmt"
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
	// managedImageKey records the image the instance was created from. up
	// never re-images an existing instance, so this is what tells the user
	// their edit to instance.image has not taken effect.
	managedImageKey = config.ReservedConfigPrefix + "image"
	// managedRestartKey records the keys whose change is waiting on a restart.
	// The warning is emitted by the run that writes them, so by the time the
	// user acts on it there is nothing left to compare against.
	managedRestartKey = config.ReservedConfigPrefix + "restart-pending"
	// managedVolumesKey records the volumes idev created, as pool/name. A
	// volume dropped from the declaration would otherwise be unreachable:
	// nothing names it any more, so nothing could delete it.
	managedVolumesKey = config.ReservedConfigPrefix + "volumes"
)

// idmapConfigKey is the key that maps uids and gids in an unprivileged
// container.
// idmapConfigKey is incus.IDMapKey, kept as a local name for readability.
const idmapConfigKey = incus.IDMapKey

// desiredConfig builds the instance config to apply, from dev.yml.
//
// current is the instance's config as it stands, or nil when it is being
// created. It is read for the volume record, which accumulates: a volume that
// leaves the declaration stays reachable.
func desiredConfig(cfg *config.Config, plan idmapPlan, current map[string]string, instance string) map[string]string {
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
	out[managedVolumesKey] = strings.Join(knownVolumes(current, cfg, instance), ",")

	return out
}

// knownVolumes returns every volume idev has created for this instance: what
// is declared now, plus what an earlier run recorded.
func knownVolumes(current map[string]string, cfg *config.Config, instance string) []string {
	out := declaredVolumes(cfg, instance)
	for _, ref := range splitList(current[managedVolumesKey]) {
		if !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	slices.Sort(out)
	return out
}

// declaredVolumes returns the volumes dev.yml asks for, as pool/name.
func declaredVolumes(cfg *config.Config, instance string) []string {
	out := make([]string, 0, len(cfg.Volumes))
	for _, key := range slices.Sorted(maps.Keys(cfg.Volumes)) {
		out = append(out, cfg.Volumes[key].PoolOrDefault()+"/"+volumeName(instance, key))
	}
	return out
}

// undeclaredVolumes returns the recorded volumes dev.yml no longer asks for,
// which are the only ones the user has to remove by hand.
func undeclaredVolumes(cfg *config.Config, instance string, recorded []string) []string {
	declared := declaredVolumes(cfg, instance)

	var out []string
	for _, ref := range recorded {
		if !slices.Contains(declared, ref) {
			out = append(out, ref)
		}
	}
	return out
}

// splitVolume splits a recorded pool/name back apart.
func splitVolume(ref string) (pool, name string, ok bool) {
	pool, name, ok = strings.Cut(ref, "/")
	return pool, name, ok && pool != "" && name != ""
}

// managedDeviceNames returns the devices idev creates.
func managedDeviceNames(cfg *config.Config) []string {
	var names []string
	for name := range cfg.Mounts() {
		names = append(names, config.MountDeviceName(name))
	}
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

	// Every host mount, main included. main lands under the device name
	// "workspace" rather than its key (spec 3.7.2).
	for name, mount := range cfg.Mounts() {
		device := incus.Device{
			"type":   "disk",
			"source": cfg.ResolvePath(mount.Source),
			"path":   mount.Target,
		}
		if mount.Readonly {
			device["readonly"] = "true"
		}
		applyShift(device, plan)

		out[config.MountDeviceName(name)] = device
	}
	return out
}

// volumeName returns a persistent volume's name in Incus.
//
// Making it unique per instance keeps several checkouts from sharing one
// volume.
func volumeName(instance, key string) string {
	return instance + "-" + key
}

// maxVolumeNameLength is Incus's cap on a storage volume name
// (shared/validate.IsAPIName). The device name a volume key also becomes has
// its own, shorter cap, which the schema carries.
const maxVolumeNameLength = 64

// checkVolumeNames refuses a volume whose derived name Incus will not take.
//
// The schema caps the key, but the name is the key with the instance in front
// of it, and the instance name is not known until the project and its scope
// are resolved -- so this is the one part of the limit only the CLI can check.
func checkVolumeNames(cfg *config.Config, instance string) error {
	for _, key := range slices.Sorted(maps.Keys(cfg.Volumes)) {
		if name := volumeName(instance, key); len(name) > maxVolumeNameLength {
			return fmt.Errorf("volumes.%s: the volume would be named %s, "+
				"which is %d characters; Incus takes at most %d, so shorten the key",
				key, name, len(name), maxVolumeNameLength)
		}
	}
	return nil
}

// applyShift puts the idmap strategy onto a disk that mounts a host directory.
//
// Extra mounts get the same treatment as the workspace; without that, a host
// using the shift strategy ends up able to write to the workspace but not to
// anything else.
//
// Nothing is set when the user manages the mapping themselves: the spec has
// idev stay out of it entirely (03-configuration.md 3.7.3). Leaving the key
// out clears a shift idev set earlier, because ApplyDevices replaces a
// declared device rather than merging into it.
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
func instanceSpec(cfg *config.Config, name string, plan idmapPlan, current map[string]string) incus.InstanceSpec {
	profiles := cfg.ProfileNames()
	config := desiredConfig(cfg, plan, current, name)
	// Written at creation and never again: it is what the instance was made
	// from, which up cannot change. Rewriting it on every run would erase the
	// one record that can tell the user their edit had no effect.
	config[managedImageKey] = cfg.Instance.Image

	return incus.InstanceSpec{
		Name:       name,
		Image:      cfg.Instance.Image,
		Profiles:   profiles,
		NoProfiles: len(profiles) == 0,
		Config:     config,
		Devices:    desiredDevices(cfg, plan, name),
	}
}

// isManagedBy reports whether idev manages the instance for this project.
func isManagedBy(instanceConfig map[string]string, projectName string) bool {
	return instanceConfig[managedProjectKey] == projectName
}

// stepsAt returns the steps at the given positions.
func stepsAt(steps []config.Step, indices []int) []config.Step {
	out := make([]config.Step, 0, len(indices))
	for _, i := range indices {
		out = append(out, steps[i])
	}
	return out
}

// recordedVolumes returns the volumes to consider idev's, newest record first
// and falling back to the declaration for an instance made before the record
// existed.
func recordedVolumes(inst *incus.Instance, cfg *config.Config, instance string) []string {
	if recorded, ok := inst.Config[managedVolumesKey]; ok {
		return splitList(recorded)
	}

	// No record: the instance predates it. Its volumes are still attached as
	// disk devices, and that is the only place left that names them -- the
	// declaration alone would miss exactly the ones the user can no longer
	// reach any other way.
	out := declaredVolumes(cfg, instance)
	for _, name := range slices.Sorted(maps.Keys(inst.Devices)) {
		dev := inst.Devices[name]
		if !isVolumeSource(dev) || dev["source"] == "" {
			continue
		}
		if ref := dev["pool"] + "/" + dev["source"]; !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}
