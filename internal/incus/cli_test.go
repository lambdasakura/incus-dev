package incus_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner/runnertest"
)

const listJSON = `[
  {"name":"dev-example","status":"Running","type":"container",
   "profiles":["default"],
   "config":{"limits.cpu":"8","image.os":"ubuntu","user.incus-devkit.project":"example"},
   "devices":{"workspace":{"type":"disk","source":"/home/u/src/example","path":"/workspace"}}}
]`

func newCLI(f *runnertest.Fake) *incus.CLI {
	return &incus.CLI{Runner: f, Project: "default"}
}

func TestInstanceParsesListOutput(t *testing.T) {
	f := &runnertest.Fake{Stdout: map[string]string{"incus list": listJSON}}
	c := newCLI(f)

	inst, err := c.Instance(context.Background(), "dev-example")
	if err != nil {
		t.Fatalf("Instance() error = %v", err)
	}
	if inst.Name != "dev-example" || inst.Status != "Running" {
		t.Errorf("Instance() = %+v", inst)
	}
	if got := inst.Config["limits.cpu"]; got != "8" {
		t.Errorf("Config[limits.cpu] = %q", got)
	}
	if got := inst.Devices["workspace"]["path"]; got != "/workspace" {
		t.Errorf("Devices[workspace][path] = %q", got)
	}
	if !cmp.Equal(inst.Profiles, []string{"default"}) {
		t.Errorf("Profiles = %v", inst.Profiles)
	}
	want := "incus list --project default --format json dev-example"
	if got := f.LastCommand(); got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestInstanceNotFound(t *testing.T) {
	f := &runnertest.Fake{Stdout: map[string]string{"incus list": `[]`}}
	c := newCLI(f)

	_, err := c.Instance(context.Background(), "dev-missing")
	if !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Fatalf("Instance() error = %v, want ErrInstanceNotFound", err)
	}
}

// 前方一致で別のinstanceが返らないこと
func TestInstanceRequiresExactMatch(t *testing.T) {
	f := &runnertest.Fake{Stdout: map[string]string{"incus list": listJSON}}
	c := newCLI(f)

	if _, err := c.Instance(context.Background(), "dev-ex"); !errors.Is(err, incus.ErrInstanceNotFound) {
		t.Fatalf("Instance() error = %v, want ErrInstanceNotFound", err)
	}
}

func TestInstanceExists(t *testing.T) {
	f := &runnertest.Fake{Stdout: map[string]string{"incus list": listJSON}}
	c := newCLI(f)

	ok, err := c.InstanceExists(context.Background(), "dev-example")
	if err != nil || !ok {
		t.Errorf("InstanceExists() = %v, %v, want true, nil", ok, err)
	}

	f.Stdout["incus list"] = `[]`
	ok, err = c.InstanceExists(context.Background(), "dev-example")
	if err != nil || ok {
		t.Errorf("InstanceExists() = %v, %v, want false, nil", ok, err)
	}
}

func TestCreateInstance(t *testing.T) {
	tests := []struct {
		name string
		spec incus.InstanceSpec
		want string
	}{
		{
			name: "profileとconfig",
			spec: incus.InstanceSpec{
				Name:     "dev-example",
				Image:    "images:ubuntu/24.04",
				Profiles: []string{"default", "gpu"},
				Config:   map[string]string{"limits.memory": "16GiB", "limits.cpu": "8"},
			},
			// configキーは決定性のためソートする
			want: "incus create --project default images:ubuntu/24.04 dev-example -p default -p gpu -c limits.cpu=8 -c limits.memory=16GiB",
		},
		{
			name: "profileなし",
			spec: incus.InstanceSpec{
				Name:       "dev-example",
				Image:      "images:ubuntu/24.04",
				NoProfiles: true,
			},
			want: "incus create --project default images:ubuntu/24.04 dev-example --no-profiles",
		},
		{
			name: "仮想マシン",
			spec: incus.InstanceSpec{
				Name:  "dev-example",
				Image: "images:ubuntu/24.04",
				Type:  "virtual-machine",
			},
			want: "incus create --project default images:ubuntu/24.04 dev-example --vm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &runnertest.Fake{}
			c := newCLI(f)

			if err := c.CreateInstance(context.Background(), tt.spec); err != nil {
				t.Fatalf("CreateInstance() error = %v", err)
			}
			if got := f.LastCommand(); got != tt.want {
				t.Errorf("command =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestLifecycleCommands(t *testing.T) {
	tests := []struct {
		name string
		call func(*incus.CLI) error
		want string
	}{
		{"start", func(c *incus.CLI) error { return c.StartInstance(context.Background(), "dev-x") },
			"incus start --project default dev-x"},
		{"stop", func(c *incus.CLI) error { return c.StopInstance(context.Background(), "dev-x") },
			"incus stop --project default dev-x"},
		{"delete", func(c *incus.CLI) error { return c.DeleteInstance(context.Background(), "dev-x") },
			"incus delete --project default --force dev-x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &runnertest.Fake{}
			if err := tt.call(newCLI(f)); err != nil {
				t.Fatalf("error = %v", err)
			}
			if got := f.LastCommand(); got != tt.want {
				t.Errorf("command = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyConfig(t *testing.T) {
	f := &runnertest.Fake{}
	c := newCLI(f)

	err := c.ApplyConfig(context.Background(), "dev-x", map[string]string{
		"limits.memory": "16GiB",
		"limits.cpu":    "8",
	})
	if err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	want := "incus config set --project default dev-x limits.cpu=8 limits.memory=16GiB"
	if got := f.LastCommand(); got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestApplyConfigEmptyIsNoop(t *testing.T) {
	f := &runnertest.Fake{}
	c := newCLI(f)

	if err := c.ApplyConfig(context.Background(), "dev-x", nil); err != nil {
		t.Fatalf("ApplyConfig() error = %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("commands = %v, 空のconfigではコマンドを実行しないこと", f.Commands())
	}
}

func TestApplyDevicesAddsMissingDevice(t *testing.T) {
	f := &runnertest.Fake{Stdout: map[string]string{"incus list": `[{"name":"dev-x","devices":{}}]`}}
	c := newCLI(f)

	err := c.ApplyDevices(context.Background(), "dev-x", map[string]incus.Device{
		"workspace": {"type": "disk", "source": "/src", "path": "/workspace"},
	})
	if err != nil {
		t.Fatalf("ApplyDevices() error = %v", err)
	}
	want := "incus config device add --project default dev-x workspace disk path=/workspace source=/src"
	if got := f.LastCommand(); got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestApplyDevicesUpdatesExistingDevice(t *testing.T) {
	existing := `[{"name":"dev-x","devices":{"workspace":{"type":"disk","source":"/old","path":"/workspace"}}}]`
	f := &runnertest.Fake{Stdout: map[string]string{"incus list": existing}}
	c := newCLI(f)

	err := c.ApplyDevices(context.Background(), "dev-x", map[string]incus.Device{
		"workspace": {"type": "disk", "source": "/new", "path": "/workspace"},
	})
	if err != nil {
		t.Fatalf("ApplyDevices() error = %v", err)
	}
	want := "incus config device set --project default dev-x workspace source=/new"
	if got := f.LastCommand(); got != want {
		t.Errorf("command = %q, want %q (変更されたキーのみ設定すること)", got, want)
	}
}

func TestApplyDevicesSkipsUnchangedDevice(t *testing.T) {
	existing := `[{"name":"dev-x","devices":{"workspace":{"type":"disk","source":"/src","path":"/workspace"}}}]`
	f := &runnertest.Fake{Stdout: map[string]string{"incus list": existing}}
	c := newCLI(f)

	err := c.ApplyDevices(context.Background(), "dev-x", map[string]incus.Device{
		"workspace": {"type": "disk", "source": "/src", "path": "/workspace"},
	})
	if err != nil {
		t.Fatalf("ApplyDevices() error = %v", err)
	}
	for _, cmd := range f.Commands() {
		if strings.Contains(cmd, "config device") {
			t.Errorf("commands = %v, 変更が無ければdeviceを操作しないこと", f.Commands())
		}
	}
}

func TestApplyDevicesRecreatesOnTypeChange(t *testing.T) {
	existing := `[{"name":"dev-x","devices":{"data":{"type":"disk","source":"/src","path":"/data"}}}]`
	f := &runnertest.Fake{Stdout: map[string]string{"incus list": existing}}
	c := newCLI(f)

	err := c.ApplyDevices(context.Background(), "dev-x", map[string]incus.Device{
		"data": {"type": "proxy", "listen": "tcp:127.0.0.1:80"},
	})
	if err != nil {
		t.Fatalf("ApplyDevices() error = %v", err)
	}
	cmds := f.Commands()
	joined := strings.Join(cmds, "\n")
	if !strings.Contains(joined, "config device remove --project default dev-x data") {
		t.Errorf("commands = %v, 型が変わったdeviceは削除して作り直すこと", cmds)
	}
	if !strings.Contains(joined, "config device add --project default dev-x data proxy") {
		t.Errorf("commands = %v", cmds)
	}
}

func TestProfileExists(t *testing.T) {
	f := &runnertest.Fake{Stdout: map[string]string{
		"incus profile list": `[{"name":"default"},{"name":"gpu"}]`,
	}}
	c := newCLI(f)

	ok, err := c.ProfileExists(context.Background(), "gpu")
	if err != nil || !ok {
		t.Errorf("ProfileExists(gpu) = %v, %v, want true, nil", ok, err)
	}

	ok, err = c.ProfileExists(context.Background(), "nope")
	if err != nil || ok {
		t.Errorf("ProfileExists(nope) = %v, %v, want false, nil", ok, err)
	}
}

func TestExecBuildsCommand(t *testing.T) {
	tests := []struct {
		name string
		opt  incus.ExecOptions
		argv []string
		want string
	}{
		{
			name: "最小",
			argv: []string{"true"},
			want: "incus exec --project default dev-x -T -- true",
		},
		{
			name: "cwdと環境変数",
			opt: incus.ExecOptions{
				Cwd: "/workspace",
				Env: map[string]string{"B": "2", "A": "1"},
			},
			argv: []string{"sh", "-c", "echo hi"},
			want: `incus exec --project default dev-x --cwd /workspace --env A=1 --env B=2 -T -- sh -c "echo hi"`,
		},
		{
			name: "数値ユーザー",
			opt:  incus.ExecOptions{User: "1000"},
			argv: []string{"true"},
			want: "incus exec --project default dev-x --user 1000 -T -- true",
		},
		{
			name: "TTY",
			opt:  incus.ExecOptions{TTY: true},
			argv: []string{"/bin/bash"},
			want: "incus exec --project default dev-x -t -- /bin/bash",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &runnertest.Fake{}
			c := newCLI(f)

			if _, err := c.Exec(context.Background(), "dev-x", tt.argv, tt.opt); err != nil {
				t.Fatalf("Exec() error = %v", err)
			}
			if got := f.LastCommand(); got != tt.want {
				t.Errorf("command =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

// remote指定時はinstance名を remote: で修飾する（仕様 05-incus.md 5.6）
func TestRemoteQualification(t *testing.T) {
	f := &runnertest.Fake{}
	c := &incus.CLI{Runner: f, Project: "default", Remote: "dev-server"}

	if err := c.StartInstance(context.Background(), "dev-x"); err != nil {
		t.Fatalf("StartInstance() error = %v", err)
	}
	want := "incus start --project default dev-server:dev-x"
	if got := f.LastCommand(); got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestWaitReady(t *testing.T) {
	attempts := 0
	f := &runnertest.Fake{}
	f.Handler = func(runner.Command) (runner.Result, error) {
		attempts++
		if attempts < 3 {
			return runner.Result{ExitCode: 1}, errors.New("instance is not running")
		}
		return runner.Result{}, nil
	}
	c := newCLI(f)

	if err := c.WaitReady(context.Background(), "dev-x", incus.WaitOptions{
		Timeout:  2 * time.Second,
		Interval: time.Millisecond,
	}); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestWaitReadyTimeout(t *testing.T) {
	f := &runnertest.Fake{}
	f.Handler = func(runner.Command) (runner.Result, error) {
		return runner.Result{ExitCode: 1}, errors.New("not running")
	}
	c := newCLI(f)

	err := c.WaitReady(context.Background(), "dev-x", incus.WaitOptions{
		Timeout:  20 * time.Millisecond,
		Interval: time.Millisecond,
	})
	if err == nil {
		t.Fatal("WaitReady() = nil error, want timeout error")
	}
	if !strings.Contains(err.Error(), "dev-x") {
		t.Errorf("error = %q, instance名を含むこと", err.Error())
	}
}
