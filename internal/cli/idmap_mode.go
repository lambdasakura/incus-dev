package cli

import (
	"fmt"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
)

// idmapPlan is a resolved uid/gid mapping strategy.
//
// Holding "which strategy" and "does devkit manage this at all" in one value is
// what lets planning (plan.go) and applying (app.go) share the same decision.
type idmapPlan struct {
	// Mode is the strategy to apply. It is meaningless when Managed is false.
	Mode config.IDMapMode
	// Managed reports whether devkit manages the mapping. It is false when the
	// user set raw.idmap in instance.config, and for instances that are not
	// containers.
	Managed bool
	// UID and GID are the invoking host user.
	UID, GID int
	// Warning is what to tell the user. Empty means nothing.
	Warning string
}

// shiftEnabled reports whether disk devices should get shift set.
func (p idmapPlan) shiftEnabled() bool {
	return p.Managed && p.Mode == config.IDMapShift
}

// rawIDMap returns the value to set raw.idmap to, or the empty string to set
// nothing.
func (p idmapPlan) rawIDMap() string {
	if !p.Managed || p.Mode != config.IDMapRaw {
		return ""
	}
	// The uid and the gid can differ, so map them separately.
	return fmt.Sprintf("uid %d 0\ngid %d 0", p.UID, p.GID)
}

// userManagesIDMap reports whether the user set the mapping themselves.
func userManagesIDMap(cfg *config.Config) bool {
	_, explicit := cfg.Instance.Config[idmapConfigKey]
	return explicit
}

// resolveIDMap decides which idmap strategy to apply.
//
//   - With raw.idmap set by the user, it stays out of the way
//   - Outside a container the mapping means something else, so it stays out of
//     the way there too
//   - auto: raw when raw works, otherwise fall back to shift and warn
//   - raw: an error when raw does not work
//   - shift and none: as they are
func resolveIDMap(cfg *config.Config, uid, gid int, check func(uid, gid int) error) (idmapPlan, error) {
	plan := idmapPlan{UID: uid, GID: gid}
	declared := cfg.WorkspaceOrDefault().IDMap

	if userManagesIDMap(cfg) {
		if cfg.Workspace != nil && cfg.Workspace.IDMap != "" {
			plan.Warning = fmt.Sprintf(
				"instance.config.%s is set, so workspace.idmap: %s is ignored", idmapConfigKey, declared)
		}
		return plan, nil
	}
	if cfg.Instance.TypeOrDefault() != "container" {
		// Both raw.idmap and a disk's shift are container-only mechanisms.
		return plan, nil
	}

	plan.Managed = true
	plan.Mode = declared

	switch declared {
	case config.IDMapRaw:
		if err := check(uid, gid); err != nil {
			return idmapPlan{}, err
		}
	case config.IDMapAuto:
		if err := check(uid, gid); err != nil {
			// Fall back to shift rather than failing, so it works without the host
			// being touched.
			plan.Mode = config.IDMapShift
			plan.Warning = fallbackWarning(uid, gid)
			//nolint:nilerr // the fallback is deliberate, and the user is told through the warning
			return plan, nil
		}
		plan.Mode = config.IDMapRaw
	}
	return plan, nil
}

// fallbackWarning says that it fell back to shift, and how to do better.
func fallbackWarning(uid, gid int) string {
	return fmt.Sprintf(
		"workspace is mounted with shift (idmapped mount) because raw.idmap is not permitted on this host.\n"+
			"        Files created inside the container will be owned by root on the host.\n"+
			"        To have them owned by you, add 'root:%d:1' to %s and 'root:%d:1' to %s,\n"+
			"        then run 'idev up' again.",
		uid, subUIDPath, gid, subGIDPath)
}
