package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus/incustest"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/provision"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner/runnertest"
)

var errBoom = errors.New("boom")

// errWriter は必ず書き込みに失敗する io.Writer。
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errBoom }

// appWith は指定の設定とfakeクライアントでAppを構成する。
func appWith(t *testing.T, body string, client *incustest.Fake) *App {
	t.Helper()

	cfg, err := config.Parse([]byte(body), config.Options{})
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	cfg.Root = t.TempDir()

	return NewApp(AppOptions{
		Config:     cfg,
		Client:     client,
		Runner:     &runnertest.Fake{},
		Out:        &bytes.Buffer{},
		CheckIDMap: func(int, int) error { return nil },
	})
}

// managed は当該プロジェクトのdevkit管理下instanceを登録する。
func managed(client *incustest.Fake, status string) *incustest.Fake {
	return client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: status,
		Config: map[string]string{managedProjectKey: "example-project"},
	})
}

// Incus操作が失敗した場合、その原因が呼び出し元まで伝わること
func TestUpPropagatesIncusErrors(t *testing.T) {
	tests := []struct {
		name    string
		failOn  string
		managed bool
	}{
		{"profile確認の失敗", "profile", false},
		{"instance取得の失敗", "instance", false},
		{"instance作成の失敗", "create", false},
		{"device適用の失敗", "devices", true},
		{"起動の失敗", "start", false},
		{"ready待ちの失敗", "waitready", false},
		{"config再適用の失敗", "config", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := incustest.New()
			client.FailOn = map[string]error{tt.failOn: errBoom}
			if tt.managed {
				managed(client, "Running")
			}

			err := appWith(t, rootYAML, client).Up(context.Background())
			if !errors.Is(err, errBoom) {
				t.Errorf("error = %v, want %v", err, errBoom)
			}
		})
	}
}

func TestProvisionPropagatesErrors(t *testing.T) {
	tests := []struct {
		name   string
		failOn string
	}{
		{"instance取得の失敗", "instance"},
		{"ready待ちの失敗", "waitready"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := incustest.New()
			managed(client, "Running")
			client.FailOn = map[string]error{tt.failOn: errBoom}

			if err := appWith(t, rootYAML, client).Provision(context.Background(), provision.Selection{}); !errors.Is(err, errBoom) {
				t.Errorf("error = %v, want %v", err, errBoom)
			}
		})
	}
}

func TestProvisionPropagatesStartError(t *testing.T) {
	client := incustest.New()
	managed(client, "Stopped")
	client.FailOn = map[string]error{"start": errBoom}

	if err := appWith(t, rootYAML, client).Provision(context.Background(), provision.Selection{}); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

func TestProvisionPropagatesStepError(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")
	client.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) { return 1, errBoom }

	err := appWith(t, rootYAML+"provision:\n  - run: failing\n", client).Provision(context.Background(), provision.Selection{})
	if !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

func TestBootstrapErrorStopsProvisioning(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")
	client.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) { return 1, errBoom }

	body := rootYAML + "bootstrap:\n  - run: failing-bootstrap\nprovision:\n  - run: never\n"
	err := appWith(t, body, client).Provision(context.Background(), provision.Selection{})

	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want %v", err, errBoom)
	}
	if !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("error = %q, bootstrapでの失敗と分かること", err.Error())
	}
	for _, argv := range client.Execs {
		if strings.Contains(strings.Join(argv, " "), "never") {
			t.Error("bootstrap失敗後にprovisionを実行している")
		}
	}
}

func TestDestroyPropagatesErrors(t *testing.T) {
	for _, failOn := range []string{"instance", "delete"} {
		t.Run(failOn, func(t *testing.T) {
			client := incustest.New()
			managed(client, "Running")
			client.FailOn = map[string]error{failOn: errBoom}

			if err := appWith(t, rootYAML, client).Destroy(context.Background()); !errors.Is(err, errBoom) {
				t.Errorf("error = %v, want %v", err, errBoom)
			}
		})
	}
}

func TestRebuildPropagatesLookupError(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")
	client.FailOn = map[string]error{"instance": errBoom}

	if err := appWith(t, rootYAML, client).Rebuild(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

func TestRebuildPropagatesDestroyError(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")
	client.FailOn = map[string]error{"delete": errBoom}

	if err := appWith(t, rootYAML, client).Rebuild(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

func TestShellPropagatesErrors(t *testing.T) {
	t.Run("instanceが管理外", func(t *testing.T) {
		client := incustest.New().AddInstance(&incus.Instance{Name: "dev-example-project", Status: "Running"})

		if err := appWith(t, rootYAML, client).Shell(context.Background(), nil); err == nil {
			t.Error("error = nil, want error")
		}
	})

	t.Run("起動に失敗", func(t *testing.T) {
		client := incustest.New()
		managed(client, "Stopped")
		client.FailOn = map[string]error{"start": errBoom}

		if err := appWith(t, rootYAML, client).Shell(context.Background(), nil); !errors.Is(err, errBoom) {
			t.Errorf("error = %v, want %v", err, errBoom)
		}
	})

	t.Run("実行そのものが失敗", func(t *testing.T) {
		client := incustest.New()
		managed(client, "Running")
		client.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) { return 0, errBoom }

		err := appWith(t, rootYAML, client).Shell(context.Background(), []string{"true"})
		if !errors.Is(err, errBoom) {
			t.Errorf("error = %v, want %v", err, errBoom)
		}
	})
}

func TestStatusPropagatesLookupError(t *testing.T) {
	client := incustest.New()
	client.FailOn = map[string]error{"instance": errBoom}

	if err := appWith(t, rootYAML, client).Status(context.Background(), false); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

func TestStatusReportsWriteError(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	for _, asJSON := range []bool{false, true} {
		app := NewApp(AppOptions{
			Config:     cfg,
			Client:     incustest.New(),
			Runner:     &runnertest.Fake{},
			Out:        errWriter{},
			CheckIDMap: func(int, int) error { return nil },
		})
		if err := app.Status(context.Background(), asJSON); !errors.Is(err, errBoom) {
			t.Errorf("json=%v: error = %v, want %v", asJSON, err, errBoom)
		}
	}
}

func TestValidateReportsWriteError(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(AppOptions{
		Config:     cfg,
		Client:     incustest.New(),
		Runner:     &runnertest.Fake{},
		Out:        errWriter{},
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Validate(); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// status は既存instanceのProfileとlimitsも表示する
func TestStatusShowsInstanceDetails(t *testing.T) {
	out := &bytes.Buffer{}
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	client := incustest.New().AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Stopped",
		Profiles: []string{"default", "gpu"},
		Config: map[string]string{
			"limits.cpu":    "8",
			"limits.memory": "16GiB",
			"image.os":      "ubuntu",
		},
	})
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Status(context.Background(), false); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{"default, gpu", "Managed:    no", "limits.cpu", "16GiB"} {
		if !strings.Contains(text, want) {
			t.Errorf("status =\n%s\n%q を含むこと", text, want)
		}
	}
	if strings.Contains(text, "image.os") {
		t.Errorf("status = %q, limits以外のconfigは表示しないこと", text)
	}
}

// instance.config に raw.idmap が明示されている場合、devkitは対応付けに介入しない
func TestIDMapModeRespectsExplicitRawIDMap(t *testing.T) {
	client := incustest.New()
	body := rootYAML + "  config:\n    raw.idmap: \"both 1234 0\"\n"

	app := appWith(t, body, client)
	app.checkIDMap = func(int, int) error { return errBoom }

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v, 明示指定時は検査しないこと", err)
	}
	if got := client.Instances["dev-example-project"].Config["raw.idmap"]; got != "both 1234 0" {
		t.Errorf("raw.idmap = %q", got)
	}
}

func TestExitCodeErrorMessage(t *testing.T) {
	err := &ExitCodeError{Code: 42}

	if got := err.Error(); !strings.Contains(got, "42") {
		t.Errorf("Error() = %q, 終了コードを含むこと", got)
	}
}

func TestNewAppDefaultsWriters(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Out / ErrOut を省略しても落ちないこと
	app := NewApp(AppOptions{Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{}})
	if err := app.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}
}

// shift から raw へ切り替えた場合、古い設定が残らないこと
func TestUpCleansStaleIDMapConfig(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Stopped",
		Config: map[string]string{
			managedProjectKey: "example-project",
			idmapConfigKey:    "uid 1000 0\ngid 1000 0",
		},
		Devices: map[string]incus.Device{
			"workspace": {"type": "disk", "path": "/workspace", "shift": "true"},
		},
	})

	app := appWith(t, rootYAML+"workspace:\n  idmap: shift\n", client)
	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	inst := client.Instances["dev-example-project"]
	if _, ok := inst.Config[idmapConfigKey]; ok {
		t.Errorf("raw.idmap が残っている: %q", inst.Config[idmapConfigKey])
	}
	if got := inst.Devices["workspace"]["shift"]; got != "true" {
		t.Errorf("shift = %q, want true", got)
	}
}

func TestUpDisablesShiftWhenSwitchingToRaw(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Stopped",
		Config: map[string]string{managedProjectKey: "example-project"},
		Devices: map[string]incus.Device{
			"workspace": {"type": "disk", "path": "/workspace", "shift": "true"},
		},
	})

	app := appWith(t, rootYAML+"workspace:\n  idmap: raw\n", client)
	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	inst := client.Instances["dev-example-project"]
	if got := inst.Devices["workspace"]["shift"]; got != "false" {
		t.Errorf("shift = %q, want false (切り替え時に無効化すること)", got)
	}
	if !strings.HasPrefix(inst.Config[idmapConfigKey], "uid ") {
		t.Errorf("raw.idmap = %q", inst.Config[idmapConfigKey])
	}
}

// 稼働中instanceで再起動を要する変更をした場合は警告する（仕様 05-incus.md 5.4.5）
func TestUpWarnsWhenRestartRequired(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
	})

	cfg, err := config.Parse([]byte(rootYAML+"  config:\n    security.nesting: \"true\"\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut,
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	out := errOut.String()
	if !strings.Contains(out, "security.nesting") || !strings.Contains(out, "restart") {
		t.Errorf("warning = %q, 再起動が必要である旨を伝えること", out)
	}
}

func TestUpDoesNotWarnWhenStopped(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Stopped",
		Config: map[string]string{managedProjectKey: "example-project"},
	})

	cfg, err := config.Parse([]byte(rootYAML+"  config:\n    security.nesting: \"true\"\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut,
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart") {
		t.Errorf("停止中は再起動の警告を出さないこと: %q", errOut.String())
	}
}

// ready待ちに失敗した場合、provisionへ進まない（仕様 06-provisioning.md 6.2）
func TestUpFailsWhenInstanceNotReady(t *testing.T) {
	client := incustest.New()
	client.FailReady = true

	err := appWith(t, rootYAML+"provision:\n  - run: never\n", client).Up(context.Background())
	if err == nil {
		t.Fatal("Up() = nil error, ready待ちの失敗を報告すること")
	}
	if !strings.Contains(err.Error(), "ready") {
		t.Errorf("error = %q", err.Error())
	}
	if len(client.Execs) != 0 {
		t.Errorf("execs = %v, ready前にステップを実行しないこと", client.Execs)
	}
}

// ansibleステップの失敗も位置と名前で特定できること
func TestAnsibleStepFailureIdentifiesStep(t *testing.T) {
	root := t.TempDir()
	playbook := filepath.Join(root, ".incus-dev", "ansible", "site.yml")
	if err := os.MkdirAll(filepath.Dir(playbook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(playbook, []byte("---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Parse([]byte(rootYAML+`
provision:
  - run: "true"
  - name: playbook step
    ansible:
      playbook: .incus-dev/ansible/site.yml
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = root

	client := incustest.New()
	managed(client, "Running")

	cmdRunner := &runnertest.Fake{Err: map[string]error{"ansible-playbook": errBoom}}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: cmdRunner,
		Out: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err = app.Provision(context.Background(), provision.Selection{})
	if err == nil {
		t.Fatal("Provision() = nil error, want error")
	}
	for _, want := range []string{"playbook step", "2/2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, %q を含むこと", err.Error(), want)
		}
	}
}

// idmap方式の切り替えで unset に失敗した場合、その原因が伝わること
func TestUpPropagatesUnsetError(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Stopped",
		Config: map[string]string{
			managedProjectKey: "example-project",
			idmapConfigKey:    "uid 1000 0",
		},
	})
	client.FailOn = map[string]error{"unset": errBoom}

	err := appWith(t, rootYAML+"workspace:\n  idmap: shift\n", client).Up(context.Background())
	if !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// 稼働中でも再起動を要する変更が無ければ警告しない
func TestUpDoesNotWarnWithoutRestartRequiredChanges(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			idmapConfigKey:    fmt.Sprintf("uid %d 0\ngid %d 0", os.Getuid(), os.Getgid()),
		},
	})

	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut,
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart") {
		t.Errorf("変更が無いのに警告している: %q", errOut.String())
	}
}

// devkitが設定した raw.idmap を取り消す場合も再起動が必要である
func TestUpWarnsWhenIDMapRemoved(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			idmapConfigKey:    "uid 1000 0",
		},
	})

	cfg, err := config.Parse([]byte(rootYAML+"workspace:\n  idmap: shift\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut,
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(errOut.String(), idmapConfigKey) {
		t.Errorf("warning = %q", errOut.String())
	}
}

// up の provision 失敗も伝播すること
func TestUpPropagatesProvisionError(t *testing.T) {
	client := incustest.New()
	client.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) { return 1, errBoom }

	err := appWith(t, rootYAML+"provision:\n  - run: failing\n", client).Up(context.Background())
	if !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// shell はコンテナ内コマンドの終了コードを ExitCodeError として返す
func TestShellConvertsExitErrorToExitCode(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")
	client.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) {
		return 0, &runner.ExitError{Cmd: "incus exec", ExitCode: 42}
	}

	err := appWith(t, rootYAML, client).Shell(context.Background(), []string{"false"})

	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != 42 {
		t.Errorf("error = %v, want ExitCodeError{42}", err)
	}
}

// Profile が空の場合、status の該当行を出さない
func TestStatusSkipsEmptyRows(t *testing.T) {
	out := &bytes.Buffer{}
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	client := incustest.New().AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
	})
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Status(context.Background(), false); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if strings.Contains(out.String(), "Profiles:") {
		t.Errorf("status = %q, 空の項目は表示しないこと", out.String())
	}
}

// 起動前の状態取得に失敗した場合
func TestUpPropagatesEnsureRunningLookupError(t *testing.T) {
	client := incustest.New()
	calls := 0
	client.Hook = func(call string) error {
		if !strings.HasPrefix(call, "instance") {
			return nil
		}
		calls++
		if calls >= 2 {
			return errBoom
		}
		return nil
	}

	if err := appWith(t, rootYAML, client).Up(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// idev shell で利用者が入力したコマンドは、診断のため表示してよい
func TestShellCommandIsNotRedacted(t *testing.T) {
	cmdRunner := &runnertest.Fake{}
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	client := &incus.CLI{Runner: cmdRunner, Project: "default"}
	cmdRunner.Stdout = map[string]string{
		"incus list": `[{"name":"dev-example-project","status":"Running",` +
			`"config":{"user.incus-devkit.project":"example-project"}}]`,
	}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: cmdRunner,
		Out: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})
	if err := app.Shell(context.Background(), []string{"make", "test"}); err != nil {
		t.Fatalf("Shell() error = %v", err)
	}

	if got := cmdRunner.LastCommand(); !strings.Contains(got, "make test") {
		t.Errorf("表示 = %q, 利用者が入力したコマンドは表示すること", got)
	}
}

// 再起動が必要なキーの一覧を取り違えないこと（仕様 05-incus.md 5.4.5）
func TestRestartRequiredKeys(t *testing.T) {
	for _, key := range []string{"raw.idmap", "security.nesting", "security.privileged"} {
		t.Run(key, func(t *testing.T) {
			client := incustest.New()
			client.AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: map[string]string{managedProjectKey: "example-project"},
			})

			cfg, err := config.Parse([]byte(rootYAML+"  config:\n    "+key+": \"changed\"\n"), config.Options{})
			if err != nil {
				t.Fatal(err)
			}
			cfg.Root = t.TempDir()

			errOut := &bytes.Buffer{}
			app := NewApp(AppOptions{
				Config: cfg, Client: client, Runner: &runnertest.Fake{},
				Out: &bytes.Buffer{}, ErrOut: errOut,
				CheckIDMap: func(int, int) error { return nil },
			})

			if err := app.Up(context.Background()); err != nil {
				t.Fatalf("Up() error = %v", err)
			}
			if !strings.Contains(errOut.String(), key) {
				t.Errorf("warning = %q, %q の変更を警告すること", errOut.String(), key)
			}
		})
	}
}

// 宣言から消えたキーは unset しないため、変更として警告しない。
// そうしないと何もしていないのに警告が出続ける。
func TestNoWarningForKeysRemovedFromConfig(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey:     "example-project",
			"security.nesting":    "true",
			"security.privileged": "true",
			// devkitが設定する値と同じ = 変更なし
			idmapConfigKey: fmt.Sprintf("uid %d 0\ngid %d 0", os.Getuid(), os.Getgid()),
		},
	})

	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut,
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart") {
		t.Errorf("warning = %q, 変更していないキーを警告しないこと", errOut.String())
	}
}

// ネットワークが割り当てられなくても、provisionは実行する。
// アドレスが現れない構成もあるため、ここで止めると回避手段が無くなる。
func TestUpContinuesWhenNetworkNotReady(t *testing.T) {
	client := incustest.New()
	client.NetworkNotReady = true

	cfg, err := config.Parse([]byte(rootYAML+"provision:\n  - run: echo hi\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut,
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v, 警告に留めて続行すること", err)
	}
	if !strings.Contains(errOut.String(), "network") {
		t.Errorf("warning = %q, ネットワーク未割り当てを伝えること", errOut.String())
	}
	if len(client.Execs) == 0 {
		t.Error("provisionステップが実行されていない")
	}
}

// 解決できないステップ指定は、instanceに触れる前に弾く
func TestProvisionRejectsUnknownStepBeforeTouchingInstance(t *testing.T) {
	client := incustest.New()

	err := appWith(t, rootYAML+"provision:\n  - name: only-one\n    run: \"true\"\n", client).
		Provision(context.Background(), provision.Selection{Only: []string{"nope"}})
	if err == nil {
		t.Fatal("Provision() = nil error, want error")
	}
	if len(client.Calls) != 0 {
		t.Errorf("calls = %v, 指定を解決できない場合はIncusへ触れないこと", client.Calls)
	}
}

func TestListSteps(t *testing.T) {
	out := &bytes.Buffer{}
	cfg, err := config.Parse([]byte(rootYAML+"provision:\n  - name: named\n    run: \"true\"\n  - run: \"true\"\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	app := NewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
		Out: out, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.ListSteps(); err != nil {
		t.Fatalf("ListSteps() error = %v", err)
	}
	for _, want := range []string{"1", "named", "2", "step 2"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, %q を含むこと", out.String(), want)
		}
	}
}

func TestListStepsWithoutSteps(t *testing.T) {
	out := &bytes.Buffer{}
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	app := NewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{}, Out: out,
	})

	if err := app.ListSteps(); err != nil {
		t.Fatalf("ListSteps() error = %v", err)
	}
	if !strings.Contains(out.String(), "no provision steps") {
		t.Errorf("output = %q", out.String())
	}
}

func TestListStepsWriteError(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML+"provision:\n  - run: \"true\"\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{}, Out: errWriter{},
	})

	if err := app.ListSteps(); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}

	empty, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	app = NewApp(AppOptions{
		Config: empty, Client: incustest.New(), Runner: &runnertest.Fake{}, Out: errWriter{},
	})
	if err := app.ListSteps(); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}
