package cli

import (
	"context"
	"fmt"
	"time"
)

// snapshotTimeFormat names a snapshot when no name was given.
const snapshotTimeFormat = "20060102-150405"

// CreateSnapshot takes a snapshot of the instance, naming it after the current
// time when no name was given.
func (a *App) CreateSnapshot(ctx context.Context, name string) error {
	if _, err := a.managedInstance(ctx); err != nil {
		return err
	}
	if name == "" {
		name = time.Now().Format(snapshotTimeFormat)
	}

	if err := a.client.CreateSnapshot(ctx, a.instance, name); err != nil {
		return err
	}
	a.log.Info("Created snapshot " + name)

	return nil
}

// ListSnapshots prints the snapshots.
func (a *App) ListSnapshots(ctx context.Context) error {
	if _, err := a.managedInstance(ctx); err != nil {
		return err
	}

	snapshots, err := a.client.Snapshots(ctx, a.instance)
	if err != nil {
		return err
	}
	if len(snapshots) == 0 {
		_, err := fmt.Fprintln(a.out, "no snapshots")
		return err
	}

	for _, s := range snapshots {
		created := ""
		if !s.CreatedAt.IsZero() {
			created = "\t" + s.CreatedAt.Local().Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(a.out, "%s%s\n", s.Name, created); err != nil {
			return err
		}
	}
	return nil
}

// RestoreSnapshot rolls the instance back to a snapshot.
//
// Its current state is lost. The workspace on the host is unaffected.
func (a *App) RestoreSnapshot(ctx context.Context, name string) error {
	if _, err := a.managedInstance(ctx); err != nil {
		return err
	}

	a.log.Info("Restoring snapshot " + name)
	if err := a.client.RestoreSnapshot(ctx, a.instance, name); err != nil {
		return err
	}
	a.log.Info("Restored. The workspace on the host is untouched")

	return nil
}

// DeleteSnapshot deletes a snapshot.
func (a *App) DeleteSnapshot(ctx context.Context, name string) error {
	if _, err := a.managedInstance(ctx); err != nil {
		return err
	}
	if err := a.client.DeleteSnapshot(ctx, a.instance, name); err != nil {
		return err
	}
	a.log.Info("Deleted snapshot " + name)

	return nil
}
