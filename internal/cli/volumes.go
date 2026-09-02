package cli

// Volumes: the persistent volumes idev creates, and the record on the
// instance that says which of them are its own.
//
// The record is the only thing that names a volume after it leaves dev.yml,
// so most of what is here is about not losing it (spec 03-configuration.md
// 3.13).

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/incus"
)

// volumeDeleteHint renders advice the user can paste. The record joins pool
// and volume with a slash; the command takes them as two operands.
func volumeDeleteHint(volumes []string) string {
	pool, name, ok := splitVolume(volumes[0])
	if !ok {
		return "'incus storage volume delete <pool> <volume>'"
	}
	hint := fmt.Sprintf("'incus storage volume delete %s %s'", pool, name)
	if len(volumes) > 1 {
		hint += ", and so on"
	}
	return hint
}

// ensureVolumes creates the declared persistent volumes.
func (a *App) ensureVolumes(ctx context.Context) error {
	for _, key := range slices.Sorted(maps.Keys(a.cfg.Volumes)) {
		vol := a.cfg.Volumes[key]
		pool, name := vol.PoolOrDefault(), volumeName(a.instance, key)

		exists, err := a.client.VolumeExists(ctx, pool, name)
		if err != nil {
			return err
		}
		if exists {
			// Said out loud: it may be one an earlier instance left behind,
			// and idev is about to record it as its own to remove.
			a.log.Info(fmt.Sprintf("Using existing volume %s on pool %s", name, pool))
			continue
		}

		a.log.Info(fmt.Sprintf("Creating volume %s on pool %s", name, pool))

		config := map[string]string{}
		if vol.Size != "" {
			config["size"] = vol.Size
		}
		if err := a.client.CreateVolume(ctx, pool, name, config); err != nil {
			return err
		}
	}
	return nil
}

// deleteVolumes deletes the given volumes, named as pool/name.
//
// The list comes from what the instance recorded, not from the declaration, so
// a volume dropped from dev.yml is still reachable.
func (a *App) deleteVolumes(ctx context.Context, refs []string) error {
	for i, ref := range refs {
		pool, name, ok := splitVolume(ref)
		if !ok {
			continue
		}

		exists, err := a.client.VolumeExists(ctx, pool, name)
		switch {
		case errors.Is(err, incus.ErrPoolNotFound):
			// No volume can be on a pool that is not there. Failing here
			// cannot be retried: the instance, and the record naming the
			// rest, are already gone.
			a.log.Debug("skipping " + ref + ": " + err.Error())
			continue
		case err != nil:
			return remainingVolumes(err, refs[i:])
		}
		if !exists {
			continue
		}

		a.log.Info("Deleting volume " + name)
		if err := a.client.DeleteVolume(ctx, pool, name); err != nil {
			return remainingVolumes(err, refs[i:])
		}
	}
	return nil
}

// namedVolumes keeps the record entries that name a volume.
//
// The record can hold an entry that does not -- it is hand-editable -- and
// sending the user after one wastes their time.
func namedVolumes(refs []string) []string {
	var out []string
	for _, ref := range refs {
		if _, _, ok := splitVolume(ref); ok {
			out = append(out, ref)
		}
	}
	return out
}

// remainingVolumes names what a failed cleanup left behind, for the caller
// that knows the instance is already deleted.
//
// The instance carried the only record naming them, so an error that does not
// list them leaves storage no idev command can name again.
func remainingVolumes(err error, refs []string) error {
	named := namedVolumes(refs)
	if len(named) == 0 {
		return err
	}
	return fmt.Errorf("%w\nthe instance is gone, and with it the record naming "+
		"these volume(s), which were not deleted: %s\nremove them with %s",
		err, strings.Join(named, ", "), volumeDeleteHint(named))
}

// volumesUntouched names the volumes when deleting the instance failed, so it
// is not known whether the instance is still there.
//
// Only the wait for the operation is ambiguous: a lookup, a force-stop or a
// rejected request all leave the instance -- and its volumes, still attached
// to it -- exactly where they were. Deleting a volume by hand is what Incus
// refuses in that case, so the first thing to try is the same command again.
func volumesUntouched(err error, refs []string) error {
	named := namedVolumes(refs)
	if len(named) == 0 {
		return err
	}
	return fmt.Errorf("%w\nno volume was deleted: %s\n"+
		"run 'idev destroy --volumes' again; if the instance is already gone, "+
		"remove them with %s",
		err, strings.Join(named, ", "), volumeDeleteHint(named))
}

// volumesLeftUnnameable names the volumes that only the instance's own record
// knew about, for a destroy whose wait was abandoned while the daemon went on
// deleting.
//
// Said only for that one failure -- the others leave the instance, its record
// and its volumes where they were. It does not claim the instance is gone,
// because nothing here knows: that is the whole content of
// incus.ErrOutcomeUnknown. So it gives the user the step that settles it
// before the one that deletes anything.
func volumesLeftUnnameable(err error, instance string, refs []string) error {
	named := namedVolumes(refs)
	if len(named) == 0 {
		return err
	}
	return fmt.Errorf("%w\nif the instance did go, nothing names these again: %s\n"+
		"check with 'incus list %s', then remove them with %s",
		err, strings.Join(named, ", "), instance, volumeDeleteHint(named))
}

// adoptCarried folds a record rebuild is carrying into the instance it found.
//
// Rebuild holds the record in memory while the instance that held it is gone.
// Reaching an instance that exists gives it somewhere to live again -- but it
// is still only in memory until the write lands, so the caller stops carrying
// it after that and not before.
func (a *App) adoptCarried(inst *incus.Instance) {
	if len(a.carried) == 0 {
		return
	}
	// Config is never nil here: the caller reached this by finding idev's own
	// project marker in it.
	merged := knownVolumes(inst.Config, a.cfg, a.instance)
	for _, ref := range a.carried {
		if !slices.Contains(merged, ref) {
			merged = append(merged, ref)
		}
	}
	slices.Sort(merged)
	inst.Config[managedVolumesKey] = strings.Join(merged, ",")
}

// recordLostWith names the carried volumes when the create half of a rebuild
// failed, because the record naming them went with the old instance.
//
// The declared ones the next up adopts by name; the rest are reachable only
// through this message.
func (a *App) recordLostWith(err error) error {
	named := namedVolumes(undeclaredVolumes(a.cfg, a.instance, a.carried))
	if len(named) == 0 {
		return err
	}
	return fmt.Errorf("%w\nthe instance holding the volume record is already gone, "+
		"so nothing names these again: %s\n"+
		"keep them for the next 'idev up' by hand, or remove them with %s",
		err, strings.Join(named, ", "), volumeDeleteHint(named))
}

// pruneVolumeRecord drops from the record the volumes that are no longer on
// the pool, and anything not in the pool/name form.
//
// Without it the record only ever grows, so a warning about a volume outlives
// the volume: deleting one by hand would leave up complaining about it for
// good.
func (a *App) pruneVolumeRecord(ctx context.Context, inst *incus.Instance) {
	recorded, ok := inst.Config[managedVolumesKey]
	if !ok {
		return
	}

	declared := declaredVolumes(a.cfg, a.instance)

	var kept []string
	for _, ref := range splitList(recorded) {
		pool, name, ok := splitVolume(ref)
		if !ok {
			continue
		}
		// A declared volume is never a stale record, so there is nothing to
		// ask the daemon. up has already created it by the time this runs,
		// and the preview reaches here saying what up would do, which is to
		// create it. Asking anyway costs a round trip per declared volume in
		// both, and would have the preview show up dropping a record up keeps.
		//
		// It is kept rather than skipped for what this function says it does;
		// desiredConfig starts the record from the declared list either way.
		if slices.Contains(declared, ref) {
			kept = append(kept, ref)
			continue
		}
		exists, err := a.client.VolumeExists(ctx, pool, name)
		switch {
		case errors.Is(err, incus.ErrPoolNotFound):
			// The pool has no row, so nothing it names can exist. Keeping the
			// record would warn about the volume on every run, for good, and
			// point at a pool Incus says is not there.
			a.log.Debug("dropping " + ref + ": " + err.Error())
			continue
		case err != nil:
			// Unreachable rather than gone. Nothing declared needs this
			// volume, so refusing to run would block up over a record that is
			// only there to be tidied.
			a.log.Debug("could not check " + ref + ": " + err.Error())
			kept = append(kept, ref)
			continue
		}
		if exists {
			kept = append(kept, ref)
		}
	}
	inst.Config[managedVolumesKey] = strings.Join(kept, ",")
}

// warnVolumesDropped says so when a volume idev created has left the
// declaration.
//
// Nothing names it any more, so the data would sit on the pool with no way to
// reach it (spec 03-configuration.md 3.13).
func (a *App) warnVolumesDropped(inst *incus.Instance) {
	declared := declaredVolumes(a.cfg, a.instance)

	var dropped []string
	for _, ref := range splitList(inst.Config[managedVolumesKey]) {
		if !slices.Contains(declared, ref) {
			dropped = append(dropped, ref)
		}
	}
	if len(dropped) == 0 {
		return
	}
	// 'idev destroy --volumes' would reach them, but it takes the instance and
	// every other volume with it, so it is not the advice to give here.
	a.log.Warn(fmt.Sprintf(
		"volume(s) no longer declared, and their data is kept: %s\n"+
			"                declare them again, or remove one with %s",
		strings.Join(dropped, ", "), volumeDeleteHint(dropped)))
}
