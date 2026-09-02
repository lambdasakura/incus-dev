// Package contract holds the behaviour every incus.Client implementation has
// to have, as one set of assertions run against each of them.
//
// It exists because the fake and the belief it encodes were the same mistake
// written twice. Fifteen rounds of review produced 21 fixes, and the ones that
// escaped every unit test were all wrong beliefs about Incus: a 404 means the
// object is missing (it also means the project or the pool is), creating a
// volume that exists is fine (Incus refuses), a snapshot may be named anything
// (some names leave the instance undeletable). The fake agreed with each
// belief, so the test agreed too.
//
// Run returns the names of the checks it ran, so a caller can assert that the
// contract was actually exercised. Without that, gutting Run passes both
// suites -- the same "the test agrees with the mistake" trap one layer up.
package contract

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/incus"
)

// Env is what a suite needs from whoever runs it.
type Env struct {
	// Client is the implementation under test.
	Client incus.Client
	// Instance is a name no test may collide on. The suite creates and
	// deletes it.
	Instance string
	// Image is what to create that instance from.
	Image string
	// Pool is a storage pool that exists.
	Pool string
	// MissingPool is a storage pool name that does not exist.
	MissingPool string
	// Profile is a profile that exists.
	Profile string
	// RunsPrograms says whether Exec really runs the argv it is given.
	//
	// A fake cannot, and pretending otherwise would mean asserting nothing.
	// It gates the program's own output and exit code, and nothing else:
	// what Exec does about an instance that is absent or stopped is
	// contracted for both.
	RunsPrograms bool
}

// Run executes the contract against env.Client.
//
// Every assertion holds for the real daemon, so a fake that fails one is
// wrong about Incus -- which is the only thing this package is for.
func Run(t *testing.T, env Env) []string {
	t.Helper()

	var ran []string
	// Recorded from inside the subtest, not beside it: counting the
	// registrations would leave "delete the t.Run and keep the append" as a
	// way to run nothing and still report a full contract.
	check := func(name string, fn func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			ran = append(ran, name)
			fn(t)
		})
	}

	check("Instance reports a missing instance as such", func(t *testing.T) {
		_, err := env.Client.Instance(context.Background(), env.Instance+"-nope")
		if !errors.Is(err, incus.ErrInstanceNotFound) {
			t.Errorf("Instance() error = %v, want ErrInstanceNotFound", err)
		}
	})

	check("VolumeExists is false for a volume that is not there", func(t *testing.T) {
		exists, err := env.Client.VolumeExists(context.Background(), env.Pool, env.Instance+"-nope")
		if err != nil {
			t.Fatalf("VolumeExists() error = %v", err)
		}
		if exists {
			t.Error("VolumeExists() = true for a volume that was never created")
		}
	})

	// The distinction idev got wrong: a pool with no row holds nothing, which
	// is a reason to drop a record and a reason to refuse to create -- not the
	// same answer as a volume that is merely absent.
	check("VolumeExists reports a missing pool rather than a missing volume", func(t *testing.T) {
		_, err := env.Client.VolumeExists(context.Background(), env.MissingPool, "anything")
		if !errors.Is(err, incus.ErrPoolNotFound) {
			t.Errorf("VolumeExists() error = %v, want ErrPoolNotFound", err)
		}
	})

	check("CreateVolume refuses a name that is already taken", func(t *testing.T) {
		name := env.Instance + "-contract"
		ctx := context.Background()

		if err := env.Client.CreateVolume(ctx, env.Pool, name, nil); err != nil {
			t.Fatalf("CreateVolume() error = %v", err)
		}
		t.Cleanup(func() { _ = env.Client.DeleteVolume(ctx, env.Pool, name) })

		if err := env.Client.CreateVolume(ctx, env.Pool, name, nil); err == nil {
			t.Error("CreateVolume() = nil error for a volume that already exists")
		}

		exists, err := env.Client.VolumeExists(ctx, env.Pool, name)
		if err != nil || !exists {
			t.Errorf("VolumeExists() = %v, %v; want true, nil after CreateVolume", exists, err)
		}
	})

	check("DeleteVolume refuses a volume that is not there", func(t *testing.T) {
		err := env.Client.DeleteVolume(context.Background(), env.Pool, env.Instance+"-nope")
		if err == nil {
			t.Error("DeleteVolume() = nil error for a volume that does not exist")
		}
	})

	check("CreateVolume refuses a pool that is not there", func(t *testing.T) {
		err := env.Client.CreateVolume(context.Background(), env.MissingPool, "anything", nil)
		if err == nil {
			t.Error("CreateVolume() = nil error on a pool that does not exist")
		}
	})

	check("the host answers the same way twice about idmapped mounts", func(t *testing.T) {
		// What a host can do is the host's business, so the contract cannot
		// say which answer is right. What it can hold is that both
		// implementations answer at all, and answer consistently: idev
		// decides between raw and shift on this, and a value that moved
		// between two runs would send the same project down both paths.
		ctx := context.Background()

		first, err := env.Client.SupportsIDMappedMounts(ctx)
		if err != nil {
			t.Fatalf("SupportsIDMappedMounts() error = %v", err)
		}
		again, err := env.Client.SupportsIDMappedMounts(ctx)
		if err != nil {
			t.Fatalf("second SupportsIDMappedMounts() error = %v", err)
		}
		if first != again {
			t.Errorf("SupportsIDMappedMounts() = %v then %v", first, again)
		}
	})

	check("ProfileNames lists what is there and nothing else", func(t *testing.T) {
		names, err := env.Client.ProfileNames(context.Background())
		if err != nil {
			t.Fatalf("ProfileNames() error = %v", err)
		}
		if !slices.Contains(names, env.Profile) {
			t.Errorf("ProfileNames() = %v, want it to contain %q", names, env.Profile)
		}
		if slices.Contains(names, "idev-contract-nope") {
			t.Errorf("ProfileNames() = %v, want no profile that was never created", names)
		}
	})

	check("CheckImage rejects a reference that does not resolve", func(t *testing.T) {
		if err := env.Client.CheckImage(context.Background(), "images:no/such/image"); err == nil {
			t.Error("CheckImage() = nil error for an image that does not exist")
		}
	})

	return append(ran, runInstanceContract(t, env)...)
}

// Checks is how many checks Run performs, and Critical names the ones whose
// absence has caused a real defect here. Both callers assert on them, so
// gutting Run means editing three files in two packages rather than one.
//
// A check deleted and replaced in the same edit keeps the count, which no
// cheap guard catches -- but that is a deliberate act, not an oversight.
const Checks = 36

// Critical are the checks that exist because the fake once disagreed with
// Incus and a defect reached a user through the gap.
var Critical = []string{
	"VolumeExists reports a missing pool rather than a missing volume",
	"CreateVolume refuses a name that is already taken",
	"UpdateInstance replaces a device rather than merging",
	"snapshots round-trip and a duplicate name is refused",
	"a delete cut short reports that the outcome is unknown",
	"Instance returns a detached copy",
	"a write against a stale reading is refused",
	"Exec refuses an instance that is not there",
	"RestoreSnapshot puts back what the snapshot held",
}

// Filtered reports whether -run narrowed the run to some of the subtests.
//
// The whole-contract assertions cannot hold then, and failing would bury the
// check the reader was narrowing to.
func Filtered() bool {
	f := flag.Lookup("test.run")
	if f == nil {
		return false
	}
	// The part after the first "/" is the subtest pattern. An empty one
	// matches every subtest, so it narrows nothing -- and treating it as a
	// filter is how this guard would stop guarding.
	_, subtest, ok := strings.Cut(f.Value.String(), "/")
	return ok && strings.TrimSpace(subtest) != ""
}

// Verify reports what is wrong with the checks Run performed, or nil.
//
// Both suites call it, so the assertion cannot drift between them.
func Verify(ran []string) error {
	if Filtered() {
		return nil
	}
	if len(ran) != Checks {
		return fmt.Errorf("ran %d checks, want %d; if you added or removed one, "+
			"update contract.Checks rather than this assertion:\n  %s",
			len(ran), Checks, strings.Join(ran, "\n  "))
	}
	seen := map[string]bool{}
	for _, name := range ran {
		if seen[name] {
			return fmt.Errorf("two checks are named %q", name)
		}
		seen[name] = true
	}
	for _, name := range Critical {
		if !seen[name] {
			return fmt.Errorf("the check %q is gone; it is there because its "+
				"absence let a defect reach a user", name)
		}
	}
	return nil
}

// runInstanceContract covers what needs an instance to exist.
func runInstanceContract(t *testing.T, env Env) []string {
	t.Helper()

	var ran []string
	// Recorded from inside the subtest, not beside it: counting the
	// registrations would leave "delete the t.Run and keep the append" as a
	// way to run nothing and still report a full contract.
	check := func(name string, fn func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			ran = append(ran, name)
			fn(t)
		})
	}

	ctx := context.Background()
	spec := incus.InstanceSpec{
		Name:   env.Instance,
		Image:  env.Image,
		Config: map[string]string{"user.incus-dev.project": "contract"},
	}
	if err := env.Client.CreateInstance(ctx, spec); err != nil {
		t.Fatalf("CreateInstance() error = %v", err)
	}
	t.Cleanup(func() { _ = env.Client.DeleteInstance(ctx, env.Instance) })

	check("CreateInstance refuses a name that is already taken", func(t *testing.T) {
		err := env.Client.CreateInstance(ctx, spec)
		if err == nil {
			t.Fatal("CreateInstance() = nil error for an instance that already exists")
		}
		// The sentinel, not just an error: idev tells the user another run is
		// creating it, and asserting only non-nil let a matcher that never
		// fires pass on both implementations.
		if !errors.Is(err, incus.ErrInstanceExists) {
			t.Errorf("error = %v, want ErrInstanceExists", err)
		}
	})

	check("Instance carries back what was set", func(t *testing.T) {
		inst, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatalf("Instance() error = %v", err)
		}
		if inst.Name != env.Instance {
			t.Errorf("Name = %q, want %q", inst.Name, env.Instance)
		}
		if inst.Config["user.incus-dev.project"] != "contract" {
			t.Errorf("Config = %v, want the marker set at creation", inst.Config)
		}
		// A freshly created instance is not running until it is started.
		if inst.IsRunning() {
			t.Error("a created instance reports Running before it was started")
		}
	})

	check("ListInstances includes it, with its config", func(t *testing.T) {
		all, err := env.Client.ListInstances(ctx)
		if err != nil {
			t.Fatalf("ListInstances() error = %v", err)
		}
		for _, inst := range all {
			if inst.Name != env.Instance {
				continue
			}
			if inst.Config["user.incus-dev.project"] != "contract" {
				t.Errorf("Config = %v, want the markers carried", inst.Config)
			}
			return
		}
		t.Errorf("ListInstances() = %d instances, none of them %q", len(all), env.Instance)
	})

	check("UpdateInstance sets and unsets, and both round-trip", func(t *testing.T) {
		if err := env.Client.UpdateInstance(ctx, env.Instance, incus.InstanceChange{SetConfig: map[string]string{"limits.cpu": "1"}, UnsetConfig: nil}, ""); err != nil {
			t.Fatalf("UpdateInstance() error = %v", err)
		}
		inst, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatal(err)
		}
		if inst.Config["limits.cpu"] != "1" {
			t.Errorf("limits.cpu = %q, want 1", inst.Config["limits.cpu"])
		}

		if err := env.Client.UpdateInstance(ctx, env.Instance, incus.InstanceChange{SetConfig: nil, UnsetConfig: []string{"limits.cpu"}}, ""); err != nil {
			t.Fatalf("UpdateInstance() error = %v", err)
		}
		inst, err = env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := inst.Config["limits.cpu"]; ok {
			t.Errorf("limits.cpu = %q, want it gone", inst.Config["limits.cpu"])
		}
	})

	// UpdateInstance replaces the named device rather than merging into it,
	// which is what makes the managed-devices record the only way to remove
	// one. idev depended on this without checking it.
	check("UpdateInstance replaces a device rather than merging", func(t *testing.T) {
		first := map[string]incus.Device{
			"extra": {"type": "disk", "source": "/tmp", "path": "/one", "readonly": "true"},
		}
		if err := env.Client.UpdateInstance(ctx, env.Instance, incus.InstanceChange{SetDevices: first}, ""); err != nil {
			t.Fatalf("UpdateInstance() error = %v", err)
		}

		second := map[string]incus.Device{
			"extra": {"type": "disk", "source": "/tmp", "path": "/two"},
		}
		if err := env.Client.UpdateInstance(ctx, env.Instance, incus.InstanceChange{SetDevices: second}, ""); err != nil {
			t.Fatalf("UpdateInstance() error = %v", err)
		}

		inst, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatal(err)
		}
		got := inst.Devices["extra"]
		if got["path"] != "/two" {
			t.Errorf("path = %q, want the second value", got["path"])
		}
		if _, ok := got["readonly"]; ok {
			t.Errorf("device = %v, want readonly gone rather than merged", got)
		}
	})

	check("UpdateInstance removes a device", func(t *testing.T) {
		if err := env.Client.UpdateInstance(ctx, env.Instance, incus.InstanceChange{RemoveDevices: []string{"extra"}}, ""); err != nil {
			t.Fatalf("UpdateInstance() error = %v", err)
		}
		inst, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := inst.Devices["extra"]; ok {
			t.Error("the device is still attached")
		}
	})

	check("a snapshot name Incus refuses is refused", func(t *testing.T) {
		// idev checks the name before asking, so this is about what Incus
		// itself will not take: a name with a slash or a space.
		for _, name := range []string{"a/b", "a b"} {
			if err := env.Client.CreateSnapshot(ctx, env.Instance, name); err == nil {
				_ = env.Client.DeleteSnapshot(ctx, env.Instance, name)
				t.Errorf("CreateSnapshot(%q) = nil error, want it refused", name)
			}
		}
	})

	check("snapshots round-trip and a duplicate name is refused", func(t *testing.T) {
		const name = "contract-snap"

		if err := env.Client.CreateSnapshot(ctx, env.Instance, name); err != nil {
			t.Fatalf("CreateSnapshot() error = %v", err)
		}
		t.Cleanup(func() { _ = env.Client.DeleteSnapshot(ctx, env.Instance, name) })

		snaps, err := env.Client.Snapshots(ctx, env.Instance)
		if err != nil {
			t.Fatalf("Snapshots() error = %v", err)
		}
		var found bool
		for _, s := range snaps {
			if s.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("Snapshots() = %v, want it to include %q", snaps, name)
		}

		err = env.Client.CreateSnapshot(ctx, env.Instance, name)
		if err == nil {
			t.Fatal("CreateSnapshot() = nil error for a name that already exists")
		}
		if !errors.Is(err, incus.ErrSnapshotExists) {
			t.Errorf("error = %v, want ErrSnapshotExists", err)
		}
	})

	check("RestoreSnapshot puts back what the snapshot held", func(t *testing.T) {
		const name = "contract-restore"

		if err := env.Client.UpdateInstance(ctx, env.Instance,
			incus.InstanceChange{SetConfig: map[string]string{"user.contract": "before"}}, ""); err != nil {
			t.Fatalf("UpdateInstance() error = %v", err)
		}
		if err := env.Client.CreateSnapshot(ctx, env.Instance, name); err != nil {
			t.Fatalf("CreateSnapshot() error = %v", err)
		}
		t.Cleanup(func() { _ = env.Client.DeleteSnapshot(ctx, env.Instance, name) })

		if err := env.Client.UpdateInstance(ctx, env.Instance,
			incus.InstanceChange{SetConfig: map[string]string{"user.contract": "after"}}, ""); err != nil {
			t.Fatalf("UpdateInstance() error = %v", err)
		}
		if err := env.Client.RestoreSnapshot(ctx, env.Instance, name); err != nil {
			t.Fatalf("RestoreSnapshot() error = %v", err)
		}

		inst, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatal(err)
		}
		if got := inst.Config["user.contract"]; got != "before" {
			t.Errorf("user.contract = %q, want the snapshot's value back", got)
		}
	})

	check("RestoreSnapshot refuses one that is not there", func(t *testing.T) {
		if err := env.Client.RestoreSnapshot(ctx, env.Instance, "contract-nope"); err == nil {
			t.Error("RestoreSnapshot() = nil error for a snapshot that does not exist")
		}
	})

	check("DeleteSnapshot refuses one that is not there", func(t *testing.T) {
		if err := env.Client.DeleteSnapshot(ctx, env.Instance, "contract-nope"); err == nil {
			t.Error("DeleteSnapshot() = nil error for a snapshot that does not exist")
		}
	})

	check("starting makes it runnable", func(t *testing.T) {
		if err := env.Client.StartInstance(ctx, env.Instance); err != nil {
			t.Fatalf("StartInstance() error = %v", err)
		}
		if err := env.Client.WaitReady(ctx, env.Instance, incus.WaitOptions{}); err != nil {
			t.Fatalf("WaitReady() error = %v", err)
		}

		inst, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatal(err)
		}
		if !inst.IsRunning() {
			t.Errorf("Status = %q, want it running after StartInstance", inst.Status)
		}
	})

	// Both implementations can honour this, so both are held to it.
	check("Exec refuses an instance that is not there", func(t *testing.T) {
		_, err := env.Client.Exec(ctx, env.Instance+"-nope", []string{"true"}, incus.ExecOptions{})
		if !errors.Is(err, incus.ErrInstanceNotFound) {
			t.Errorf("Exec() error = %v, want ErrInstanceNotFound", err)
		}
	})

	check("Exec carries the command's output and exit code", func(t *testing.T) {
		if !env.RunsPrograms {
			t.Skip("this implementation does not run programs")
		}

		var out strings.Builder
		code, err := env.Client.Exec(ctx, env.Instance,
			[]string{"sh", "-c", "echo contract; exit 7"},
			incus.ExecOptions{Stdout: &out, Stderr: &out})
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		if code != 7 {
			t.Errorf("Exec() = %d, want the command's exit code 7", code)
		}
		if !strings.Contains(out.String(), "contract") {
			t.Errorf("Exec() output = %q, want the command's output", out.String())
		}
	})

	check("stopping is reflected, and Exec refuses a stopped instance", func(t *testing.T) {
		if err := env.Client.StopInstance(ctx, env.Instance); err != nil {
			t.Fatalf("StopInstance() error = %v", err)
		}
		inst, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatal(err)
		}
		if inst.IsRunning() {
			t.Errorf("Status = %q, want it stopped", inst.Status)
		}

		if _, err := env.Client.Exec(ctx, env.Instance, []string{"true"}, incus.ExecOptions{}); err == nil {
			t.Error("Exec() = nil error against a stopped instance")
		}
	})

	check("changing an instance that is not there is an error", func(t *testing.T) {
		absent := env.Instance + "-nope"

		if err := env.Client.UpdateInstance(ctx, absent, incus.InstanceChange{RemoveDevices: []string{"extra"}}, ""); err == nil {
			t.Error("UpdateInstance() = nil error for an instance that does not exist")
		}
		if err := env.Client.UpdateInstance(ctx, absent, incus.InstanceChange{SetConfig: map[string]string{"limits.cpu": "1"}, UnsetConfig: nil}, ""); err == nil {
			t.Error("UpdateInstance() = nil error for an instance that does not exist")
		}
	})

	check("a write against a stale reading is refused", func(t *testing.T) {
		// What two idevs in two terminals do to each other. Both read, both
		// decide what to record from what they read, and the later write
		// erases the earlier one's answer unless the etag stops it.
		first, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatalf("Instance() error = %v", err)
		}
		if first.ETag == "" {
			t.Fatal("Instance() returned no ETag, so nothing can be written against it")
		}

		// Someone else writes in between.
		if err := env.Client.UpdateInstance(ctx, env.Instance,
			incus.InstanceChange{SetConfig: map[string]string{"user.contract-other": "1"}}, ""); err != nil {
			t.Fatalf("UpdateInstance() error = %v", err)
		}
		t.Cleanup(func() {
			_ = env.Client.UpdateInstance(ctx, env.Instance, incus.InstanceChange{SetConfig: nil, UnsetConfig: []string{"user.contract-other"}}, "")
		})

		err = env.Client.UpdateInstance(ctx, env.Instance,
			incus.InstanceChange{SetConfig: map[string]string{"user.contract-stale": "1"}}, first.ETag)
		if !errors.Is(err, incus.ErrChanged) {
			t.Errorf("UpdateInstance() with a stale etag = %v, want ErrChanged", err)
		}

		// Refused, not partly applied.
		after, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatalf("Instance() error = %v", err)
		}
		if _, written := after.Config["user.contract-stale"]; written {
			t.Error("the refused write was applied anyway")
		}
		if _, kept := after.Config["user.contract-other"]; !kept {
			t.Error("the other write was lost")
		}
	})

	check("a write with no etag is not judged against one", func(t *testing.T) {
		// restart-pending is decided from a reading of its own and written
		// straight back; it must not be refused because something else moved
		// the instance on.
		//
		// This is also what holds the two readings together: the write takes
		// its own etag from the plain instance while Instance hands callers
		// the one from the full instance. If the daemon computed them
		// differently, this write would be refused.
		if err := env.Client.UpdateInstance(ctx, env.Instance,
			incus.InstanceChange{SetConfig: map[string]string{"user.contract-free": "1"}}, ""); err != nil {
			t.Errorf("UpdateInstance() with no etag = %v, want it applied", err)
		}
		t.Cleanup(func() {
			_ = env.Client.UpdateInstance(ctx, env.Instance, incus.InstanceChange{SetConfig: nil, UnsetConfig: []string{"user.contract-free"}}, "")
		})
	})

	check("starting and stopping moves the etag on", func(t *testing.T) {
		// The daemon's etag covers the volatile keys a start writes, so a
		// reading taken before one is stale afterwards. A client that says
		// otherwise lets a test hold an etag across a restart and prove
		// nothing.
		before, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatalf("Instance() error = %v", err)
		}
		if err := env.Client.StartInstance(ctx, env.Instance); err != nil {
			t.Fatalf("StartInstance() error = %v", err)
		}
		t.Cleanup(func() { _ = env.Client.StopInstance(ctx, env.Instance) })

		after, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatalf("Instance() error = %v", err)
		}
		if before.ETag == after.ETag {
			t.Error("the etag did not move across a start")
		}
	})

	check("the two spellings of an idmap are one mapping to the daemon", func(t *testing.T) {
		// SameIDMapping says "both 1000 0" and the uid/gid pair ask for the
		// same thing, and idev skips a restart on that basis. CLAUDE.md lists
		// this among the beliefs about Incus that have already caused
		// regressions here, and until now it was held by a unit test of
		// idev's own opinion. This asks the daemon.
		const both, split = "both 1000 0", "uid 1000 0\ngid 1000 0"
		if !incus.SameIDMapping(both, split) {
			t.Fatal("SameIDMapping says these differ; the rest of this check assumes it does not")
		}

		set := func(value string) string {
			t.Helper()
			if err := env.Client.UpdateInstance(ctx, env.Instance,
				incus.InstanceChange{SetConfig: map[string]string{incus.IDMapKey: value}}, ""); err != nil {
				t.Fatalf("UpdateInstance(%q) error = %v", value, err)
			}
			inst, err := env.Client.Instance(ctx, env.Instance)
			if err != nil {
				t.Fatalf("Instance() error = %v", err)
			}
			return inst.Config[incus.IDMapKey]
		}
		t.Cleanup(func() {
			_ = env.Client.UpdateInstance(ctx, env.Instance,
				incus.InstanceChange{UnsetConfig: []string{incus.IDMapKey}}, "")
		})

		// Both spellings are accepted, and each is stored as written -- so
		// idev cannot tell them apart by reading the instance back, which is
		// exactly why it has to normalise.
		if got := set(both); got != both {
			t.Errorf("after writing %q the instance holds %q", both, got)
		}
		if got := set(split); got != split {
			t.Errorf("after writing %q the instance holds %q", split, got)
		}
	})

	check("a listed instance is detached from the client's state", func(t *testing.T) {
		// What the listing hands back must not be the client's own maps.
		//
		// It carries less than Instance does -- idev's ListInstances keeps
		// the name, the status and the config and drops the rest -- but that
		// is idev's choice, not something Incus does: GetInstances really
		// does return devices and profiles. Asserting the absence here would
		// pin a decision the client is free to revisit, and could not fail
		// against either side anyway.
		listed, err := env.Client.ListInstances(ctx)
		if err != nil {
			t.Fatalf("ListInstances() error = %v", err)
		}
		var found *incus.Instance
		for i := range listed {
			if listed[i].Name == env.Instance {
				found = &listed[i]
			}
		}
		if found == nil {
			t.Fatalf("ListInstances() did not include %s", env.Instance)
		}
		found.Config["user.incus-dev.listing-probe"] = "written through the listing"

		again, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatalf("Instance() error = %v", err)
		}
		if _, leaked := again.Config["user.incus-dev.listing-probe"]; leaked {
			t.Error("a write to a listed instance reached the client's own state")
		}
	})

	check("a created device is not shared with the caller", func(t *testing.T) {
		// The same rule as for a written device, on the other way in. The
		// contract covered one half of it and the fake diverged on the other.
		device := incus.Device{"type": "disk", "source": "/srv", "path": "/created-probe"}
		spec := incus.InstanceSpec{
			Name:    env.Instance + "-devcopy",
			Image:   env.Image,
			Config:  map[string]string{"user.incus-dev.project": "contract"},
			Devices: map[string]incus.Device{"probe": device},
		}
		if err := env.Client.CreateInstance(ctx, spec); err != nil {
			t.Fatalf("CreateInstance() error = %v", err)
		}
		t.Cleanup(func() { _ = env.Client.DeleteInstance(ctx, spec.Name) })

		device["path"] = "/changed-after-the-create"

		inst, err := env.Client.Instance(ctx, spec.Name)
		if err != nil {
			t.Fatalf("Instance() error = %v", err)
		}
		if got := inst.Devices["probe"]["path"]; got != "/created-probe" {
			t.Errorf("device path = %q, want %q: the create shares state with the caller", got, "/created-probe")
		}
	})

	check("a written device is not shared with the caller", func(t *testing.T) {
		// The real client serialises the request, so a caller that reuses its
		// map cannot reach back into the instance. A client that keeps the
		// map lets a test mutate state it has already written.
		device := incus.Device{"type": "disk", "source": "/srv", "path": "/shared-probe"}
		change := incus.InstanceChange{SetDevices: map[string]incus.Device{"probe": device}}
		if err := env.Client.UpdateInstance(ctx, env.Instance, change, ""); err != nil {
			t.Fatalf("UpdateInstance() error = %v", err)
		}
		t.Cleanup(func() {
			_ = env.Client.UpdateInstance(ctx, env.Instance,
				incus.InstanceChange{RemoveDevices: []string{"probe"}}, "")
		})

		device["path"] = "/changed-after-the-write"

		inst, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatalf("Instance() error = %v", err)
		}
		if got := inst.Devices["probe"]["path"]; got != "/shared-probe" {
			t.Errorf("device path = %q, want %q: the write shares state with the caller", got, "/shared-probe")
		}
	})

	check("Instance returns a detached copy", func(t *testing.T) {
		// Callers hold the result across other calls and compare it with what
		// they are about to write. A client that hands back its own live state
		// makes that comparison always agree with itself, so a stale snapshot
		// -- which is what a second idev produces -- cannot be expressed at
		// all, in a test or in the code being tested.
		first, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatalf("Instance() error = %v", err)
		}
		first.Config["user.incus-dev.probe"] = "written on the copy"

		second, err := env.Client.Instance(ctx, env.Instance)
		if err != nil {
			t.Fatalf("Instance() error = %v", err)
		}
		if _, leaked := second.Config["user.incus-dev.probe"]; leaked {
			t.Error("a write to one result reached the next: Instance shares state with the client")
		}
	})

	check("DeleteInstance makes it absent", func(t *testing.T) {
		if err := env.Client.DeleteInstance(ctx, env.Instance); err != nil {
			t.Fatalf("DeleteInstance() error = %v", err)
		}
		if _, err := env.Client.Instance(ctx, env.Instance); !errors.Is(err, incus.ErrInstanceNotFound) {
			t.Errorf("Instance() error = %v, want ErrInstanceNotFound after delete", err)
		}
		if err := env.Client.DeleteInstance(ctx, env.Instance); err == nil {
			t.Error("DeleteInstance() = nil error for an instance that is already gone")
		}
	})

	// Last, and on an instance of its own: the daemon may finish the delete
	// after the wait is abandoned, so this consumes whatever it runs against.
	check("a delete cut short reports that the outcome is unknown", func(t *testing.T) {
		cut := spec
		cut.Name = env.Instance + "-cut"
		if err := env.Client.CreateInstance(ctx, cut); err != nil {
			t.Fatalf("CreateInstance() error = %v", err)
		}
		t.Cleanup(func() { _ = env.Client.DeleteInstance(ctx, cut.Name) })

		// A lookup afterwards cannot answer this -- the daemon is still
		// deleting when it replies -- so the failure has to say so itself.
		done, cancel := context.WithCancel(ctx)
		cancel()

		if err := env.Client.DeleteInstance(done, cut.Name); !errors.Is(err, incus.ErrOutcomeUnknown) {
			t.Errorf("DeleteInstance() with a done context = %v, want ErrOutcomeUnknown", err)
		}
	})

	check("a running instance refuses before the delete is sent", func(t *testing.T) {
		// The force stop comes first and will not start once the context is
		// done, so nothing is sent and the outcome is not in doubt. Saying it
		// is would offer the user advice about volumes nothing has touched.
		running := spec
		running.Name = env.Instance + "-running"
		if err := env.Client.CreateInstance(ctx, running); err != nil {
			t.Fatalf("CreateInstance() error = %v", err)
		}
		t.Cleanup(func() { _ = env.Client.DeleteInstance(ctx, running.Name) })
		if err := env.Client.StartInstance(ctx, running.Name); err != nil {
			t.Fatalf("StartInstance() error = %v", err)
		}

		done, cancel := context.WithCancel(ctx)
		cancel()

		err := env.Client.DeleteInstance(done, running.Name)
		if err == nil {
			t.Fatal("DeleteInstance() = nil error with a done context")
		}
		if errors.Is(err, incus.ErrOutcomeUnknown) {
			t.Errorf("DeleteInstance() = %v, want no claim of an unknown outcome: "+
				"the stop refuses before anything is sent", err)
		}
	})

	return ran
}
