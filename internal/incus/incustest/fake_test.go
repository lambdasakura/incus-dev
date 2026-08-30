package incustest_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus/incustest"
)

var errInjected = errors.New("injected")

func TestFakeLifecycle(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()

	if ok, err := f.InstanceExists(ctx, "dev-x"); ok || err != nil {
		t.Fatalf("InstanceExists() = %v, %v, want false, nil", ok, err)
	}
	if _, err := f.Instance(ctx, "dev-x"); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Fatalf("Instance() error = %v, want ErrInstanceNotFound", err)
	}

	spec := incus.InstanceSpec{
		Name:     "dev-x",
		Image:    "images:alpine/3.21",
		Type:     "container",
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
		t.Error("StartInstance() で Running にならない")
	}
	if err := f.StopInstance(ctx, "dev-x"); err != nil {
		t.Fatal(err)
	}
	if f.Instances["dev-x"].IsRunning() {
		t.Error("StopInstance() で Stopped にならない")
	}
	if err := f.DeleteInstance(ctx, "dev-x"); err != nil {
		t.Fatal(err)
	}
	if len(f.Instances) != 0 {
		t.Errorf("DeleteInstance() 後に残っている: %v", f.Instances)
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
		t.Errorf("空の適用で呼び出しを記録している: %v", f.Calls)
	}

	if err := f.ApplyConfig(ctx, "dev-x", map[string]string{"a": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.UnsetConfig(ctx, "dev-x", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Instances["dev-x"].Config["a"]; ok {
		t.Error("UnsetConfig() で削除されていない")
	}
	if err := f.UnsetConfig(ctx, "dev-x", nil); err != nil {
		t.Fatalf("UnsetConfig(nil) error = %v", err)
	}

	// 本物同様、宣言されていないキーは残す
	dev := map[string]incus.Device{"ws": {"type": "disk", "path": "/ws", "shift": "true"}}
	if err := f.ApplyDevices(ctx, "dev-x", dev); err != nil {
		t.Fatal(err)
	}
	if err := f.ApplyDevices(ctx, "dev-x", map[string]incus.Device{"ws": {"type": "disk", "path": "/ws2"}}); err != nil {
		t.Fatal(err)
	}
	got := f.Instances["dev-x"].Devices["ws"]
	if got["path"] != "/ws2" || got["shift"] != "true" {
		t.Errorf("device = %v, 宣言外のキーは残すこと", got)
	}

	// 型が変われば作り直す
	if err := f.ApplyDevices(ctx, "dev-x", map[string]incus.Device{"ws": {"type": "proxy"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Instances["dev-x"].Devices["ws"]["path"]; ok {
		t.Errorf("device = %v, 型変更時は作り直すこと", f.Instances["dev-x"].Devices["ws"])
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

	// 実物同様、存在しない/停止中のinstanceへのexecは失敗する
	if _, err := f.Exec(ctx, "dev-x", []string{"true"}, incus.ExecOptions{}); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Errorf("Exec() error = %v", err)
	}

	f.AddInstance(&incus.Instance{Name: "dev-x", Status: "Stopped"})
	if _, err := f.Exec(ctx, "dev-x", []string{"true"}, incus.ExecOptions{}); err == nil {
		t.Error("停止中のinstanceでは失敗すること")
	}

	f.Instances["dev-x"].Status = "Running"
	if code, err := f.Exec(ctx, "dev-x", []string{"true"}, incus.ExecOptions{}); code != 0 || err != nil {
		t.Errorf("Exec() = %d, %v", code, err)
	}

	f.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) { return 7, nil }
	if code, _ := f.Exec(ctx, "dev-x", []string{"true"}, incus.ExecOptions{}); code != 7 {
		t.Errorf("ExecFunc の結果を返すこと: %d", code)
	}
}

func TestFakeProfilesAndWaitReady(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()

	if ok, _ := f.ProfileExists(ctx, "default"); !ok {
		t.Error("既定のProfileが存在しない")
	}
	if ok, _ := f.ProfileExists(ctx, "missing"); ok {
		t.Error("存在しないProfileを存在すると判定している")
	}

	if err := f.WaitReady(ctx, "dev-x", incus.WaitOptions{}); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
	f.FailReady = true
	if err := f.WaitReady(ctx, "dev-x", incus.WaitOptions{}); err == nil {
		t.Error("FailReady 時は失敗すること")
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

	if f.Called("start") {
		t.Error("何も実行していないのに Called() が真")
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

func TestFakeInstanceExistsAfterCreate(t *testing.T) {
	ctx := context.Background()
	f := incustest.New()

	if err := f.CreateInstance(ctx, incus.InstanceSpec{Name: "dev-x"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.InstanceExists(ctx, "dev-x"); !ok || err != nil {
		t.Errorf("InstanceExists() = %v, %v, want true, nil", ok, err)
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

	if err := f.StartInstance(context.Background(), "dev-x"); !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want %v", err, errInjected)
	}
	if err := f.StopInstance(context.Background(), "dev-x"); err != nil {
		t.Errorf("error = %v, want nil", err)
	}
}
