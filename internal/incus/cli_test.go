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
		name      string
		spec      incus.InstanceSpec
		want      string
		wantStdin []string
	}{
		{
			name: "profileとconfig",
			spec: incus.InstanceSpec{
				Name:     "dev-example",
				Image:    "images:ubuntu/24.04",
				Profiles: []string{"default", "gpu"},
				Config:   map[string]string{"limits.memory": "16GiB", "limits.cpu": "8"},
			},
			want:      "incus create --project default images:ubuntu/24.04 dev-example -p default -p gpu",
			wantStdin: []string{"limits.cpu: \"8\"", "limits.memory: 16GiB"},
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
			// -d フラグは新規deviceを作成できないため、標準入力のYAMLで渡す
			name: "device付き",
			spec: incus.InstanceSpec{
				Name:  "dev-example",
				Image: "images:ubuntu/24.04",
				Devices: map[string]incus.Device{
					"workspace": {"type": "disk", "source": "/src", "path": "/workspace"},
				},
			},
			want:      "incus create --project default images:ubuntu/24.04 dev-example",
			wantStdin: []string{"workspace:", "type: disk", "source: /src", "path: /workspace"},
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
			for _, want := range tt.wantStdin {
				if !strings.Contains(f.LastStdin(), want) {
					t.Errorf("stdin =\n%s\n%q を含むこと", f.LastStdin(), want)
				}
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
	if got := f.LastArgv(); got != want {
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
	if got := f.LastArgv(); got != want {
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
	if got := f.LastArgv(); got != want {
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
			want: "incus exec --project default dev-x --cwd /workspace --env A=1 --env B=2 -T -- sh -c echo hi",
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
			if got := f.LastArgv(); got != tt.want {
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
	const noNIC = `[{"name":"dev-x","status":"Running","expanded_devices":{"root":{"type":"disk"}}}]`

	attempts := 0
	f := &runnertest.Fake{}
	f.Handler = func(c runner.Command) (runner.Result, error) {
		if c.Args[0] == "list" {
			return runner.Result{Stdout: []byte(noNIC)}, nil
		}
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

// incus の出力が壊れている場合はパースエラーとして報告する
func TestInstanceReportsMalformedJSON(t *testing.T) {
	f := &runnertest.Fake{Stdout: map[string]string{"incus list": "not json"}}

	_, err := newCLI(f).Instance(context.Background(), "dev-x")
	if err == nil || !strings.Contains(err.Error(), "parse instance list") {
		t.Errorf("error = %v", err)
	}
}

func TestProfileExistsReportsMalformedJSON(t *testing.T) {
	f := &runnertest.Fake{Stdout: map[string]string{"incus profile list": "{"}}

	_, err := newCLI(f).ProfileExists(context.Background(), "default")
	if err == nil || !strings.Contains(err.Error(), "parse profile list") {
		t.Errorf("error = %v", err)
	}
}

// incus コマンド自体が失敗した場合、そのまま伝播すること
func TestCommandFailuresPropagate(t *testing.T) {
	tests := []struct {
		name string
		call func(*incus.CLI) error
	}{
		{"list", func(c *incus.CLI) error { _, err := c.Instance(context.Background(), "dev-x"); return err }},
		{"start", func(c *incus.CLI) error { return c.StartInstance(context.Background(), "dev-x") }},
		{"stop", func(c *incus.CLI) error { return c.StopInstance(context.Background(), "dev-x") }},
		{"delete", func(c *incus.CLI) error { return c.DeleteInstance(context.Background(), "dev-x") }},
		{"create", func(c *incus.CLI) error {
			return c.CreateInstance(context.Background(), incus.InstanceSpec{Name: "dev-x", Image: "i"})
		}},
		{"config set", func(c *incus.CLI) error {
			return c.ApplyConfig(context.Background(), "dev-x", map[string]string{"a": "b"})
		}},
		{"config unset", func(c *incus.CLI) error {
			return c.UnsetConfig(context.Background(), "dev-x", []string{"a"})
		}},
		{"profile list", func(c *incus.CLI) error { _, err := c.ProfileExists(context.Background(), "p"); return err }},
		{"exec", func(c *incus.CLI) error {
			_, err := c.Exec(context.Background(), "dev-x", []string{"true"}, incus.ExecOptions{})
			return err
		}},
	}

	wantErr := errors.New("incus failed")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &runnertest.Fake{Err: map[string]error{"incus": wantErr}}

			if err := tt.call(newCLI(f)); !errors.Is(err, wantErr) {
				t.Errorf("error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestUnsetConfig(t *testing.T) {
	f := &runnertest.Fake{}

	if err := newCLI(f).UnsetConfig(context.Background(), "dev-x", []string{"raw.idmap"}); err != nil {
		t.Fatalf("UnsetConfig() error = %v", err)
	}
	want := "incus config unset --project default dev-x raw.idmap"
	if got := f.LastArgv(); got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestUnsetConfigEmptyIsNoop(t *testing.T) {
	f := &runnertest.Fake{}

	if err := newCLI(f).UnsetConfig(context.Background(), "dev-x", nil); err != nil {
		t.Fatalf("UnsetConfig() error = %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("commands = %v, 空なら実行しないこと", f.Commands())
	}
}

// ユーザー名は incus exec --user へ渡せない（UIDのみ）
func TestExecRejectsNonNumericUser(t *testing.T) {
	f := &runnertest.Fake{}

	_, err := newCLI(f).Exec(context.Background(), "dev-x", []string{"true"}, incus.ExecOptions{User: "developer"})
	if err == nil || !strings.Contains(err.Error(), "numeric uid") {
		t.Errorf("error = %v", err)
	}
}

func TestApplyDevicesPropagatesLookupError(t *testing.T) {
	f := &runnertest.Fake{Err: map[string]error{"incus list": errors.New("nope")}}

	err := newCLI(f).ApplyDevices(context.Background(), "dev-x", map[string]incus.Device{
		"data": {"type": "disk"},
	})
	if err == nil {
		t.Error("error = nil, want error")
	}
}

func TestApplyDevicesEmptyIsNoop(t *testing.T) {
	f := &runnertest.Fake{}

	if err := newCLI(f).ApplyDevices(context.Background(), "dev-x", nil); err != nil {
		t.Fatalf("ApplyDevices() error = %v", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("commands = %v", f.Commands())
	}
}

func TestWaitReadyStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := &runnertest.Fake{Err: map[string]error{"incus": errors.New("not running")}}
	err := newCLI(f).WaitReady(ctx, "dev-x", incus.WaitOptions{
		Timeout:  time.Minute,
		Interval: time.Millisecond,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestInstanceIsRunning(t *testing.T) {
	if !(&incus.Instance{Status: "Running"}).IsRunning() {
		t.Error("Running を実行中と判定すること")
	}
	if (&incus.Instance{Status: "Stopped"}).IsRunning() {
		t.Error("Stopped を実行中と判定しないこと")
	}
}

func TestApplyDevicesPropagatesCommandErrors(t *testing.T) {
	existing := `[{"name":"dev-x","devices":{"data":{"type":"disk","source":"/old"}}}]`

	tests := []struct {
		name   string
		prefix string
		want   map[string]incus.Device
	}{
		{"追加の失敗", "incus config device add", map[string]incus.Device{"new": {"type": "nic"}}},
		{"更新の失敗", "incus config device set", map[string]incus.Device{"data": {"type": "disk", "source": "/new"}}},
		{"削除の失敗", "incus config device remove", map[string]incus.Device{"data": {"type": "proxy"}}},
	}

	wantErr := errors.New("device failed")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &runnertest.Fake{
				Stdout: map[string]string{"incus list": existing},
				Err:    map[string]error{tt.prefix: wantErr},
			}

			if err := newCLI(f).ApplyDevices(context.Background(), "dev-x", tt.want); !errors.Is(err, wantErr) {
				t.Errorf("error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestInstanceExistsPropagatesError(t *testing.T) {
	wantErr := errors.New("incus failed")
	f := &runnertest.Fake{Err: map[string]error{"incus list": wantErr}}

	if _, err := newCLI(f).InstanceExists(context.Background(), "dev-x"); !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
}

// remote指定時は profile list にもremoteを渡す（渡さないとローカルを見てしまう）
func TestProfileExistsUsesRemote(t *testing.T) {
	f := &runnertest.Fake{Stdout: map[string]string{"incus profile list": `[{"name":"default"}]`}}
	c := &incus.CLI{Runner: f, Project: "default", Remote: "dev-server"}

	if _, err := c.ProfileExists(context.Background(), "default"); err != nil {
		t.Fatalf("ProfileExists() error = %v", err)
	}
	want := "incus profile list --project default --format json dev-server:"
	if got := f.LastArgv(); got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
}

func TestWaitReadyUsesDefaults(t *testing.T) {
	f := &runnertest.Fake{Stdout: map[string]string{
		"incus list": `[{"name":"dev-x","status":"Running","expanded_devices":{"root":{"type":"disk"}}}]`,
	}}

	// 既定値でも即座に成功すること（fakeは成功を返す）
	if err := newCLI(f).WaitReady(context.Background(), "dev-x", incus.WaitOptions{}); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
}

// instanceが起動しても、ネットワークが使えるまでには間がある。
// パッケージ導入を伴うステップが初回から失敗しないよう、割り当てを待つ。
func TestWaitReadyWaitsForNetworkAddress(t *testing.T) {
	const noAddress = `[{"name":"dev-x","status":"Running",` +
		`"expanded_devices":{"eth0":{"type":"nic"}},` +
		`"state":{"network":{"lo":{"addresses":[{"family":"inet","address":"127.0.0.1","scope":"local"}]}}}}]`
	const withAddress = `[{"name":"dev-x","status":"Running",` +
		`"expanded_devices":{"eth0":{"type":"nic"}},` +
		`"state":{"network":{"eth0":{"addresses":[{"family":"inet","address":"10.0.0.2","scope":"global"}]}}}}]`

	calls := 0
	f := &runnertest.Fake{}
	f.Handler = func(c runner.Command) (runner.Result, error) {
		if c.Args[0] != "list" {
			return runner.Result{}, nil // exec は成功する
		}
		calls++
		if calls < 3 {
			return runner.Result{Stdout: []byte(noAddress)}, nil
		}
		return runner.Result{Stdout: []byte(withAddress)}, nil
	}

	err := newCLI(f).WaitReady(context.Background(), "dev-x", incus.WaitOptions{
		Timeout:  2 * time.Second,
		Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if calls < 3 {
		t.Errorf("list呼び出し = %d回, アドレスが付くまで待つこと", calls)
	}
}

// アドレスが付かない場合は、失敗ではなく判別可能なエラーで知らせる。
// 静的設定などアドレスが現れない構成もあるため、呼び出し側が判断できるようにする。
func TestWaitReadyReportsNetworkNotReady(t *testing.T) {
	const noAddress = `[{"name":"dev-x","status":"Running",` +
		`"expanded_devices":{"eth0":{"type":"nic"}},"state":{"network":{}}}]`

	f := &runnertest.Fake{Stdout: map[string]string{"incus list": noAddress}}

	err := newCLI(f).WaitReady(context.Background(), "dev-x", incus.WaitOptions{
		Timeout:        time.Second,
		Interval:       time.Millisecond,
		NetworkTimeout: 10 * time.Millisecond,
	})
	if !errors.Is(err, incus.ErrNetworkNotReady) {
		t.Errorf("error = %v, want ErrNetworkNotReady", err)
	}
}

// NICを持たないinstanceではネットワークを待たない
func TestWaitReadySkipsNetworkWithoutNIC(t *testing.T) {
	const noNIC = `[{"name":"dev-x","status":"Running",` +
		`"expanded_devices":{"root":{"type":"disk"}},"state":{"network":{}}}]`

	f := &runnertest.Fake{Stdout: map[string]string{"incus list": noNIC}}

	err := newCLI(f).WaitReady(context.Background(), "dev-x", incus.WaitOptions{
		Timeout:        time.Second,
		Interval:       time.Millisecond,
		NetworkTimeout: time.Minute, // 待つならタイムアウトするはず
	})
	if err != nil {
		t.Errorf("WaitReady() error = %v, NICが無ければ待たないこと", err)
	}
}

// IPv6(ULA)が先に付く環境で、IPv4の割り当てまで待つこと。
// IPv6だけの時点ではデフォルトルートが無く、外部へ出られない。
func TestWaitReadyWaitsForIPv4(t *testing.T) {
	const ipv6Only = `[{"name":"dev-x","status":"Running",` +
		`"expanded_devices":{"eth0":{"type":"nic"}},` +
		`"state":{"network":{"eth0":{"addresses":[` +
		`{"family":"inet6","address":"fd42::1","scope":"global"}]}}}}]`
	const dualStack = `[{"name":"dev-x","status":"Running",` +
		`"expanded_devices":{"eth0":{"type":"nic"}},` +
		`"state":{"network":{"eth0":{"addresses":[` +
		`{"family":"inet6","address":"fd42::1","scope":"global"},` +
		`{"family":"inet","address":"10.0.0.2","scope":"global"}]}}}}]`

	calls := 0
	f := &runnertest.Fake{}
	f.Handler = func(c runner.Command) (runner.Result, error) {
		if c.Args[0] != "list" {
			return runner.Result{}, nil
		}
		calls++
		if calls < 3 {
			return runner.Result{Stdout: []byte(ipv6Only)}, nil
		}
		return runner.Result{Stdout: []byte(dualStack)}, nil
	}

	err := newCLI(f).WaitReady(context.Background(), "dev-x", incus.WaitOptions{
		Timeout:   2 * time.Second,
		Interval:  time.Millisecond,
		IPv4Grace: time.Second,
	})
	if err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if calls < 3 {
		t.Errorf("list呼び出し = %d回, IPv4が付くまで待つこと", calls)
	}
}

// IPv6しか付かない環境では、猶予時間を過ぎたら先へ進む
func TestWaitReadyProceedsWithIPv6Only(t *testing.T) {
	const ipv6Only = `[{"name":"dev-x","status":"Running",` +
		`"expanded_devices":{"eth0":{"type":"nic"}},` +
		`"state":{"network":{"eth0":{"addresses":[` +
		`{"family":"inet6","address":"fd42::1","scope":"global"}]}}}}]`

	f := &runnertest.Fake{Stdout: map[string]string{"incus list": ipv6Only}}

	start := time.Now()
	err := newCLI(f).WaitReady(context.Background(), "dev-x", incus.WaitOptions{
		Timeout:        time.Second,
		Interval:       time.Millisecond,
		NetworkTimeout: time.Minute,
		IPv4Grace:      20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WaitReady() error = %v, IPv6のみでも進むこと", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("elapsed = %v, 猶予時間を超えて待たないこと", elapsed)
	}
}

func TestWaitReadyTimesOutWithoutError(t *testing.T) {
	// 非0終了だがエラーは返さない実装でも、メッセージが壊れないこと
	f := &runnertest.Fake{}
	f.Handler = func(runner.Command) (runner.Result, error) {
		return runner.Result{ExitCode: 1}, nil
	}

	err := newCLI(f).WaitReady(context.Background(), "dev-x", incus.WaitOptions{
		Timeout:  10 * time.Millisecond,
		Interval: time.Millisecond,
	})
	if err == nil {
		t.Fatal("WaitReady() = nil error, want timeout error")
	}
	if strings.Contains(err.Error(), "%!w") {
		t.Errorf("error = %q, nilを%%wへ渡さないこと", err.Error())
	}
}

func TestWaitReadyPropagatesInstanceError(t *testing.T) {
	wantErr := errors.New("list failed")
	f := &runnertest.Fake{Err: map[string]error{"incus list": wantErr}}

	err := newCLI(f).WaitReady(context.Background(), "dev-x", incus.WaitOptions{
		Timeout:  time.Second,
		Interval: time.Millisecond,
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
}

func TestWaitNetworkStopsOnContextCancel(t *testing.T) {
	const noAddress = `[{"name":"dev-x","status":"Running",` +
		`"expanded_devices":{"eth0":{"type":"nic"}},"state":{"network":{}}}]`

	ctx, cancel := context.WithCancel(context.Background())
	f := &runnertest.Fake{}
	f.Handler = func(c runner.Command) (runner.Result, error) {
		if c.Args[0] == "list" {
			cancel() // 1回目の状態取得後に中断する
			return runner.Result{Stdout: []byte(noAddress)}, nil
		}
		return runner.Result{}, nil
	}

	err := newCLI(f).WaitReady(ctx, "dev-x", incus.WaitOptions{
		Timeout:        time.Second,
		NetworkTimeout: time.Minute,
		Interval:       10 * time.Millisecond,
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestInstanceAddressHelpers(t *testing.T) {
	// 状態が取得できていない場合
	var empty incus.Instance
	if empty.HasGlobalAddress() || empty.HasIPv4Address() {
		t.Error("状態が無い場合はアドレス無しとすること")
	}

	inst := incus.Instance{
		State: &incus.InstanceState{Network: map[string]incus.NetworkState{
			// ループバックとリンクローカルは対象外
			"lo":   {Addresses: []incus.NetworkAddress{{Family: "inet", Address: "127.0.0.1", Scope: "local"}}},
			"eth0": {Addresses: []incus.NetworkAddress{{Family: "inet6", Address: "fe80::1", Scope: "link"}}},
		}},
	}
	if inst.HasGlobalAddress() {
		t.Error("グローバルでないアドレスを数えないこと")
	}
}
