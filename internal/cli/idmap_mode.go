package cli

import (
	"fmt"

	"github.com/lambdasakura/incus-dev/internal/config"
)

// idmapPlan is a resolved uid/gid mapping strategy.
//
// Holding "which strategy" and "does idev manage this at all" in one value is
// what lets planning (plan.go) and applying (app.go) share the same decision.
type idmapPlan struct {
	// Mode is the strategy to apply. It is meaningless when Managed is false.
	Mode config.IDMapMode
	// Managed reports whether idev manages the mapping. It is false when the
	// user set raw.idmap in instance.config, and for instances that are not
	// containers.
	Managed bool
	// UID and GID are the invoking host user.
	UID, GID int
	// Owner is the container-side id the host user is mapped onto. The zero
	// value is root, which is what raw did before it could be chosen
	// (spec 03-configuration.md 3.7.3).
	Owner config.ResolvedOwner
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
	// The uid and the gid can differ on both sides, so map them separately.
	return fmt.Sprintf("uid %d %d\ngid %d %d", p.UID, p.Owner.UID, p.GID, p.Owner.GID)
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
//   - shift: an error when the kernel has no idmapped mounts
//   - none: as it is
//
// shiftOK is asked only when shift is what would be used. On a host where raw
// works it is never called, so choosing raw costs no round trip.
func resolveIDMap(cfg *config.Config, uid, gid int, check func(uid, gid int) error,
	shiftOK func() (bool, error),
) (idmapPlan, error) {
	ws := cfg.WorkspaceOrDefault()
	plan := idmapPlan{UID: uid, GID: gid, Owner: ws.Owner}
	declared := ws.IDMap

	if userManagesIDMap(cfg) {
		if cfg.Workspace != nil && cfg.Workspace.IDMap != "" {
			plan.Warning = fmt.Sprintf(
				"instance.config.%s is set, so workspace.idmap: %s is ignored", idmapConfigKey, declared)
		}
		return plan, nil
	}
	plan.Managed = true
	plan.Mode = declared

	switch declared {
	case config.IDMapRaw:
		if err := check(uid, gid); err != nil {
			return idmapPlan{}, err
		}
	case config.IDMapShift:
		if err := requireIDMappedMounts(shiftOK); err != nil {
			return idmapPlan{}, err
		}
	case config.IDMapAuto:
		if rawErr := check(uid, gid); rawErr != nil {
			// Fall back to shift rather than failing, so it works without the
			// host being touched -- where the kernel can do it at all.
			if err := requireIDMappedMounts(shiftOK); err != nil {
				return idmapPlan{}, neitherMethodError(uid, gid, rawErr)
			}
			plan.Mode = config.IDMapShift
			plan.Warning = fallbackWarning(uid, gid, cfg.Workspace != nil && cfg.Workspace.Owner != nil)
			return plan, nil
		}
		plan.Mode = config.IDMapRaw
	}
	return plan, nil
}

// requireIDMappedMounts refuses shift on a kernel that cannot do it.
//
// Incus fails the mount with "idmapping abilities are required but aren't
// supported on system", which names neither the setting that asked for it nor
// anything to do about it, and does so only after the instance exists.
func requireIDMappedMounts(shiftOK func() (bool, error)) error {
	if shiftOK == nil {
		// Nothing to ask. Letting the run continue leaves Incus to report it,
		// which is what happened before this check existed.
		return nil
	}
	ok, err := shiftOK()
	if err != nil {
		return fmt.Errorf("check whether this host can do idmapped mounts: %w", err)
	}
	if ok {
		return nil
	}
	return fmt.Errorf(
		"workspace idmap (shift) needs idmapped mounts, which this kernel does not have.\n"+
			"WSL is the usual place this comes up.\n\n"+
			"Use 'workspace: {idmap: raw}' instead, which needs an entry in\n"+
			"%s and %s, or 'workspace: {idmap: none}' and handle ownership yourself",
		subUIDPath, subGIDPath)
}

// neitherMethodError says that auto had nothing left to choose.
func neitherMethodError(uid, gid int, rawErr error) error {
	return fmt.Errorf(
		"this host supports neither way of mapping the workspace.\n\n"+
			"raw is not permitted: add 'root:%d:1' to %s and 'root:%d:1' to %s,\n"+
			"then run 'idev up' again (no incus restart needed).\n\n"+
			"shift is not available either: this kernel has no idmapped mounts,\n"+
			"which is usual on WSL.\n\n"+
			"Alternatively set 'workspace: {idmap: none}' and handle ownership yourself\n"+
			"(host files will not be writable from the container).\n\n"+
			"The raw check said: %w",
		uid, subUIDPath, gid, subGIDPath, rawErr)
}

// fallbackWarning says that it fell back to shift, and how to do better.
func fallbackWarning(uid, gid int, ownerDeclared bool) string {
	owner := ""
	if ownerDeclared {
		// It names a container id for raw to map onto, and shift maps every
		// id onto the host id of the same number instead.
		owner = "\n        workspace.owner is not applied under shift."
	}
	return owner + fmt.Sprintf(
		"workspace is mounted with shift (idmapped mount) because raw.idmap is not permitted on this host.\n"+
			"        Files created inside the container will be owned by root on the host.\n"+
			"        To have them owned by you, add 'root:%d:1' to %s and 'root:%d:1' to %s,\n"+
			"        then run 'idev up' again.",
		uid, subUIDPath, gid, subGIDPath)
}
