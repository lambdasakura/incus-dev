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

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/cli"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus/incustest"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/provision"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner/runnertest"
)

const baseYAML = `
schema: 1
project:
  name: example-project
instance:
  image: images:ubuntu/24.04
`

// newApp creates a project and returns an App built on fakes.
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
		// Do not depend on the host's /etc/subuid.
		CheckIDMap:   func(int, int) error { return nil },
		Remote:       "local",
		IncusProject: "default",
	})
	return app, client, out
}

func TestUpCreatesInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	inst, ok := client.Instances["dev-example-project"]
	if !ok {
		t.Fatalf("no instance was created: %v", client.Calls)
	}
	if inst.Status != "Running" {
		t.Errorf("Status = %q, want Running", inst.Status)
	}
	// The devices are passed at creation time, so the order is create, start,
	// wait for ready.
	want := []string{"create", "start", "waitready"}
	var got []string
	for _, c := range client.Calls {
		for _, w := range want {
			if strings.HasPrefix(c, w) {
				got = append(got, w)
			}
		}
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("wrong order of operations (-want +got):\n%s\ncalls=%v", diff, client.Calls)
	}
}

func TestUpMountsWorkspace(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	dev := client.Instances["dev-example-project"].Devices["workspace"]
	if dev["type"] != "disk" || dev["path"] != "/workspace" {
		t.Errorf("workspace device = %v", dev)
	}
	if dev["source"] == "" {
		t.Errorf("the workspace device source is empty")
	}
}

// An existing instance is never destroyed (spec 04-cli.md 4.1).
func TestUpDoesNotRecreateExistingInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Stopped",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if client.Called("create") {
		t.Errorf("recreated an existing instance: %v", client.Calls)
	}
	if client.Called("delete") {
		t.Errorf("deleted an existing instance: %v", client.Calls)
	}
	if !client.Called("start") {
		t.Errorf("a stopped instance was never started: %v", client.Calls)
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

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if got := client.Instances["dev-example-project"].Config["limits.cpu"]; got != "16" {
		t.Errorf("limits.cpu = %q, want 16 (the dev.yml change must be applied)", got)
	}
}

// An instance devkit does not manage is left alone (spec 05-incus.md 5.2).
func TestUpRefusesUnmanagedInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{Name: "dev-example-project", Status: "Running"})

	err := app.Up(context.Background(), cli.UpOptions{})
	if err == nil {
		t.Fatal("Up() = nil error, want a failure on an unmanaged instance")
	}
	if !strings.Contains(err.Error(), "dev-example-project") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestUpSetsManagedMarkers(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	cfg := client.Instances["dev-example-project"].Config
	if got := cfg["user.incus-devkit.project"]; got != "example-project" {
		t.Errorf("user.incus-devkit.project = %q", got)
	}
	if cfg["user.incus-devkit.root"] == "" {
		t.Errorf("user.incus-devkit.root is empty")
	}
	if got := cfg["user.incus-devkit.schema"]; got != "1" {
		t.Errorf("user.incus-devkit.schema = %q, want 1", got)
	}
}

// A named profile that does not exist fails explicitly (spec 05-incus.md 5.3).
func TestUpFailsWhenProfileMissing(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
  profiles:
    - default
    - gpu-nvidia
`)
	client.Profiles = []string{"default"}

	err := app.Up(context.Background(), cli.UpOptions{})
	if err == nil {
		t.Fatal("Up() = nil error, want a failure on a missing profile")
	}
	if !strings.Contains(err.Error(), "gpu-nvidia") {
		t.Errorf("error = %q, want it to name the missing profile", err.Error())
	}
	if client.Called("create") {
		t.Error("created the instance before checking the profiles")
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
	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !client.Called("create dev-example-project image=images:ubuntu/24.04 type=container profiles=[] noprofiles=true") {
		t.Errorf("calls = %v, want profiles: [] to mean no profiles at all", client.Calls)
	}
}

func TestUpRunsProvisionSteps(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
provision:
  - run: echo provisioned
`)
	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	var found bool
	for _, argv := range client.Execs {
		if strings.Contains(strings.Join(argv, " "), "echo provisioned") {
			found = true
		}
	}
	if !found {
		t.Errorf("no provisioning step ran: %v", client.Execs)
	}
}

// --- provision ---

func TestProvisionRequiresExistingInstance(t *testing.T) {
	app, _, _ := newApp(t, baseYAML)

	err := app.Provision(context.Background(), provision.Selection{})
	if err == nil {
		t.Fatal("Provision() = nil error, want a failure without an instance")
	}
	if !strings.Contains(err.Error(), "idev up") {
		t.Errorf("error = %q, want it to point at idev up", err.Error())
	}
}

// provision never recreates the instance (spec 04-cli.md 4.2).
func TestProvisionDoesNotCreateInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	_ = app.Provision(context.Background(), provision.Selection{})
	if client.Called("create") {
		t.Errorf("calls = %v, want provision not to create an instance", client.Calls)
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

	if err := app.Provision(context.Background(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if client.Called("config ") {
		t.Errorf("calls = %v, want provision not to change the instance config", client.Calls)
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

	if err := app.Provision(context.Background(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if !client.Called("start") {
		t.Errorf("calls = %v, want it started when it is stopped", client.Calls)
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

	if err := app.Destroy(context.Background(), cli.DestroyOptions{}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if _, ok := client.Instances["dev-example-project"]; ok {
		t.Error("the instance was not deleted")
	}
}

func TestDestroyRefusesUnmanagedInstance(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{Name: "dev-example-project", Status: "Running"})

	if err := app.Destroy(context.Background(), cli.DestroyOptions{}); err == nil {
		t.Fatal("Destroy() = nil error, want an unmanaged instance left alone")
	}
	if _, ok := client.Instances["dev-example-project"]; !ok {
		t.Error("deleted an unmanaged instance")
	}
}

func TestDestroyOnMissingInstanceIsAnError(t *testing.T) {
	app, _, _ := newApp(t, baseYAML)

	if err := app.Destroy(context.Background(), cli.DestroyOptions{}); err == nil {
		t.Fatal("Destroy() = nil error, want a failure when there is nothing to delete")
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
			t.Errorf("status =\n%s\nwant it to contain %q", text, want)
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
		t.Fatalf("the JSON output is invalid: %v\n%s", err, out.String())
	}
	// The minimum fields spec 04-cli.md 4.12 requires.
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
		t.Fatalf("Status() error = %v, want success even without an instance", err)
	}
	if !strings.Contains(out.String(), "NOT CREATED") {
		t.Errorf("status =\n%s\nwant it to say nothing has been created", out.String())
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
		t.Errorf("calls = %v, nothing was deleted", client.Calls)
	}
	if !client.Called("create") {
		t.Errorf("calls = %v, nothing was created", client.Calls)
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
		t.Fatal("Shell() = nil error, want a failure without an instance")
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
		t.Fatal("nothing was executed")
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

// Under idmap: auto on a host where raw does not work, it falls back to shift
// and carries on.
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

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v, want it to fall back to shift and carry on", err)
	}

	dev := client.Instances["dev-example-project"].Devices["workspace"]
	if dev["shift"] != "true" {
		t.Errorf("workspace device = %v, want shift=true", dev)
	}
	if _, ok := client.Instances["dev-example-project"].Config["raw.idmap"]; ok {
		t.Error("raw.idmap was set")
	}
	// The user is told it fell back, and how to do better.
	for _, want := range []string{"shift", "root:"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("warning = %q, want it to contain %q", errOut.String(), want)
		}
	}
}

// With idmap: raw declared, it fails before creating an instance when raw does
// not work.
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

	err := app.Up(context.Background(), cli.UpOptions{})
	if err == nil {
		t.Fatal("Up() = nil error, want a failure when raw was declared")
	}
	if !strings.Contains(err.Error(), "subuid") {
		t.Errorf("error = %q", err.Error())
	}
	if client.Called("create") {
		t.Errorf("calls = %v, created the instance before the check", client.Calls)
	}
}

// Under idmap: none nothing is checked.
func TestUpSkipsIDMapCheckWhenDisabled(t *testing.T) {
	cfg := loadTestConfig(t, baseYAML+"workspace:\n  idmap: none\n")

	app := cli.NewApp(cli.AppOptions{
		Config:     cfg,
		Client:     incustest.New(),
		Runner:     &runnertest.Fake{},
		Out:        &bytes.Buffer{},
		CheckIDMap: func(int, int) error { return errors.New("must not be called") },
	})

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v, want no check under idmap: none", err)
	}
}

// loadTestConfig creates a temporary project and loads its configuration.
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

// idev shell -- cmd passes the command's exit code straight through.
func TestShellPropagatesExitCode(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})
	client.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) {
		return 42, nil
	}

	err := app.Shell(context.Background(), []string{"sh", "-c", "exit 42"})

	var exitErr *cli.ExitCodeError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %v (%T), want *cli.ExitCodeError", err, err)
	}
	if exitErr.Code != 42 {
		t.Errorf("Code = %d, want 42", exitErr.Code)
	}
}

// With nothing attached to a terminal, no pseudo-terminal is allocated; it
// would put carriage returns into the output.
func TestShellAllocatesTTYOnlyWhenInteractive(t *testing.T) {
	tests := []struct {
		name        string
		interactive bool
		wantTTY     bool
	}{
		{"attached to a terminal", true, true},
		{"through a pipe", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadTestConfig(t, baseYAML)
			client := incustest.New()
			client.AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: map[string]string{"user.incus-devkit.project": "example-project"},
			})

			var got incus.ExecOptions
			client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
				got = opt
				return 0, nil
			}

			in, out, errOut := strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}
			app := cli.NewApp(cli.AppOptions{
				Config:      cfg,
				Client:      client,
				Runner:      &runnertest.Fake{},
				In:          in,
				Out:         out,
				ErrOut:      errOut,
				Interactive: tt.interactive,
				CheckIDMap:  func(int, int) error { return nil },
			})

			if err := app.Shell(context.Background(), []string{"pwd"}); err != nil {
				t.Fatalf("Shell() error = %v", err)
			}
			if got.TTY != tt.wantTTY {
				t.Errorf("TTY = %v, want %v", got.TTY, tt.wantTTY)
			}
			// Even with a terminal allocated, the streams have to reach Incus.
			if got.Stdin != in || got.Stdout != out || got.Stderr != errOut {
				t.Errorf("the streams were not passed: %+v", got)
			}
		})
	}
}

// Output is streamed in the non-interactive case too.
func TestShellStreamsOutputWhenNotInteractive(t *testing.T) {
	cfg := loadTestConfig(t, baseYAML)
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	var gotStdout bool
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		gotStdout = opt.Stdout != nil
		return 0, nil
	}

	app := cli.NewApp(cli.AppOptions{
		Config:     cfg,
		Client:     client,
		Runner:     &runnertest.Fake{},
		Out:        &bytes.Buffer{},
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Shell(context.Background(), []string{"pwd"}); err != nil {
		t.Fatalf("Shell() error = %v", err)
	}
	if !gotStdout {
		t.Error("want standard output streamed in the non-interactive case")
	}
}

// The keys of status --json are a machine-readable contract, so every field is
// pinned (spec 04-cli.md 4.12, docs/manual/07-ai-agents.md).
func TestStatusJSONContract(t *testing.T) {
	app, client, out := newApp(t, baseYAML+`
  config:
    limits.cpu: "8"
provision:
  - run: "true"
  - run: "true"
`)
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			"user.incus-devkit.project": "example-project",
			"limits.cpu":                "8",
		},
		Devices: map[string]incus.Device{"workspace": {"type": "disk"}},
	})

	if err := app.Status(context.Background(), true); err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("the JSON output is invalid: %v\n%s", err, out.String())
	}

	want := map[string]any{
		"project":          "example-project",
		"instance":         "dev-example-project",
		"status":           "Running",
		"image":            "images:ubuntu/24.04",
		"workspace":        "/workspace",
		"workspace_source": got["workspace_source"], // a temporary directory, so the value does not matter
		"exists":           true,
		"managed":          true,
		"profiles":         []any{"default"},
		"config":           map[string]any{"limits.cpu": "8"},
		"devices":          []any{"workspace(disk)"},
		"provision_steps":  float64(2),
		"incus_remote":     "local",
		"incus_project":    "default",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("the keys or values of status --json changed (-want +got):\n%s", diff)
	}
	if got["workspace_source"] == "" {
		t.Error("workspace_source is empty")
	}
}

func TestStatusReportsProvisionStepCount(t *testing.T) {
	app, _, out := newApp(t, baseYAML+"provision:\n  - run: a\n  - run: b\n  - run: c\n")

	if err := app.Status(context.Background(), false); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !strings.Contains(out.String(), "3 step(s)") {
		t.Errorf("status = %q, want the number of steps shown", out.String())
	}
}

func TestValidateReportsProvisionStepCount(t *testing.T) {
	app, _, out := newApp(t, baseYAML+"provision:\n  - run: a\n  - run: b\n")

	if err := app.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !strings.Contains(out.String(), "2 step(s)") {
		t.Errorf("validate = %q, want the number of steps shown", out.String())
	}
}

// shell starts the default shell in the workspace (spec 04-cli.md 4.3).
func TestShellUsesDefaultShellAndWorkspace(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	var gotCwd string
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		gotCwd = opt.Cwd
		return 0, nil
	}

	if err := app.Shell(context.Background(), nil); err != nil {
		t.Fatalf("Shell() error = %v", err)
	}

	if diff := cmp.Diff([]string{config.DefaultShell}, client.Execs[0]); diff != "" {
		t.Errorf("argv mismatch (-want +got):\n%s", diff)
	}
	if gotCwd != "/workspace" {
		t.Errorf("Cwd = %q, want /workspace", gotCwd)
	}
}

// instance.type reaches what is passed at creation time.
func TestUpPassesInstanceType(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+"  type: virtual-machine\n")

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !client.Called("create dev-example-project image=images:ubuntu/24.04 type=virtual-machine") {
		t.Errorf("calls = %v, want instance.type passed", client.Calls)
	}
}

// The context devkit hands to a step is assembled correctly (spec 3.10).
func TestProvisionEnvIsPopulated(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+"provision:\n  - run: echo hi\n")

	var gotEnv map[string]string
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		gotEnv = opt.PublicEnv
		return 0, nil
	}

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	if got := gotEnv["DEVKIT_PROJECT_NAME"]; got != "example-project" {
		t.Errorf("DEVKIT_PROJECT_NAME = %q", got)
	}
	if got := gotEnv["DEVKIT_INSTANCE"]; got != "dev-example-project" {
		t.Errorf("DEVKIT_INSTANCE = %q", got)
	}
	if got := gotEnv["DEVKIT_WORKSPACE"]; got != "/workspace" {
		t.Errorf("DEVKIT_WORKSPACE = %q", got)
	}
	// workspace (inside the container) and workspace_source (on the host) are
	// not mixed up.
	src := gotEnv["DEVKIT_WORKSPACE_SOURCE"]
	if src == "/workspace" || !filepath.IsAbs(src) {
		t.Errorf("DEVKIT_WORKSPACE_SOURCE = %q, want a path on the host", src)
	}
	if got := gotEnv["DEVKIT_INCUS_REMOTE"]; got != "local" {
		t.Errorf("DEVKIT_INCUS_REMOTE = %q, want local", got)
	}
	if got := gotEnv["DEVKIT_INCUS_PROJECT"]; got != "default" {
		t.Errorf("DEVKIT_INCUS_PROJECT = %q, want default", got)
	}
}

// With a pseudo-terminal allocated, the host's TERM reaches the container.
// Without it, vim and less cannot tell what terminal they are on.
func TestShellPassesTerm(t *testing.T) {
	cfg := loadTestConfig(t, baseYAML)
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	var got incus.ExecOptions
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		got = opt
		return 0, nil
	}

	app := cli.NewApp(cli.AppOptions{
		Config:      cfg,
		Client:      client,
		Runner:      &runnertest.Fake{},
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Interactive: true,
		Term:        "xterm-256color",
		CheckIDMap:  func(int, int) error { return nil },
	})

	if err := app.Shell(context.Background(), nil); err != nil {
		t.Fatalf("Shell() error = %v", err)
	}
	if got.Term != "xterm-256color" {
		t.Errorf("Term = %q, want xterm-256color", got.Term)
	}
}
