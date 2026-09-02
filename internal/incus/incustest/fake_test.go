package incustest_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/incus/incustest"
)

var errInjected = errors.New("injected")

func TestFakeLifecycle(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()

	if _, err := f.Instance(ctx, "dev-x"); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Fatalf("Instance() error = %v, want ErrInstanceNotFound", err)
	}

	spec := incus.InstanceSpec{
		Name:     "dev-x",
		Image:    "images:alpine/3.21",
		Profiles: []string{"default"},
		Config:   map[string]string{"limits.cpu": "2"},
		Devices:  map[string]incus.Device{"root": {"type": "disk", "path": "/"}},
	}
	if err := f.CreateInstance(ctx, spec); err != nil {
		t.Fatalf("CreateInstance() error = %v", err)
	}

	inst, err := f.Instance(ctx, "dev-x")
	if err != nil {
		t.Fatalf("Instance() error = %v", err)
	}
	if inst.Status != "Stopped" || inst.Config["limits.cpu"] != "2" || inst.Devices["root"]["path"] != "/" {
		t.Errorf("Instance() = %+v", inst)
	}

	if err := f.StartInstance(ctx, "dev-x"); err != nil {
		t.Fatal(err)
	}
	if !f.Instances["dev-x"].IsRunning() {
		t.Error("StartInstance() did not move it to Running")
	}
	if err := f.StopInstance(ctx, "dev-x"); err != nil {
		t.Fatal(err)
	}
	if f.Instances["dev-x"].IsRunning() {
		t.Error("StopInstance() did not move it to Stopped")
	}
	if err := f.DeleteInstance(ctx, "dev-x"); err != nil {
		t.Fatal(err)
	}
	if len(f.Instances) != 0 {
		t.Errorf("still present after DeleteInstance(): %v", f.Instances)
	}
}

func TestFakeConfigAndDevices(t *testing.T) {
	ctx := context.Background()
	f := incustest.New().AddInstance(&incus.Instance{Name: "dev-x", Status: "Stopped"})

	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetConfig: nil, UnsetConfig: nil}, ""); err != nil {
		t.Fatalf("ApplyConfig(nil) error = %v", err)
	}
	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetDevices: nil}, ""); err != nil {
		t.Fatalf("ApplyDevices(nil) error = %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("recorded a call for an empty apply: %v", f.Calls)
	}

	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetConfig: map[string]string{"a": "1"}, UnsetConfig: nil}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetConfig: nil, UnsetConfig: []string{"a"}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Instances["dev-x"].Config["a"]; ok {
		t.Error("UpdateInstance() did not remove it")
	}
	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetConfig: nil, UnsetConfig: nil}, ""); err != nil {
		t.Fatalf("UnsetConfig(nil) error = %v", err)
	}

	// As the real thing does, a declared device is replaced.
	dev := map[string]incus.Device{"ws": {"type": "disk", "path": "/ws", "shift": "true"}}
	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetDevices: dev}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetDevices: map[string]incus.Device{"ws": {"type": "disk", "path": "/ws2"}}}, ""); err != nil {
		t.Fatal(err)
	}
	got := f.Instances["dev-x"].Devices["ws"]
	if got["path"] != "/ws2" {
		t.Errorf("device = %v, want the declared path", got)
	}
	if _, ok := got["shift"]; ok {
		t.Errorf("device = %v, want a key that left the declaration gone", got)
	}

	// A changed type means a recreated device.
	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetDevices: map[string]incus.Device{"ws": {"type": "proxy"}}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Instances["dev-x"].Devices["ws"]["path"]; ok {
		t.Errorf("device = %v, want it recreated when the type changed", f.Instances["dev-x"].Devices["ws"])
	}
}

func TestFakeConfigOnMissingInstance(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()

	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetConfig: map[string]string{"a": "1"}, UnsetConfig: nil}, ""); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Errorf("UpdateInstance() error = %v", err)
	}
	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetDevices: map[string]incus.Device{"d": {"type": "disk"}}}, ""); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Errorf("UpdateInstance() error = %v", err)
	}
	if err := f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetConfig: nil, UnsetConfig: []string{"a"}}, ""); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Errorf("UpdateInstance() error = %v", err)
	}
}

func TestFakeExec(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()

	// As the real thing does, exec fails against an absent or stopped instance.
	if _, err := f.Exec(ctx, "dev-x", []string{"true"}, incus.ExecOptions{}); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Errorf("Exec() error = %v", err)
	}

	f.AddInstance(&incus.Instance{Name: "dev-x", Status: "Stopped"})
	if _, err := f.Exec(ctx, "dev-x", []string{"true"}, incus.ExecOptions{}); err == nil {
		t.Error("want a failure against a stopped instance")
	}

	f.Instances["dev-x"].Status = "Running"
	if code, err := f.Exec(ctx, "dev-x", []string{"true"}, incus.ExecOptions{}); code != 0 || err != nil {
		t.Errorf("Exec() = %d, %v", code, err)
	}

	f.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) { return 7, nil }
	if code, _ := f.Exec(ctx, "dev-x", []string{"true"}, incus.ExecOptions{}); code != 7 {
		t.Errorf("want the result of ExecFunc: %d", code)
	}
}

func TestFakeProfilesAndWaitReady(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()

	names, err := f.ProfileNames(ctx)
	if err != nil {
		t.Fatalf("ProfileNames() error = %v", err)
	}
	if !slices.Contains(names, "default") {
		t.Errorf("ProfileNames() = %v, want the default profile", names)
	}
	// The caller keeps the result while it works out what is missing.
	names[0] = "clobbered"
	if again, _ := f.ProfileNames(ctx); slices.Contains(again, "clobbered") {
		t.Errorf("ProfileNames() = %v, want the fake's own list left alone", again)
	}

	if err := f.WaitReady(ctx, "dev-x", incus.WaitOptions{}); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
	f.FailReady = true
	if err := f.WaitReady(ctx, "dev-x", incus.WaitOptions{}); err == nil {
		t.Error("want a failure while FailReady is set")
	}
}

func TestFakeErrorInjection(t *testing.T) {
	ctx := context.Background()

	ops := map[string]func(*incustest.Fake) error{
		"instance": func(f *incustest.Fake) error { _, err := f.Instance(ctx, "dev-x"); return err },
		"create": func(f *incustest.Fake) error {
			return f.CreateInstance(ctx, incus.InstanceSpec{Name: "dev-x"})
		},
		"start":  func(f *incustest.Fake) error { return f.StartInstance(ctx, "dev-x") },
		"stop":   func(f *incustest.Fake) error { return f.StopInstance(ctx, "dev-x") },
		"delete": func(f *incustest.Fake) error { return f.DeleteInstance(ctx, "dev-x") },
		"config": func(f *incustest.Fake) error {
			return f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetConfig: map[string]string{"a": "1"}, UnsetConfig: nil}, "")
		},
		"unset": func(f *incustest.Fake) error {
			return f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetConfig: nil, UnsetConfig: []string{"a"}}, "")
		},
		"devices": func(f *incustest.Fake) error {
			return f.UpdateInstance(ctx, "dev-x", incus.InstanceChange{SetDevices: map[string]incus.Device{"d": {"type": "disk"}}}, "")
		},
		"profiles": func(f *incustest.Fake) error { _, err := f.ProfileNames(ctx); return err },
		"exec": func(f *incustest.Fake) error {
			_, err := f.Exec(ctx, "dev-x", []string{"true"}, incus.ExecOptions{})
			return err
		},
		"waitready": func(f *incustest.Fake) error { return f.WaitReady(ctx, "dev-x", incus.WaitOptions{}) },
	}

	for prefix, call := range ops {
		t.Run(prefix, func(t *testing.T) {
			f := incustest.New().AddInstance(&incus.Instance{Name: "dev-x", Status: "Running"})
			f.FailOn = map[string]error{prefix: errInjected}

			if err := call(f); !errors.Is(err, errInjected) {
				t.Errorf("error = %v, want %v", err, errInjected)
			}
		})
	}
}

func TestFakeCalled(t *testing.T) {
	f := incustest.New()
	f.AddInstance(&incus.Instance{Name: "dev-x", Status: "Stopped"})

	if f.Called("start") {
		t.Error("Called() is true though nothing ran")
	}
	if err := f.StartInstance(context.Background(), "dev-x"); err != nil {
		t.Fatal(err)
	}
	if !f.Called("start dev-x") {
		t.Errorf("Calls = %v", f.Calls)
	}
	if !strings.Contains(strings.Join(f.Calls, " "), "dev-x") {
		t.Errorf("Calls = %v", f.Calls)
	}
}

func TestFakeInstanceIsFoundAfterCreate(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()

	if err := f.CreateInstance(ctx, incus.InstanceSpec{Name: "dev-x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Instance(ctx, "dev-x"); err != nil {
		t.Errorf("Instance() error = %v", err)
	}
}

func TestFakeHook(t *testing.T) {
	f := incustest.New()
	f.Hook = func(call string) error {
		if strings.HasPrefix(call, "start") {
			return errInjected
		}
		return nil
	}

	f.AddInstance(&incus.Instance{Name: "dev-x", Status: "Running"})

	if err := f.StartInstance(context.Background(), "dev-x"); !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want %v", err, errInjected)
	}
	if err := f.StopInstance(context.Background(), "dev-x"); err != nil {
		t.Errorf("error = %v, want nil", err)
	}
}

// The fake refuses what the real Incus refuses.
//
// A fake more forgiving than the real thing makes the guards in other packages
// untestable: an instance that was never created could be started, stopped and
// deleted, so removing an existence check broke nothing (spec 08-testing.md
// 8.1).
func TestFakeRefusesAbsentInstances(t *testing.T) {
	ctx := context.Background()

	for _, tt := range []struct {
		name string
		call func(f *incustest.Fake) error
	}{
		{"start", func(f *incustest.Fake) error { return f.StartInstance(ctx, "nope") }},
		{"stop", func(f *incustest.Fake) error { return f.StopInstance(ctx, "nope") }},
		{"delete", func(f *incustest.Fake) error { return f.DeleteInstance(ctx, "nope") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(incustest.New()); !errors.Is(err, incus.ErrInstanceNotFound) {
				t.Errorf("error = %v, want ErrInstanceNotFound", err)
			}
		})
	}
}

// Creating a name that is already in use is a conflict, not an overwrite.
func TestFakeRefusesToCreateTwice(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()
	spec := incus.InstanceSpec{Name: "dev-x", Image: "images:alpine/3.21"}

	if err := f.CreateInstance(ctx, spec); err != nil {
		t.Fatalf("CreateInstance() error = %v", err)
	}
	if err := f.CreateInstance(ctx, spec); err == nil {
		t.Error("CreateInstance() = nil error, want a name already in use to be refused")
	}
}

// profiles: [] means no profile at all, which the fake has to reflect.
func TestFakeRecordsNoProfiles(t *testing.T) {
	f := incustest.New()

	if err := f.CreateInstance(context.Background(), incus.InstanceSpec{
		Name: "dev-x", Image: "images:alpine/3.21",
		Profiles: []string{"default"}, NoProfiles: true,
	}); err != nil {
		t.Fatalf("CreateInstance() error = %v", err)
	}

	if got := f.Instances["dev-x"].Profiles; len(got) != 0 {
		t.Errorf("Profiles = %v, want none", got)
	}
}

// Deleting a snapshot that is not there is an error, and the slices handed out
// by Snapshots are not rewritten underneath the caller.
func TestFakeDeleteSnapshot(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()
	f.SnapshotsByInstance["dev-x"] = []incus.Snapshot{{Name: "a"}, {Name: "b"}}

	if err := f.DeleteSnapshot(ctx, "dev-x", "typo"); err == nil {
		t.Error("DeleteSnapshot() = nil error, want an unknown snapshot to be refused")
	}

	before, err := f.Snapshots(ctx, "dev-x")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteSnapshot(ctx, "dev-x", "a"); err != nil {
		t.Fatalf("DeleteSnapshot() error = %v", err)
	}
	if before[0].Name != "a" || before[1].Name != "b" {
		t.Errorf("the slice returned earlier became %v", before)
	}
}

// The fake replaces a declared device, as the real ApplyDevices does.
//
// Merging instead would leave every internal/cli test checking the opposite
// of what production does, which is where a device-replacement regression
// would land.
func TestFakeApplyDevicesReplaces(t *testing.T) {
	f := incustest.New()
	f.AddInstance(&incus.Instance{
		Name: "dev-x",
		Devices: map[string]incus.Device{
			"data": {"type": "disk", "pool": "fast", "source": "vol", "path": "/data"},
		},
	})

	err := f.UpdateInstance(context.Background(), "dev-x", incus.InstanceChange{SetDevices: map[string]incus.Device{
		"data": {"type": "disk", "source": "/srv/data", "path": "/data"},
	}}, "")
	if err != nil {
		t.Fatalf("UpdateInstance() error = %v", err)
	}

	got := f.Instances["dev-x"].Devices["data"]
	if _, ok := got["pool"]; ok {
		t.Errorf("data = %v, want the key that left the declaration gone", got)
	}
}

// Incus answers 404 for a volume that is not there, and so does the fake:
// succeeding would hide a caller that deletes straight from a stale record.
func TestFakeDeleteVolumeRejectsWhatIsNotThere(t *testing.T) {
	f := incustest.New()

	if err := f.DeleteVolume(context.Background(), "default", "missing"); err == nil {
		t.Error("DeleteVolume() = nil error, want it refused for an unknown volume")
	}

	f.Volumes["default/there"] = true
	if err := f.DeleteVolume(context.Background(), "default", "there"); err != nil {
		t.Errorf("DeleteVolume() error = %v", err)
	}
	if f.Volumes["default/there"] {
		t.Error("the volume was not removed")
	}
}

// Every change to an instance moves its version on, so a reading taken before
// one cannot be written against. The contract pins the start; these are the
// rest, which no production path holds an etag across today and so nothing
// else would notice going wrong.
func TestEveryChangeMovesTheETag(t *testing.T) {
	ctx := context.Background()

	for _, tt := range []struct {
		name   string
		change func(*testing.T, *incustest.Fake)
	}{
		{"stop", func(t *testing.T, f *incustest.Fake) {
			if err := f.StopInstance(ctx, "dev-x"); err != nil {
				t.Fatal(err)
			}
		}},
		{"restore a snapshot", func(t *testing.T, f *incustest.Fake) {
			if err := f.CreateSnapshot(ctx, "dev-x", "s1"); err != nil {
				t.Fatal(err)
			}
			if err := f.RestoreSnapshot(ctx, "dev-x", "s1"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := incustest.New()
			if err := f.CreateInstance(ctx, incus.InstanceSpec{Name: "dev-x", Image: "img"}); err != nil {
				t.Fatal(err)
			}
			if err := f.StartInstance(ctx, "dev-x"); err != nil {
				t.Fatal(err)
			}

			before, err := f.Instance(ctx, "dev-x")
			if err != nil {
				t.Fatal(err)
			}
			tt.change(t, f)

			after, err := f.Instance(ctx, "dev-x")
			if err != nil {
				t.Fatal(err)
			}
			if before.ETag == after.ETag {
				t.Errorf("the etag did not move across a %s", tt.name)
			}
		})
	}

	// And creating one gives it an etag of its own, not the one a previous
	// instance of the same name left behind.
	f := incustest.New()
	if err := f.CreateInstance(ctx, incus.InstanceSpec{Name: "dev-y", Image: "img"}); err != nil {
		t.Fatal(err)
	}
	first, err := f.Instance(ctx, "dev-y")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteInstance(ctx, "dev-y"); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateInstance(ctx, incus.InstanceSpec{Name: "dev-y", Image: "img"}); err != nil {
		t.Fatal(err)
	}
	second, err := f.Instance(ctx, "dev-y")
	if err != nil {
		t.Fatal(err)
	}
	if first.ETag == second.ETag {
		t.Error("a recreated instance kept the old etag")
	}
}

// The listing carries what API.ListInstances builds and no more.
//
// The contract cannot ask this: API.ListInstances constructs
// Instance{Name, Status, Config} literally, so it can never carry a device
// whatever the daemon sends, and an assertion there passes for a reason that
// has nothing to do with the fake. It is a correspondence between this fake
// and that function, so it is checked here -- and it matters because nearly
// every unit test in the repository reads the fake, not the daemon.
func TestListInstancesCarriesWhatTheAPIBuilds(t *testing.T) {
	ctx := context.Background()

	f := incustest.New()
	if err := f.CreateInstance(ctx, incus.InstanceSpec{
		Name:     "dev-x",
		Image:    "img",
		Profiles: []string{"default"},
		Config:   map[string]string{"user.incus-dev.project": "x"},
		Devices:  map[string]incus.Device{"ws": {"type": "disk", "path": "/ws"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.StartInstance(ctx, "dev-x"); err != nil {
		t.Fatal(err)
	}

	listed, err := f.ListInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListInstances() returned %d instances, want 1", len(listed))
	}
	got := listed[0]

	if got.Name != "dev-x" || got.Status == "" || got.Config["user.incus-dev.project"] != "x" {
		t.Errorf("ListInstances() = %+v, want the name, the status and the config", got)
	}
	// Everything the API's own literal leaves out.
	for _, absent := range []struct {
		field string
		empty bool
	}{
		{"Devices", len(got.Devices) == 0},
		{"ExpandedDevices", len(got.ExpandedDevices) == 0},
		{"Profiles", len(got.Profiles) == 0},
		{"State", got.State == nil},
		{"LastUsedAt", got.LastUsedAt.IsZero()},
		{"ETag", got.ETag == ""},
	} {
		if !absent.empty {
			t.Errorf("ListInstances() filled in %s, which API.ListInstances never does; "+
				"a test reading it would read a field that is empty in production", absent.field)
		}
	}

	// And what it does carry is not the fake's own map.
	got.Config["user.incus-dev.listing-probe"] = "1"
	inst, err := f.Instance(ctx, "dev-x")
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := inst.Config["user.incus-dev.listing-probe"]; leaked {
		t.Error("a write to a listed instance reached the fake's own state")
	}
}
