package cli

// Restarts: the config keys Incus reads only at boot, and the record that
// remembers one is owed.
//
// idev does not restart an instance unless asked: something is running in
// there. So a change that needs one is carried in a record until a restart
// happens, by idev or by anyone else (spec 05-incus.md 5.4.5).

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lambdasakura/incus-dev/internal/incus"
)

// settleRestart deals with changes that need a restart (spec 05-incus.md
// 5.4.5).
//
// By default it only warns. Restarting happens only when asked for explicitly,
// so nothing the user was running is stopped unexpectedly.
func (a *App) settleRestart(ctx context.Context, running bool, lastStart time.Time, before, desired map[string]string, unset []string, opt UpOptions) error {
	if !running {
		// It is about to be started, which applies everything. Nothing is
		// owed, including anything an earlier run left owed.
		return a.clearRestartPending(ctx, before)
	}

	fresh, changed, booted := restartOwed(running, before, desired, unset, lastStart)

	if len(changed) == 0 {
		// Nothing owed. A record left by an earlier run was retired by a
		// restart idev did not do, so it goes.
		return a.clearRestartPending(ctx, before)
	}

	if !opt.Restart {
		a.log.Warn(restartWarning(fresh, changed))

		record := recordRestart(lastStart, booted)
		if before[managedRestartKey] == record {
			return nil
		}
		return a.changedUnderfoot(a.client.UpdateInstance(ctx, a.instance,
			incus.InstanceChange{SetConfig: map[string]string{managedRestartKey: record}}, ""))
	}

	a.log.Info("Restarting instance to apply " + strings.Join(changed, ", "))
	if err := a.client.StopInstance(ctx, a.instance); err != nil {
		return err
	}
	if err := a.client.StartInstance(ctx, a.instance); err != nil {
		return err
	}
	return a.clearRestartPending(ctx, before)
}

// pendingRestart returns the keys an earlier run left waiting on a restart,
// and nothing has restarted since.
//
// The record carries the instance's start time as it was when the change was
// applied. A later start — by the user, or by the host coming back up —
// applied it, so there is nothing left to warn about.
// bootedValue is what the running container has for a pending key.
//
// known is false for a record written before the value was stored: such a key
// is owed a restart, and nothing about the declaration can retire it, because
// there is nothing to compare against.
type bootedValue struct {
	value string
	known bool
}

func pendingRestart(before map[string]string, lastStart time.Time) map[string]bootedValue {
	at, entries, ok := strings.Cut(before[managedRestartKey], "|")
	if !ok {
		return nil
	}
	recorded, err := time.Parse(time.RFC3339Nano, at)
	if err != nil || lastStart.After(recorded) {
		// A restart since then applied everything, so nothing is owed.
		return nil
	}

	out := map[string]bootedValue{}
	for _, entry := range splitList(entries) {
		// key=value since this format carries the booted value. An entry
		// without one was written by an older idev, which recorded only that
		// a restart was owed.
		key, value, ok := strings.Cut(entry, "=")
		out[key] = bootedValue{value: value, known: ok}
	}
	return out
}

// restartOwed returns what this run changed and needs a restart for, and the
// full set including what an earlier run changed and nothing has restarted
// since.
//
// The preview computes it the same way, so it can say the same thing up will.
func restartOwed(running bool, before, desired map[string]string, unset []string, lastStart time.Time) (fresh, all []string, owedValues map[string]bootedValue) {
	if !running {
		return nil, nil, nil
	}

	// What the running container actually booted with. An earlier run recorded
	// it for the keys it changed; for the rest it is what is stored, since
	// nothing has changed them since the instance started.
	booted := pendingRestart(before, lastStart)

	owed := map[string]bootedValue{}
	for _, k := range incus.RestartRequiredKeys {
		was, recorded := booted[k]
		if !recorded {
			was = bootedValue{value: before[k], known: true}
		}

		want, declared := desired[k]
		switch {
		case declared:
		case slices.Contains(unset, k):
			want = ""
		default:
			// Neither declared nor being unset: the stored value stays as it
			// is, and a restart is owed if the container is not running with
			// it -- an earlier run changed it and nothing has restarted since.
			want = before[k]
		}
		// Same text is no change for any key; raw.idmap is additionally the
		// one key where two spellings mean the same mapping.
		unchanged := want == was.value ||
			(k == incus.IDMapKey && incus.SameIDMapping(want, was.value))
		if was.known && unchanged {
			// The container is already running with this. A value changed and
			// changed back is not a change, and restarting would apply
			// nothing while killing whatever is running inside.
			continue
		}
		owed[k] = was
		all = append(all, k)
		if !recorded || before[k] != want {
			fresh = append(fresh, k)
		}
	}

	slices.Sort(fresh)
	slices.Sort(all)
	return fresh, all, owed
}

// restartWarning renders what is owed. Two wordings, because "changed" is
// untrue on a run that changed nothing and is only carrying an earlier one
// forward.
func restartWarning(fresh, all []string) string {
	if len(fresh) > 0 {
		return fmt.Sprintf(
			"%s changed but the instance is running; restart it to apply (idev up --restart)",
			strings.Join(all, ", "))
	}
	return fmt.Sprintf("%s is still waiting on a restart (idev up --restart)",
		strings.Join(all, ", "))
}

// recordRestart renders the record: when the change was applied, and to what.
func recordRestart(lastStart time.Time, booted map[string]bootedValue) string {
	entries := make([]string, 0, len(booted))
	for _, k := range slices.Sorted(maps.Keys(booted)) {
		if b := booted[k]; b.known {
			entries = append(entries, k+"="+b.value)
		} else {
			// Keep it unknown rather than inventing a value that would let a
			// later run decide the restart is no longer owed.
			entries = append(entries, k)
		}
	}
	return lastStart.Format(time.RFC3339Nano) + "|" + strings.Join(entries, ",")
}

// clearRestartPending drops the record that a restart is owed.
func (a *App) clearRestartPending(ctx context.Context, before map[string]string) error {
	if _, ok := before[managedRestartKey]; !ok {
		return nil
	}
	return a.changedUnderfoot(a.client.UpdateInstance(ctx, a.instance,
		incus.InstanceChange{UnsetConfig: []string{managedRestartKey}}, ""))
}

// warnRestartNeeded says what up would say about a restart: one warning, on
// the same stream, covering both what this run would change and what an
// earlier one left owed.
func (a *App) warnRestartNeeded(inst *incus.Instance, plan idmapPlan) {
	if !inst.IsRunning() {
		return
	}
	desired := desiredConfig(a.cfg, plan, inst.Config, a.instance)
	fresh, all, _ := restartOwed(true, inst.Config, desired,
		staleConfigKeys(inst.Config, desired, plan), inst.LastUsedAt)
	if len(all) == 0 {
		return
	}
	a.log.Warn(restartWarning(fresh, all))
}
