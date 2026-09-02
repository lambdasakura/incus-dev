package incustest_test

import (
	"context"
	"errors"
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

	if err := f.ApplyConfig(ctx, "dev-x", nil); err != nil {
		t.Fatalf("ApplyConfig(nil) error = %v", err)
	}
	if err := f.ApplyDevices(ctx, "dev-x", nil); err != nil {
		t.Fatalf("ApplyDevices(nil) error = %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("recorded a call for an empty apply: %v", f.Calls)
	}

	if err := f.ApplyConfig(ctx, "dev-x", map[string]string{"a": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.UnsetConfig(ctx, "dev-x", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Instances["dev-x"].Config["a"]; ok {
		t.Error("UnsetConfig() did not remove it")
	}
	if err := f.UnsetConfig(ctx, "dev-x", nil); err != nil {
		t.Fatalf("UnsetConfig(nil) error = %v", err)
	}

	// As the real thing does, a declared device is replaced.
	dev := map[string]incus.Device{"ws": {"type": "disk", "path": "/ws", "shift": "true"}}
	if err := f.ApplyDevices(ctx, "dev-x", dev); err != nil {
		t.Fatal(err)
	}
	if err := f.ApplyDevices(ctx, "dev-x", map[string]incus.Device{"ws": {"type": "disk", "path": "/ws2"}}); err != nil {
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
	if err := f.ApplyDevices(ctx, "dev-x", map[string]incus.Device{"ws": {"type": "proxy"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Instances["dev-x"].Devices["ws"]["path"]; ok {
		t.Errorf("device = %v, want it recreated when the type changed", f.Instances["dev-x"].Devices["ws"])
	}
}

func TestFakeConfigOnMissingInstance(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()

	if err := f.ApplyConfig(ctx, "dev-x", map[string]string{"a": "1"}); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Errorf("ApplyConfig() error = %v", err)
	}
	if err := f.ApplyDevices(ctx, "dev-x", map[string]incus.Device{"d": {"type": "disk"}}); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Errorf("ApplyDevices() error = %v", err)
	}
	if err := f.UnsetConfig(ctx, "dev-x", []string{"a"}); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Errorf("UnsetConfig() error = %v", err)
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

	if ok, _ := f.ProfileExists(ctx, "default"); !ok {
		t.Error("the default profile is missing")
	}
	if ok, _ := f.ProfileExists(ctx, "missing"); ok {
		t.Error("reported a profile that does not exist as existing")
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
			return f.ApplyConfig(ctx, "dev-x", map[string]string{"a": "1"})
		},
		"unset": func(f *incustest.Fake) error { return f.UnsetConfig(ctx, "dev-x", []string{"a"}) },
		"devices": func(f *incustest.Fake) error {
			return f.ApplyDevices(ctx, "dev-x", map[string]incus.Device{"d": {"type": "disk"}})
		},
		"profile": func(f *incustest.Fake) error { _, err := f.ProfileExists(ctx, "p"); return err },
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

	err := f.ApplyDevices(context.Background(), "dev-x", map[string]incus.Device{
		"data": {"type": "disk", "source": "/srv/data", "path": "/data"},
	})
	if err != nil {
		t.Fatalf("ApplyDevices() error = %v", err)
	}

	got := f.Instances["dev-x"].Devices["data"]
	if _, ok := got["pool"]; ok {
		t.Errorf("data = %v, want the key that left the declaration gone", got)
	}
}
