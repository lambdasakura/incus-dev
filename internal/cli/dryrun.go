package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/provision"
)

// Plan は idev up が行う操作を、実行せずに表示する（仕様 04-cli.md 4.8）。
//
// ホスト側の前提（idmap、Profileの存在）は up と同じように確認する。
// Incusへの読み取りは行うが、変更は一切行わない。
func (a *App) Plan(ctx context.Context) error {
	plan, err := a.idmapPlan()
	if err != nil {
		return err
	}
	if plan.Warning != "" {
		a.log.Warn(plan.Warning)
	}
	if err := a.checkProfiles(ctx); err != nil {
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

	for _, action := range planActions(a.cfg, a.instance, current, plan) {
		if _, err := fmt.Fprintln(a.out, action); err != nil {
			return err
		}
	}
	return nil
}

// planActions は実行予定の操作を列挙する。
//
// 副作用を持たない純粋関数として組み立てることで、
// 実際に適用する経路（desiredConfig / desiredDevices）と
// 同じ計算結果を表示できる。
func planActions(cfg *config.Config, name string, current *incus.Instance, idmap idmapPlan) []string {
	var out []string

	desiredCfg := desiredConfig(cfg, idmap)
	desiredDev := desiredDevices(cfg, idmap)

	if current == nil {
		out = append(out, fmt.Sprintf("Create instance %s (%s)", name, cfg.Instance.Image))
		if t := cfg.Instance.TypeOrDefault(); t != "container" {
			out = append(out, "Instance type: "+t)
		}

		if profiles := cfg.ProfileNames(); len(profiles) > 0 {
			out = append(out, "Apply profiles: "+strings.Join(profiles, ", "))
		} else {
			out = append(out, "Apply no profiles")
		}
		out = append(out, configActions(nil, desiredCfg, nil)...)
		out = append(out, deviceActions(nil, desiredDev)...)
		out = append(out, "Start instance")
	} else {
		out = append(out, fmt.Sprintf("Use existing instance %s (%s)", name, current.Status))
		out = append(out, configActions(current.Config, desiredCfg, staleIDMapKeys(current.Config, idmap))...)
		out = append(out, deviceActions(current.Devices, desiredDev)...)

		if !current.IsRunning() {
			out = append(out, "Start instance")
		}
	}

	return append(out, provisionActions(cfg)...)
}

// configActions はinstance configへの変更を列挙する。
//
// devkitの管理用キー（user.incus-devkit.*）は、利用者が書いたものではなく
// 常に設定されるため、1行にまとめる。
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
		out = append(out, "Set devkit markers ("+config.ReservedConfigPrefix+"*)")
	}
	return out
}

// deviceActions はdeviceへの変更を列挙する。
func deviceActions(current, desired map[string]incus.Device) []string {
	var out []string

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

// changedKeys は現状と異なるキーを key=value の形で返す。
func changedKeys(have, want incus.Device) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if k != "type" && have[k] != want[k] {
			out = append(out, k+"="+want[k])
		}
	}
	return out
}

// deviceDetail はdeviceの要点（マウント元と先）を返す。
func deviceDetail(dev incus.Device) string {
	if src, path := dev["source"], dev["path"]; src != "" && path != "" {
		return fmt.Sprintf(" %s -> %s", src, path)
	}
	return ""
}

// provisionActions はbootstrapとprovisionの実行予定を列挙する。
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

// singleLine は複数行の値を1行にまとめる。
func singleLine(v string) string {
	return strings.ReplaceAll(strings.TrimRight(v, "\n"), "\n", " / ")
}
