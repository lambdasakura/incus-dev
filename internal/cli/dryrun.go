package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/provision"
)

// Plan prints what idev up would do, without doing it (spec 04-cli.md 4.8).
//
// It checks the host-side prerequisites — idmap, the profiles existing — just
// as up does. It reads from Incus, and changes nothing.
func (a *App) Plan(ctx context.Context) error {
	plan, err := a.idmapPlan()
	if err != nil {
		return err
	}
	if plan.Warning != "" {
		a.log.Warn(plan.Warning)
	}
	if _, err := a.env(); err != nil {
		return err
	}
	if err := a.checkProfiles(ctx); err != nil {
		return err
	}
	// The same host-side checks up makes, so the preflight does not pass while
	// up fails on one of them (spec 04-cli.md 4.7).
	if err := a.exec.CheckPrerequisites(ctx, a.cfg.Provision); err != nil {
		return err
	}

	current, err := a.client.Instance(ctx, a.instance)
	switch {
	case err == nil:
		if !isManagedBy(current.Config, a.cfg.Project.Name) {
			return a.unmanagedError(current)
		}
	case errors.Is(err, incus.ErrInstanceNotFound):
		current = nil
	default:
		return err
	}

	if current != nil {
		// The same warnings up gives. A preflight that stays quiet about what
		// up would refuse to apply is not a preflight (spec 04-cli.md 4.8).
		a.pruneVolumeRecord(ctx, current)
		a.warnChanges(current, wouldRemountHere)
		a.warnRestartNeeded(current, plan)
	} else if err := a.client.CheckImage(ctx, a.cfg.Instance.Image); err != nil {
		// Only when the instance would be created, because that is the only
		// time up fetches an image. The reference is resolved against a
		// remote, so this is the one check nothing offline can make -- and
		// the image is the likeliest thing in dev.yml to be mistyped
		// (spec 04-cli.md 4.7).
		return err
	}

	existing, err := a.existingVolumes(ctx)
	if err != nil {
		return err
	}

	for _, action := range planActions(a.cfg, a.instance, current, plan, existing) {
		if _, err := fmt.Fprintln(a.out, action); err != nil {
			return err
		}
	}
	return nil
}

// planActions lists the operations that would run.
//
// Building it as a pure function with no side effects is what lets it show the
// same result the applying path (desiredConfig, desiredDevices) computes.
func planActions(cfg *config.Config, name string, current *incus.Instance, idmap idmapPlan, existingVolumes map[string]bool) []string {
	var out []string

	var currentCfg map[string]string
	if current != nil {
		currentCfg = current.Config
	}
	desiredCfg := desiredConfig(cfg, idmap, currentCfg, name)
	desiredDev := desiredDevices(cfg, idmap, name)

	out = append(out, volumeActions(cfg, name, existingVolumes)...)

	if current == nil {
		out = append(out, fmt.Sprintf("Create instance %s (%s)", name, cfg.Instance.Image))
		if profiles := cfg.ProfileNames(); len(profiles) > 0 {
			out = append(out, "Apply profiles: "+strings.Join(profiles, ", "))
		} else {
			out = append(out, "Apply no profiles")
		}
		out = append(out, configActions(nil, desiredCfg, nil)...)
		out = append(out, deviceActions(nil, desiredDev, nil)...)
		out = append(out, "Start instance")
	} else {
		out = append(out, fmt.Sprintf("Use existing instance %s (%s)", name, current.Status))
		// The same functions the applying path uses, so the plan cannot
		// under-report what up would remove.
		out = append(out, configActions(current.Config, desiredCfg,
			staleConfigKeys(current.Config, desiredCfg, idmap))...)
		out = append(out, deviceActions(current.Devices, desiredDev,
			staleDevices(current, desiredDev))...)

		if !current.IsRunning() {
			out = append(out, "Start instance")
		}
	}

	return append(out, provisionActions(cfg)...)
}

// existingVolumes reports which of the declared volumes are already there.
func (a *App) existingVolumes(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	for _, key := range slices.Sorted(maps.Keys(a.cfg.Volumes)) {
		vol := a.cfg.Volumes[key]
		pool, name := vol.PoolOrDefault(), volumeName(a.instance, key)

		// A pool that is not there stops up on its first volume, so the
		// preflight stops here too. Reporting the volume as one that would be
		// created and exiting 0 is the false green light spec 04-cli.md 4.7
		// leans on this command not to give.
		exists, err := a.client.VolumeExists(ctx, pool, name)
		if err != nil {
			return nil, err
		}
		out[pool+"/"+name] = exists
	}
	return out, nil
}

// volumeActions lists the volumes that would be created.
//
// Allocating storage is the one thing up does that consumes space on the host,
// so the preview has to say it will happen.
func volumeActions(cfg *config.Config, name string, existing map[string]bool) []string {
	var out []string
	for _, key := range slices.Sorted(maps.Keys(cfg.Volumes)) {
		vol := cfg.Volumes[key]
		pool, volume := vol.PoolOrDefault(), volumeName(name, key)
		if existing[pool+"/"+volume] {
			// Adoption is worth saying: it puts data idev did not create under
			// `idev destroy --volumes`.
			out = append(out, fmt.Sprintf("Use existing volume %s on pool %s", volume, pool))
			continue
		}
		out = append(out, fmt.Sprintf("Create volume %s on pool %s", volume, pool))
	}
	return out
}

// configActions lists the changes to the instance config.
//
// idev's bookkeeping keys (user.incus-dev.*) are not something the user
// wrote and are always set, so they collapse into one line.
func configActions(current, desired map[string]string, stale []string) []string {
	var out []string
	for _, k := range stale {
		out = append(out, "Unset config "+k)
	}

	markers := false
	for _, k := range slices.Sorted(maps.Keys(desired)) {
		if current[k] == desired[k] {
			continue
		}
		if strings.HasPrefix(k, config.ReservedConfigPrefix) {
			markers = true
			continue
		}
		out = append(out, fmt.Sprintf("Set config %s=%s", k, singleLine(desired[k])))
	}
	if markers {
		out = append(out, "Set idev markers ("+config.ReservedConfigPrefix+"*)")
	}
	return out
}

// deviceActions lists the changes to the devices.
func deviceActions(current, desired map[string]incus.Device, stale []string) []string {
	var out []string

	for _, name := range stale {
		out = append(out, "Remove device "+name)
	}

	for _, name := range slices.Sorted(maps.Keys(desired)) {
		want := desired[name]
		have, exists := current[name]

		if !exists {
			out = append(out, fmt.Sprintf("Add device %s (%s%s)", name, want.Type(), deviceDetail(want)))
			continue
		}
		if changed := changedKeys(have, want); len(changed) > 0 {
			out = append(out, fmt.Sprintf("Update device %s (%s)", name, strings.Join(changed, " ")))
		}
	}
	return out
}

// changedKeys returns the keys that differ, as key=value.
func changedKeys(have, want incus.Device) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if have[k] != want[k] {
			out = append(out, k+"="+want[k])
		}
	}
	return out
}

// deviceDetail returns what matters about a device: what it mounts, and where.
func deviceDetail(dev incus.Device) string {
	if src, path := dev["source"], dev["path"]; src != "" && path != "" {
		return fmt.Sprintf(" %s -> %s", src, path)
	}
	return ""
}

// provisionActions lists the bootstrap and provisioning that would run.
func provisionActions(cfg *config.Config) []string {
	steps := provision.BootstrapSteps(cfg)

	var out []string
	switch {
	case len(steps) == 0:
		out = append(out, "Bootstrap: skipped")
	case cfg.Bootstrap == nil:
		out = append(out, fmt.Sprintf("Bootstrap: %d step (default)", len(steps)))
	default:
		out = append(out, fmt.Sprintf("Bootstrap: %d step(s) (from dev.yml)", len(steps)))
	}

	total := len(cfg.Provision)
	for i, step := range cfg.Provision {
		detail := ""
		switch {
		case step.Ansible != nil:
			detail = " " + step.Ansible.Playbook
		case step.Galaxy != nil:
			detail = " " + step.Galaxy.Requirements
		}
		out = append(out, fmt.Sprintf("Provision step %d/%d: %s (%s%s)",
			i+1, total, step.DisplayName(i+1), stepKind(step), detail))
	}
	return out
}

// singleLine folds a multi-line value onto one line.
func singleLine(v string) string {
	return strings.ReplaceAll(strings.TrimRight(v, "\n"), "\n", " / ")
}
