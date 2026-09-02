package cli

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// snapshotTimeFormat names a snapshot when no name was given.
const snapshotTimeFormat = "20060102-150405"

// checkSnapshotName rejects a name Incus should never be asked to create.
//
// The name reaches the storage driver, and one it cannot handle -- "." is
// enough on btrfs -- leaves a snapshot behind that the instance cannot be
// deleted around: idev can name it, nothing can remove it. Every other
// identifier idev forwards is checked, so this one is too.
//
// The rule is Incus's own (no "/", no whitespace) plus the path elements the
// storage drivers mishandle. Anything stricter would refuse names Incus
// accepts and projects may already be using.
func checkSnapshotName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("a snapshot name is required")
	case name == "." || name == "..":
		return fmt.Errorf("invalid snapshot name %q: it names a directory, "+
			"which leaves a snapshot the instance cannot be deleted around", name)
	case strings.Contains(name, "/"):
		return fmt.Errorf("invalid snapshot name %q: it must not contain %q", name, "/")
	case strings.ContainsFunc(name, unicode.IsSpace):
		return fmt.Errorf("invalid snapshot name %q: it must not contain whitespace", name)
	case strings.ContainsFunc(name, unicode.IsControl):
		return fmt.Errorf("invalid snapshot name %q: it must not contain control characters", name)
	}
	return nil
}

// CreateSnapshot takes a snapshot of the instance, naming it after the current
// time when no name was given.
func (a *App) CreateSnapshot(ctx context.Context, name string) error {
	if _, err := a.managedInstance(ctx, sharedEnvironment); err != nil {
		return err
	}
	if name == "" {
		name = time.Now().Format(snapshotTimeFormat)
	}
	if err := checkSnapshotName(name); err != nil {
		return err
	}

	if err := a.client.CreateSnapshot(ctx, a.instance, name); err != nil {
		return err
	}
	a.log.Info("Created snapshot " + name)

	return nil
}

// ListSnapshots prints the snapshots.
func (a *App) ListSnapshots(ctx context.Context) error {
	if _, err := a.managedInstance(ctx, sharedEnvironment); err != nil {
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
	if _, err := a.managedInstance(ctx, sharedEnvironment); err != nil {
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
	if _, err := a.managedInstance(ctx, sharedEnvironment); err != nil {
		return err
	}
	if err := a.client.DeleteSnapshot(ctx, a.instance, name); err != nil {
		return err
	}
	a.log.Info("Deleted snapshot " + name)

	return nil
}
