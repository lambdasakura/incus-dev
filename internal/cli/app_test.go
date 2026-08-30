package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lambdasakura/incus-dev/internal/cli"
	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/incus/incustest"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
)

const baseYAML = `
schema: 1
project:
  name: example-project
instance:
  image: images:ubuntu/24.04
`

// newApp はプロジェクトを作り、fakeで構成したAppを返す。
func newApp(t *testing.T, yamlBody string) (*cli.App, *incustest.Fake, *bytes.Buffer) {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(root, ".incus-dev", "dev.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	client := incustest.New()
	out := &bytes.Buffer{}
	app := cli.NewApp(cli.AppOptions{
		Config:  cfg,
		Client:  client,
		Runner:  &runnertest.Fake{},
		Out:     out,
		Verbose: false,
		// ホストの /etc/subuid に依存しないようにする
		CheckIDMap: func(int, int) error { return nil },
	})
	return app, client, out
}

func TestUpCreatesInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	inst, ok := client.Instances["dev-example-project"]
	if !ok {
		t.Fatalf("instanceが作成されていない: %v", client.Calls)
	}
	if inst.Status != "Running" {
		t.Errorf("Status = %q, want Running", inst.Status)
	}
	// 作成 → device → 起動 → ready待ち の順であること
	want := []string{"create", "devices", "start", "waitready"}
	var got []string
	for _, c := range client.Calls {
		for _, w := range want {
			if strings.HasPrefix(c, w) {
				got = append(got, w)
			}
		}
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("操作順が違う (-want +got):\n%s\ncalls=%v", diff, client.Calls)
	}
}

func TestUpMountsWorkspace(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	dev := client.Instances["dev-example-project"].Devices["workspace"]
	if dev["type"] != "disk" || dev["path"] != "/workspace" {
		t.Errorf("workspace device = %v", dev)
	}
	if dev["source"] == "" {
		t.Errorf("workspace device source が空")
	}
}

// 既存instanceは破壊しない（仕様 04-cli.md 4.1）
func TestUpDoesNotRecreateExistingInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Stopped",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if client.Called("create") {
		t.Errorf("既存instanceを作り直している: %v", client.Calls)
	}
	if client.Called("delete") {
		t.Errorf("既存instanceを削除している: %v", client.Calls)
	}
	if !client.Called("start") {
		t.Errorf("停止中のinstanceを起動していない: %v", client.Calls)
	}
}

func TestUpReappliesConfigToExistingInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
  config:
    limits.cpu: "16"
`)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			"user.incus-devkit.project": "example-project",
			"limits.cpu":                "4",
		},
	})

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if got := client.Instances["dev-example-project"].Config["limits.cpu"]; got != "16" {
		t.Errorf("limits.cpu = %q, want 16 (dev.ymlの変更を反映すること)", got)
	}
}

// devkit管理外のinstanceには触れない（仕様 05-incus.md 5.2）
func TestUpRefusesUnmanagedInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{Name: "dev-example-project", Status: "Running"})

	err := app.Up(context.Background())
	if err == nil {
		t.Fatal("Up() = nil error, 管理外instanceでは失敗すること")
	}
	if !strings.Contains(err.Error(), "dev-example-project") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestUpSetsManagedMarkers(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	cfg := client.Instances["dev-example-project"].Config
	if got := cfg["user.incus-devkit.project"]; got != "example-project" {
		t.Errorf("user.incus-devkit.project = %q", got)
	}
	if cfg["user.incus-devkit.root"] == "" {
		t.Errorf("user.incus-devkit.root が空")
	}
	if got := cfg["user.incus-devkit.schema"]; got != "1" {
		t.Errorf("user.incus-devkit.schema = %q, want 1", got)
	}
}

// 指定Profileが存在しなければ明示的に失敗する（仕様 05-incus.md 5.3）
func TestUpFailsWhenProfileMissing(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
  profiles:
    - default
    - gpu-nvidia
`)
	client.Profiles = []string{"default"}

	err := app.Up(context.Background())
	if err == nil {
		t.Fatal("Up() = nil error, 存在しないProfileでは失敗すること")
	}
	if !strings.Contains(err.Error(), "gpu-nvidia") {
		t.Errorf("error = %q, 不足しているProfile名を含むこと", err.Error())
	}
	if client.Called("create") {
		t.Error("Profile確認前にinstanceを作成している")
	}
}

func TestUpAppliesNoProfiles(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
  profiles: []
  devices:
    root:
      type: disk
      pool: default
      path: /
`)
	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !client.Called("create dev-example-project image=images:ubuntu/24.04 profiles=[] noprofiles=true") {
		t.Errorf("calls = %v, profiles: [] は --no-profiles に対応すること", client.Calls)
	}
}

func TestUpRunsProvisionSteps(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
provision:
  - run: echo provisioned
`)
	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	var found bool
	for _, argv := range client.Execs {
		if strings.Contains(strings.Join(argv, " "), "echo provisioned") {
			found = true
		}
	}
	if !found {
		t.Errorf("provisionステップが実行されていない: %v", client.Execs)
	}
}

// --- provision ---

func TestProvisionRequiresExistingInstance(t *testing.T) {
	app, _, _ := newApp(t, baseYAML)

	err := app.Provision(context.Background())
	if err == nil {
		t.Fatal("Provision() = nil error, instanceが無ければ失敗すること")
	}
	if !strings.Contains(err.Error(), "idev up") {
		t.Errorf("error = %q, idev up の案内を含むこと", err.Error())
	}
}

// provision はinstanceを作り直さない（仕様 04-cli.md 4.2）
func TestProvisionDoesNotCreateInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	_ = app.Provision(context.Background())
	if client.Called("create") {
		t.Errorf("calls = %v, provisionはinstanceを作成しないこと", client.Calls)
	}
}

func TestProvisionRunsStepsOnExistingInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
provision:
  - run: echo again
`)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	if err := app.Provision(context.Background()); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if client.Called("config ") {
		t.Errorf("calls = %v, provisionはinstance設定を変更しないこと", client.Calls)
	}
}

func TestProvisionStartsStoppedInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
provision:
  - run: echo hi
`)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Stopped",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	if err := app.Provision(context.Background()); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if !client.Called("start") {
		t.Errorf("calls = %v, 停止中なら起動すること", client.Calls)
	}
}

// --- destroy ---

func TestDestroyDeletesManagedInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	if err := app.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if _, ok := client.Instances["dev-example-project"]; ok {
		t.Error("instanceが削除されていない")
	}
}

func TestDestroyRefusesUnmanagedInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{Name: "dev-example-project", Status: "Running"})

	if err := app.Destroy(context.Background()); err == nil {
		t.Fatal("Destroy() = nil error, 管理外instanceは削除しないこと")
	}
	if _, ok := client.Instances["dev-example-project"]; !ok {
		t.Error("管理外instanceを削除している")
	}
}

func TestDestroyOnMissingInstanceIsAnError(t *testing.T) {
	app, _, _ := newApp(t, baseYAML)

	if err := app.Destroy(context.Background()); err == nil {
		t.Fatal("Destroy() = nil error, 対象が無ければ失敗すること")
	}
}

// --- status ---

func TestStatusOutput(t *testing.T) {
	app, client, out := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			"user.incus-devkit.project": "example-project",
			"image.description":         "ubuntu 24.04",
			"limits.cpu":                "8",
		},
	})

	if err := app.Status(context.Background(), false); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"example-project",
		"dev-example-project",
		"Running",
		"images:ubuntu/24.04",
		"/workspace",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status =\n%s\n%q を含むこと", text, want)
		}
	}
}

func TestStatusJSON(t *testing.T) {
	app, client, out := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	if err := app.Status(context.Background(), true); err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("JSON出力が不正: %v\n%s", err, out.String())
	}
	// 仕様 04-cli.md 4.12 の最低限のフィールド
	want := map[string]any{
		"project":  "example-project",
		"instance": "dev-example-project",
		"status":   "Running",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("json[%q] = %v, want %v", k, got[k], v)
		}
	}
	if got["workspace"] != "/workspace" {
		t.Errorf("json[workspace] = %v", got["workspace"])
	}
}

func TestStatusWhenInstanceMissing(t *testing.T) {
	app, _, out := newApp(t, baseYAML)

	if err := app.Status(context.Background(), false); err != nil {
		t.Fatalf("Status() error = %v, instanceが無くても成功すること", err)
	}
	if !strings.Contains(out.String(), "NOT CREATED") {
		t.Errorf("status =\n%s\n未作成であることを示すこと", out.String())
	}
}

// --- rebuild ---

func TestRebuildRecreatesInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	if err := app.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if !client.Called("delete") {
		t.Errorf("calls = %v, 削除していない", client.Calls)
	}
	if !client.Called("create") {
		t.Errorf("calls = %v, 作成していない", client.Calls)
	}
}

func TestRebuildWhenInstanceMissingJustCreates(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	if err := app.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if !client.Called("create") {
		t.Errorf("calls = %v", client.Calls)
	}
}

// --- shell ---

func TestShellRequiresRunningInstance(t *testing.T) {
	app, _, _ := newApp(t, baseYAML)

	if err := app.Shell(context.Background(), nil); err == nil {
		t.Fatal("Shell() = nil error, instanceが無ければ失敗すること")
	}
}

func TestShellExecutesInteractiveShell(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	if err := app.Shell(context.Background(), nil); err != nil {
		t.Fatalf("Shell() error = %v", err)
	}
	if len(client.Execs) == 0 {
		t.Fatal("execされていない")
	}
	if got := client.Execs[0][0]; got != "/bin/sh" && got != "/bin/bash" {
		t.Errorf("shell = %q", got)
	}
}

func TestShellRunsGivenCommand(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	if err := app.Shell(context.Background(), []string{"make", "test"}); err != nil {
		t.Fatalf("Shell() error = %v", err)
	}
	if diff := cmp.Diff([]string{"make", "test"}, client.Execs[0]); diff != "" {
		t.Errorf("argv mismatch (-want +got):\n%s", diff)
	}
}

// idmap: auto でrawが使えないホストでは、shiftへ退避して動作を継続する
func TestUpFallsBackToShiftWhenRawIDMapNotAllowed(t *testing.T) {
	cfg := loadTestConfig(t, baseYAML)
	client := incustest.New()
	errOut := &bytes.Buffer{}

	app := cli.NewApp(cli.AppOptions{
		Config:     cfg,
		Client:     client,
		Runner:     &runnertest.Fake{},
		Out:        &bytes.Buffer{},
		ErrOut:     errOut,
		CheckIDMap: func(int, int) error { return errors.New("subuid is not configured") },
	})

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v, shiftへ退避して継続すること", err)
	}

	dev := client.Instances["dev-example-project"].Devices["workspace"]
	if dev["shift"] != "true" {
		t.Errorf("workspace device = %v, shift=true を使うこと", dev)
	}
	if _, ok := client.Instances["dev-example-project"].Config["raw.idmap"]; ok {
		t.Error("raw.idmap を設定している")
	}
	// 退避したことと、より良い設定方法を利用者へ伝えること
	for _, want := range []string{"shift", "root:"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("warning = %q, %q を含むこと", errOut.String(), want)
		}
	}
}

// idmap: raw を明示した場合は、使えなければinstanceを作る前に失敗する
func TestUpFailsWhenExplicitRawIDMapNotAllowed(t *testing.T) {
	cfg := loadTestConfig(t, baseYAML+"workspace:\n  idmap: raw\n")
	client := incustest.New()

	app := cli.NewApp(cli.AppOptions{
		Config:     cfg,
		Client:     client,
		Runner:     &runnertest.Fake{},
		Out:        &bytes.Buffer{},
		CheckIDMap: func(int, int) error { return errors.New("subuid is not configured") },
	})

	err := app.Up(context.Background())
	if err == nil {
		t.Fatal("Up() = nil error, rawを明示した場合は失敗すること")
	}
	if !strings.Contains(err.Error(), "subuid") {
		t.Errorf("error = %q", err.Error())
	}
	if client.Called("create") {
		t.Errorf("calls = %v, 検査前にinstanceを作成している", client.Calls)
	}
}

// idmap: none の場合は検査しない
func TestUpSkipsIDMapCheckWhenDisabled(t *testing.T) {
	cfg := loadTestConfig(t, baseYAML+"workspace:\n  idmap: none\n")

	app := cli.NewApp(cli.AppOptions{
		Config:     cfg,
		Client:     incustest.New(),
		Runner:     &runnertest.Fake{},
		Out:        &bytes.Buffer{},
		CheckIDMap: func(int, int) error { return errors.New("must not be called") },
	})

	if err := app.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v, idmap: none では検査しないこと", err)
	}
}

// loadTestConfig は一時プロジェクトを作って設定を読み込む。
func loadTestConfig(t *testing.T, body string) *config.Config {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(root, ".incus-dev", "dev.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	return cfg
}
