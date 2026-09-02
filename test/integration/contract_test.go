//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/incus/contract"
)

// The same contract the fake is held to, against the real daemon.
//
// A fake nobody checks is a belief nobody checks. Both runs assert the same
// things, so a divergence fails one of them rather than sitting in the fake
// waiting to pass a wrong unit test (spec 08-testing.md 8.3.1).
func TestIncusMeetsTheClientContract(t *testing.T) {
	requireIncus(t)

	client, err := incus.Connect(context.Background(), incus.Target{Project: "default"})
	if err != nil {
		t.Fatalf("connect to Incus: %v", err)
	}

	name := fmt.Sprintf("dev-contract-%d", time.Now().UnixNano()%1e9)
	t.Cleanup(func() { _, _ = runIncus("delete", "--force", name) })

	// The same independent witness the fake's suite has. Run reports on
	// itself, so a rewrite of it could claim a full contract while doing
	// nothing; a recorder in front of the real client cannot be fooled that
	// way, because the calls have to reach the daemon.
	recorder := &recordingClient{client: client}

	ran := contract.Run(t, contract.Env{
		Client:       recorder,
		Instance:     name,
		Image:        testImage,
		Pool:         "default",
		MissingPool:  "idev-contract-no-such-pool",
		Profile:      "default",
		RunsPrograms: true,
	})

	// Gutting Run would otherwise pass here and in the fake's suite alike,
	// which is the trap the contract exists to close.
	if err := contract.Verify(ran); err != nil {
		t.Error(err)
	}
	if contract.Filtered() {
		return
	}
	// Every operation, matching the fake's witness: an operation the contract
	// never exercises is one where the two can disagree unnoticed.
	for _, want := range []string{
		"Instance", "ListInstances", "CreateInstance", "StartInstance",
		"StopInstance", "DeleteInstance", "UpdateInstance",
		"ProfileExists", "CheckImage",
		"VolumeExists", "CreateVolume", "DeleteVolume",
		"CreateSnapshot", "Snapshots", "RestoreSnapshot", "DeleteSnapshot",
		"Exec", "WaitReady",
	} {
		if !slices.Contains(recorder.calls, want) {
			t.Errorf("the daemon was never asked to %s; the contract did not run", want)
		}
	}
}

// recordingClient notes which operations reached the daemon.
//
// It does not embed incus.Client. Embedding would let a method added to the
// interface be satisfied by promotion and recorded by nothing, so the witness
// would quietly stop covering it -- and spec 08-testing.md 8.3.2's rule that
// a new Client method joins the contract would go unenforced. Written out,
// the compiler enforces it.
type recordingClient struct {
	client incus.Client
	calls  []string
}

var _ incus.Client = (*recordingClient)(nil)

func (c *recordingClient) note(op string) { c.calls = append(c.calls, op) }

func (c *recordingClient) CreateInstance(ctx context.Context, spec incus.InstanceSpec) error {
	c.note("CreateInstance")
	return c.client.CreateInstance(ctx, spec)
}

func (c *recordingClient) StartInstance(ctx context.Context, name string) error {
	c.note("StartInstance")
	return c.client.StartInstance(ctx, name)
}

func (c *recordingClient) DeleteInstance(ctx context.Context, name string) error {
	c.note("DeleteInstance")
	return c.client.DeleteInstance(ctx, name)
}

func (c *recordingClient) CreateVolume(ctx context.Context, pool, name string, config map[string]string) error {
	c.note("CreateVolume")
	return c.client.CreateVolume(ctx, pool, name, config)
}

func (c *recordingClient) DeleteVolume(ctx context.Context, pool, name string) error {
	c.note("DeleteVolume")
	return c.client.DeleteVolume(ctx, pool, name)
}

func (c *recordingClient) CreateSnapshot(ctx context.Context, instance, snapshot string) error {
	c.note("CreateSnapshot")
	return c.client.CreateSnapshot(ctx, instance, snapshot)
}

func (c *recordingClient) UpdateInstance(ctx context.Context, name string, change incus.InstanceChange, etag string) error {
	c.note("UpdateInstance")
	return c.client.UpdateInstance(ctx, name, change, etag)
}

func (c *recordingClient) Exec(ctx context.Context, name string, argv []string, opt incus.ExecOptions) (int, error) {
	c.note("Exec")
	return c.client.Exec(ctx, name, argv, opt)
}

func (c *recordingClient) Instance(ctx context.Context, name string) (*incus.Instance, error) {
	c.note("Instance")
	return c.client.Instance(ctx, name)
}

func (c *recordingClient) ListInstances(ctx context.Context) ([]incus.Instance, error) {
	c.note("ListInstances")
	return c.client.ListInstances(ctx)
}

func (c *recordingClient) StopInstance(ctx context.Context, name string) error {
	c.note("StopInstance")
	return c.client.StopInstance(ctx, name)
}

func (c *recordingClient) ProfileExists(ctx context.Context, name string) (bool, error) {
	c.note("ProfileExists")
	return c.client.ProfileExists(ctx, name)
}

func (c *recordingClient) CheckImage(ctx context.Context, ref string) error {
	c.note("CheckImage")
	return c.client.CheckImage(ctx, ref)
}

func (c *recordingClient) VolumeExists(ctx context.Context, pool, name string) (bool, error) {
	c.note("VolumeExists")
	return c.client.VolumeExists(ctx, pool, name)
}

func (c *recordingClient) Snapshots(ctx context.Context, instance string) ([]incus.Snapshot, error) {
	c.note("Snapshots")
	return c.client.Snapshots(ctx, instance)
}

func (c *recordingClient) RestoreSnapshot(ctx context.Context, instance, snapshot string) error {
	c.note("RestoreSnapshot")
	return c.client.RestoreSnapshot(ctx, instance, snapshot)
}

func (c *recordingClient) DeleteSnapshot(ctx context.Context, instance, snapshot string) error {
	c.note("DeleteSnapshot")
	return c.client.DeleteSnapshot(ctx, instance, snapshot)
}

func (c *recordingClient) WaitReady(ctx context.Context, name string, opt incus.WaitOptions) error {
	c.note("WaitReady")
	return c.client.WaitReady(ctx, name, opt)
}
