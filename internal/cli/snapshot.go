package cli

import (
	"context"
	"fmt"
	"time"
)

// snapshotTimeFormat は名前を省略した場合のスナップショット名。
const snapshotTimeFormat = "20060102-150405"

// CreateSnapshot はinstanceのスナップショットを作成する。
// 名前を省略した場合は日時から自動で付ける。
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

// ListSnapshots はスナップショット一覧を表示する。
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

// RestoreSnapshot はinstanceをスナップショットの状態へ戻す。
//
// instance内の現在の状態は失われる。ホスト側のworkspaceには影響しない。
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

// DeleteSnapshot はスナップショットを削除する。
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
