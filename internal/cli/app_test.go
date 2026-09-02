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
	"github.com/lambdasakura/incus-dev/internal/provision"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
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
	app := cli.MustNewApp(cli.AppOptions{
		Config:  cfg,
		Client:  client,
		Runner:  &runnertest.Fake{},
		Out:     out,
		Verbose: false,
		// Do not depend on the host's /etc/subuid.
		CheckIDMap:   func(int, int) error { return nil },
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
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
			"user.incus-dev.project": "example-project",
			"limits.cpu":             "4",
		},
	})

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if got := client.Instances["dev-example-project"].Config["limits.cpu"]; got != "16" {
		t.Errorf("limits.cpu = %q, want 16 (the dev.yml change must be applied)", got)
	}
}

// An instance idev does not manage is left alone (spec 05-incus.md 5.2).
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
	if got := cfg["user.incus-dev.project"]; got != "example-project" {
		t.Errorf("user.incus-dev.project = %q", got)
	}
	if cfg["user.incus-dev.root"] == "" {
		t.Errorf("user.incus-dev.root is empty")
	}
	if got := cfg["user.incus-dev.schema"]; got != "1" {
		t.Errorf("user.incus-dev.schema = %q, want 1", got)
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
	if !client.Called("create dev-example-project image=images:ubuntu/24.04 profiles=[] noprofiles=true") {
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
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

	err := app.Destroy(context.Background(), cli.DestroyOptions{})
	if err == nil {
		t.Fatal("Destroy() = nil error, want a failure when there is nothing to delete")
	}
	// The advice belongs to the command, not to the lookup they share: the
	// only next step offered to someone removing an environment must not be
	// to create one.
	if strings.Contains(err.Error(), "idev up") {
		t.Errorf("Destroy() = %q, want it not to advise creating the instance", err)
	}
}

// The commands that do need the instance still say how to get one.
func TestMissingInstanceAdvisesUpWhereThatHelps(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  func(cli.App) error
	}{
		{"shell", func(a cli.App) error { return a.Shell(context.Background(), nil) }},
		{"provision", func(a cli.App) error {
			return a.Provision(context.Background(), provision.Selection{})
		}},
		{"snapshot create", func(a cli.App) error {
			return a.CreateSnapshot(context.Background(), "s")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app, _, _ := newApp(t, baseYAML)

			err := tt.run(*app)
			if err == nil {
				t.Fatal("= nil error, want a failure when the instance is not there")
			}
			if !strings.Contains(err.Error(), "idev up") {
				t.Errorf("= %q, want it to say how to create the instance", err)
			}
		})
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
			"user.incus-dev.project": "example-project",
			"image.description":      "ubuntu 24.04",
			"limits.cpu":             "8",
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
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

	app := cli.MustNewApp(cli.AppOptions{
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

	app := cli.MustNewApp(cli.AppOptions{
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

	app := cli.MustNewApp(cli.AppOptions{
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
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
				Config: map[string]string{"user.incus-dev.project": "example-project"},
			})

			var got incus.ExecOptions
			client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
				got = opt
				return 0, nil
			}

			in, out, errOut := strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}
			app := cli.MustNewApp(cli.AppOptions{
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
	})

	var gotStdout bool
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		gotStdout = opt.Stdout != nil
		return 0, nil
	}

	app := cli.MustNewApp(cli.AppOptions{
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
			"user.incus-dev.project": "example-project",
			// What up records, so the shape is the one a real instance has.
			"user.incus-dev.image": "images:ubuntu/24.04",
			"limits.cpu":           "8",
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
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

// The context idev hands to a step is assembled correctly (spec 3.10).
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

	if got := gotEnv["IDEV_PROJECT_NAME"]; got != "example-project" {
		t.Errorf("IDEV_PROJECT_NAME = %q", got)
	}
	if got := gotEnv["IDEV_INSTANCE"]; got != "dev-example-project" {
		t.Errorf("IDEV_INSTANCE = %q", got)
	}
	if got := gotEnv["IDEV_WORKSPACE"]; got != "/workspace" {
		t.Errorf("IDEV_WORKSPACE = %q", got)
	}
	// workspace (inside the container) and workspace_source (on the host) are
	// not mixed up.
	src := gotEnv["IDEV_WORKSPACE_SOURCE"]
	if src == "/workspace" || !filepath.IsAbs(src) {
		t.Errorf("IDEV_WORKSPACE_SOURCE = %q, want a path on the host", src)
	}
	if got := gotEnv["IDEV_INCUS_PROJECT"]; got != "default" {
		t.Errorf("IDEV_INCUS_PROJECT = %q, want default", got)
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
		Config: map[string]string{"user.incus-dev.project": "example-project"},
	})

	var got incus.ExecOptions
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		got = opt
		return 0, nil
	}

	app := cli.MustNewApp(cli.AppOptions{
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

// A second up writes nothing when nothing has changed.
//
// The write is guarded by the ETag of the reading it was decided from, so
// every unnecessary one is an opportunity to lose a race with another idev and
// fail a run that had nothing to do. It also lands in the daemon's log as a
// change, which makes the log useless for finding when something did change.
func TestUpWritesNothingWhenNothingChanged(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
  config:
    limits.cpu: "16"
`)

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("first Up() error = %v", err)
	}

	mark := len(client.Calls)
	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("second Up() error = %v", err)
	}

	for _, call := range client.Calls[mark:] {
		for _, write := range []string{"config ", "devices ", "unset ", "removedevices "} {
			if strings.HasPrefix(call, write) {
				t.Errorf("the second up wrote %q, but nothing had changed since the "+
					"first: %v", call, client.Calls[mark:])
			}
		}
	}
}

// The skip is decided against what the daemon holds, not against the working
// copy.
//
// adoptCarried and pruneVolumeRecord edit inst.Config in place before the
// change is built, so the instance value no longer says what is stored.
// Comparing the change against it makes every edit those two make look like
// no change at all: the pruned record is never written, and the volume record
// a rebuild is carrying is dropped on the floor while up reports success.
func TestUpWritesAPrunedVolumeRecordEvenWhenNothingElseChanged(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("first Up() error = %v", err)
	}

	// A volume the record names and the pool does not have: pruning it is the
	// only difference between the instance and dev.yml.
	inst := client.Instances["dev-example-project"]
	inst.Config["user.incus-dev.volumes"] = "default/dev-example-project-gone"

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("second Up() error = %v", err)
	}

	if got := client.Instances["dev-example-project"].Config["user.incus-dev.volumes"]; got != "" {
		t.Errorf("record = %q, want the missing volume pruned from it", got)
	}
}

// A config key changed on the instance is written back even though every
// device already matches.
func TestUpWritesConfigWhenOnlyConfigDrifted(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
  config:
    limits.cpu: "16"
`)

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("first Up() error = %v", err)
	}
	client.Instances["dev-example-project"].Config["limits.cpu"] = "4"

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("second Up() error = %v", err)
	}
	if got := client.Instances["dev-example-project"].Config["limits.cpu"]; got != "16" {
		t.Errorf("limits.cpu = %q, want 16: dev.yml declares it and the instance had 4", got)
	}
}

// A device changed on the instance is written back even though every config
// key already matches.
func TestUpWritesDevicesWhenOnlyADeviceDrifted(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("first Up() error = %v", err)
	}
	want := client.Instances["dev-example-project"].Devices["workspace"]["path"]
	if want == "" {
		t.Fatal("the workspace device has no path; this test no longer checks anything")
	}
	client.Instances["dev-example-project"].Devices["workspace"]["path"] = "/elsewhere"

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("second Up() error = %v", err)
	}
	if got := client.Instances["dev-example-project"].Devices["workspace"]["path"]; got != want {
		t.Errorf("workspace path = %q, want %q: dev.yml declares it and the instance had /elsewhere",
			got, want)
	}
}

// A declared volume is asked about once per up, not twice.
//
// ensureVolumes creates it or finds it, so by the time the record is pruned
// the answer for a declared volume is already known to be yes. Asking again is
// a round trip per volume, and the second answer cannot differ from the first
// without something else having deleted the volume mid-run.
func TestUpChecksADeclaredVolumeOnce(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
volumes:
  cache:
    path: /cache
`)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			"user.incus-dev.project": "example-project",
			"user.incus-dev.volumes": "default/dev-example-project-cache",
		},
	})
	client.Volumes["default/dev-example-project-cache"] = true

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	const call = "volume exists default dev-example-project-cache"
	var n int
	for _, c := range client.Calls {
		if c == call {
			n++
		}
	}
	if n != 1 {
		t.Errorf("asked whether the declared volume exists %d times, want 1: %v", n, client.Calls)
	}
}

// The commands that work inside a running instance read it once.
//
// Each one looked it up to check idev manages it, then looked it up again to
// see whether it was running. The second reading is a round trip for something
// the first already said, and the two can disagree, which makes the behaviour
// depend on which one a later change happens to consult.
func TestCommandsReadTheInstanceOnce(t *testing.T) {
	running := func() *incus.Instance {
		return &incus.Instance{
			Name:   "dev-example-project",
			Status: "Running",
			Config: map[string]string{"user.incus-dev.project": "example-project"},
		}
	}

	for _, tt := range []struct {
		name string
		run  func(*cli.App) error
	}{
		{"provision", func(a *cli.App) error {
			return a.Provision(context.Background(), provision.Selection{})
		}},
		{"exec", func(a *cli.App) error {
			return a.Exec(context.Background(), []string{"true"})
		}},
		{"shell", func(a *cli.App) error {
			return a.Shell(context.Background(), nil)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app, client, _ := newApp(t, baseYAML)
			client.AddInstance(running())

			if err := tt.run(app); err != nil {
				t.Fatalf("%s error = %v", tt.name, err)
			}

			var n int
			for _, c := range client.Calls {
				if c == "instance dev-example-project" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("read the instance %d times, want 1: %v", n, client.Calls)
			}
		})
	}
}

// The profiles are checked with one listing, not one per profile.
//
// Incus has no "does this profile exist" call: every answer is the whole list
// of names, filtered. Asking once per declared profile fetches the same list
// again for each one, and the answers can disagree with each other.
func TestProfilesAreListedOnce(t *testing.T) {
	app, client, _ := newApp(t, baseYAML+`
  profiles:
    - default
    - gpu
    - web
`)
	client.Profiles = []string{"default", "gpu", "web"}

	if err := app.Up(context.Background(), cli.UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	var n int
	for _, c := range client.Calls {
		if strings.HasPrefix(c, "profile") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("listed the profiles %d times for 3 declared profiles, want 1: %v",
			n, client.Calls)
	}
}

// profiles: [] declares that there are none to check, so there is nothing to
// ask the host about.
func TestProfilesAreNotListedWhenNoneAreDeclared(t *testing.T) {
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
	for _, c := range client.Calls {
		if strings.HasPrefix(c, "profile") {
			t.Errorf("listed the profiles with none declared: %v", client.Calls)
		}
	}
}

// rebuild reads the instance three times, and each one reads something the
// last cannot know.
//
// It read it a fourth time: once to carry the volume record off before the
// instance goes, and again inside destroy to check idev manages it, with
// nothing in between. What is left is the record before the delete, the lookup
// that finds the instance gone, and the status of the one just created.
func TestRebuildReadsTheInstanceOnceBeforeDestroying(t *testing.T) {
	app, client, _ := newApp(t, baseYAML)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-dev.project": "example-project"},
	})

	if err := app.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	var n int
	for _, c := range client.Calls {
		if c == "instance dev-example-project" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("read the instance %d times, want 3 (the record before the delete, "+
			"the lookup after it, and the status of the new one): %v", n, client.Calls)
	}
}
