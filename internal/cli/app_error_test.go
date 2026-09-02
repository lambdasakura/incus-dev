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
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/incus/incustest"
	"github.com/lambdasakura/incus-dev/internal/provision"
	"github.com/lambdasakura/incus-dev/internal/runner"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
)

var errBoom = errors.New("boom")

// errWriter is an io.Writer whose writes always fail.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errBoom }

// appWith builds an App from the given configuration and fake client.
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

// managed registers an instance idev manages for this project.
func managed(client *incustest.Fake, status string) *incustest.Fake {
	return client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: status,
		Config: map[string]string{managedProjectKey: "example-project"},
	})
}

// When an Incus operation fails, the reason reaches the caller.
func TestUpPropagatesIncusErrors(t *testing.T) {
	tests := []struct {
		name    string
		failOn  string
		managed bool
	}{
		{"the profile check fails", "profile", false},
		{"fetching the instance fails", "instance", false},
		{"creating the instance fails", "create", false},
		{"applying the devices fails", "devices", true},
		{"starting fails", "start", false},
		{"waiting for ready fails", "waitready", false},
		{"re-applying the config fails", "config", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := incustest.New()
			client.FailOn = map[string]error{tt.failOn: errBoom}
			if tt.managed {
				managed(client, "Running")
			}

			err := appWith(t, rootYAML, client).Up(context.Background(), UpOptions{})
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
		{"fetching the instance fails", "instance"},
		{"waiting for ready fails", "waitready"},
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
		t.Errorf("error = %q, want it clear the failure was in bootstrap", err.Error())
	}
	for _, argv := range client.Execs {
		if strings.Contains(strings.Join(argv, " "), "never") {
			t.Error("ran provisioning after bootstrap failed")
		}
	}
}

func TestDestroyPropagatesErrors(t *testing.T) {
	for _, failOn := range []string{"instance", "delete"} {
		t.Run(failOn, func(t *testing.T) {
			client := incustest.New()
			managed(client, "Running")
			client.FailOn = map[string]error{failOn: errBoom}

			if err := appWith(t, rootYAML, client).Destroy(context.Background(), DestroyOptions{}); !errors.Is(err, errBoom) {
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
	t.Run("the instance is unmanaged", func(t *testing.T) {
		client := incustest.New().AddInstance(&incus.Instance{Name: "dev-example-project", Status: "Running"})

		if err := appWith(t, rootYAML, client).Shell(context.Background(), nil); err == nil {
			t.Error("error = nil, want error")
		}
	})

	t.Run("starting fails", func(t *testing.T) {
		client := incustest.New()
		managed(client, "Stopped")
		client.FailOn = map[string]error{"start": errBoom}

		if err := appWith(t, rootYAML, client).Shell(context.Background(), nil); !errors.Is(err, errBoom) {
			t.Errorf("error = %v, want %v", err, errBoom)
		}
	})

	t.Run("running it fails", func(t *testing.T) {
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

// status shows an existing instance's profiles and limits too.
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
			t.Errorf("status =\n%s\nwant it to contain %q", text, want)
		}
	}
	if strings.Contains(text, "image.os") {
		t.Errorf("status = %q, want config other than limits left out", text)
	}
}

// With raw.idmap set in instance.config, idev stays out of the mapping.
func TestIDMapModeRespectsExplicitRawIDMap(t *testing.T) {
	client := incustest.New()
	body := rootYAML + "  config:\n    raw.idmap: \"both 1234 0\"\n"

	app := appWith(t, body, client)
	app.checkIDMap = func(int, int) error { return errBoom }

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v, want no check when it was set explicitly", err)
	}
	if got := client.Instances["dev-example-project"].Config["raw.idmap"]; got != "both 1234 0" {
		t.Errorf("raw.idmap = %q", got)
	}
}

func TestExitCodeErrorMessage(t *testing.T) {
	err := &ExitCodeError{Code: 42}

	if got := err.Error(); !strings.Contains(got, "42") {
		t.Errorf("Error() = %q, want it to contain the exit code", got)
	}
}

func TestNewAppDefaultsWriters(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Omitting Out and ErrOut does not break anything.
	app := NewApp(AppOptions{Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{}})
	if err := app.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}
}

// Switching from shift to raw leaves nothing stale behind.
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
	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	inst := client.Instances["dev-example-project"]
	if _, ok := inst.Config[idmapConfigKey]; ok {
		t.Errorf("raw.idmap survived: %q", inst.Config[idmapConfigKey])
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
	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	inst := client.Instances["dev-example-project"]
	if got := inst.Devices["workspace"]["shift"]; got != "false" {
		t.Errorf("shift = %q, want false (it must be disabled on a switch)", got)
	}
	if !strings.HasPrefix(inst.Config[idmapConfigKey], "uid ") {
		t.Errorf("raw.idmap = %q", inst.Config[idmapConfigKey])
	}
}

// A change needing a restart on a running instance produces a warning
// (spec 05-incus.md 5.4.5).
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

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	out := errOut.String()
	if !strings.Contains(out, "security.nesting") || !strings.Contains(out, "restart") {
		t.Errorf("warning = %q, want it to say a restart is needed", out)
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

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart") {
		t.Errorf("want no restart warning while it is stopped: %q", errOut.String())
	}
}

// A failed wait for ready does not go on to provisioning
// (spec 06-provisioning.md 6.2).
func TestUpFailsWhenInstanceNotReady(t *testing.T) {
	client := incustest.New()
	client.FailReady = true

	err := appWith(t, rootYAML+"provision:\n  - run: never\n", client).Up(context.Background(), UpOptions{})
	if err == nil {
		t.Fatal("Up() = nil error, want the failed wait reported")
	}
	if !strings.Contains(err.Error(), "ready") {
		t.Errorf("error = %q", err.Error())
	}
	if len(client.Execs) != 0 {
		t.Errorf("execs = %v, want no step run before it is ready", client.Execs)
	}
}

// A failed ansible step is identified by its position and its name too.
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

	// The playbook run itself, not the prerequisite check, which runs
	// ansible-playbook --version.
	cmdRunner := &runnertest.Fake{Err: map[string]error{"ansible-playbook -i": errBoom}}
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
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
}

// What an ansible step needs on the host is checked before the instance is
// touched (spec 06-provisioning.md 6.5.1).
//
// Otherwise up creates the instance, starts it, waits for the network and runs
// the default bootstrap — a minute of apt — only to stop because Ansible is not
// installed on the host.
func TestUpChecksAnsiblePrerequisitesBeforeCreating(t *testing.T) {
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
  - name: playbook step
    ansible:
      playbook: .incus-dev/ansible/site.yml
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = root

	client := incustest.New()
	app := NewApp(AppOptions{
		Config: cfg, Client: client,
		Runner: &runnertest.Fake{Err: map[string]error{"ansible-playbook": errBoom}},
		Out:    &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err == nil {
		t.Fatal("Up() = nil error, want it to stop on the missing prerequisite")
	}
	for _, call := range client.Calls {
		if strings.HasPrefix(call, "create ") {
			t.Errorf("calls = %v, want no instance created", client.Calls)
			break
		}
	}
}

// The image is the most likely thing in dev.yml to be mistyped, and it is
// resolved against a remote, so nothing offline can catch it. A preflight
// that passes on an image up cannot fetch is the false green light spec
// 04-cli.md 4.7 leans on this command not to give.
func TestPlanChecksTheImageResolves(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	client := incustest.New()
	client.Images = []string{"images:debian/12"} // not the one rootYAML names

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Plan(context.Background()); err == nil {
		t.Error("Plan() = nil error, want the image failure up reports")
	}
}

// An instance that is already there is not created again, so up never
// resolves the image and neither may the preflight.
func TestPlanLeavesTheImageAloneForAnExistingInstance(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	client := incustest.New()
	client.Images = []string{"images:debian/12"}
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Plan(context.Background()); err != nil {
		t.Errorf("Plan() error = %v, want none: up would not fetch an image", err)
	}
}

// A storage pool that is not there stops up on its first volume. The preview
// used to report the volume as one it would create and exit 0.
func TestPlanChecksTheStoragePool(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML+`
volumes:
  cache:
    path: /var/cache/x
    pool: nosuchpool
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}

	app := NewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err = app.Plan(context.Background())
	if err == nil {
		t.Fatal("Plan() = nil error, want the pool failure up reports")
	}
	if !errors.Is(err, incus.ErrPoolNotFound) {
		t.Errorf("Plan() error = %v, want ErrPoolNotFound", err)
	}
}

// up --dry-run makes the same host-side checks up does.
//
// Spec 04-cli.md 4.7 leans on this: it is why validate has no --check-host
// flag. A preflight that passes while up fails on the next line is worse than
// none.
func TestPlanChecksAnsiblePrerequisites(t *testing.T) {
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
  - name: playbook step
    ansible:
      playbook: .incus-dev/ansible/site.yml
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = root

	app := NewApp(AppOptions{
		Config: cfg, Client: incustest.New(),
		Runner: &runnertest.Fake{Err: map[string]error{"ansible-playbook": errBoom}},
		Out:    &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Plan(context.Background()); err == nil {
		t.Error("Plan() = nil error, want the same prerequisite failure up reports")
	}
}

// Changing instance.image on an existing instance is said out loud.
//
// up never re-images an instance, so the declaration and the reality drift
// apart silently: the user edits image:, runs up, and gets the old
// environment with nothing said.
func TestUpWarnsWhenTheDeclaredImageChanged(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedImageKey:   "images:debian/12",
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	for _, want := range []string{"images:debian/12", "images:ubuntu/24.04", "rebuild"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("warning = %q, want it to mention %q", errOut.String(), want)
		}
	}

	// The record is what the instance was made from, so up must not rewrite
	// it: doing so would silence the warning after one run and leave status
	// reporting an image the instance never had.
	if got := client.Instances["dev-example-project"].Config[managedImageKey]; got != "images:debian/12" {
		t.Errorf("recorded image = %q, want it left as the instance was made", got)
	}
	errOut.Reset()
	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(errOut.String(), "images:debian/12") {
		t.Errorf("second run = %q, want it to warn again", errOut.String())
	}
}

// profiles: [] and an instance with none is not a change.
func TestUpDoesNotWarnWhenProfilesMatch(t *testing.T) {
	cfg := mustParse(t, rootYAML+`  profiles: []
  devices:
    root:
      type: disk
      pool: default
      path: /
`)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "instance.profiles") {
		t.Errorf("warning = %q, want none when the lists match", errOut.String())
	}
}

// Changing instance.profiles on an existing instance is said out loud too.
func TestUpWarnsWhenTheDeclaredProfilesChanged(t *testing.T) {
	cfg := mustParse(t, rootYAML+"  profiles:\n    - default\n    - gpu\n")

	client := incustest.New()
	client.Profiles = []string{"default", "gpu"}
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	for _, want := range []string{"gpu", "rebuild"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("warning = %q, want it to mention %q", errOut.String(), want)
		}
	}
}

// A volume dropped from dev.yml stays reachable and is said out loud.
//
// Nothing names it any more, so without the record its data would sit on the
// pool with no idev command able to remove it.
func TestVolumeDroppedFromTheDeclaration(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache",
		},
	})
	client.Volumes["default/dev-example-project-cache"] = true

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	// Named, and pointed at a command that removes only that volume: the
	// obvious 'idev destroy --volumes' takes the instance and every other
	// recorded volume with it.
	for _, want := range []string{"dev-example-project-cache", "incus storage volume delete"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("warning = %q, want it to mention %q", errOut.String(), want)
		}
	}

	// A record that is not pool/name is skipped rather than acted on.
	client.Instances["dev-example-project"].Config[managedVolumesKey] =
		"default/dev-example-project-cache,malformed"

	// And destroy --volumes can still reach it.
	if err := app.Destroy(context.Background(), DestroyOptions{Volumes: true}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if client.Volumes["default/dev-example-project-cache"] {
		t.Error("the volume that left the declaration was not deleted")
	}
}

// rebuild checks the host before it destroys anything.
//
// Otherwise a condition up refuses to start on — an unresolvable secret, a
// missing profile — is discovered after the environment is already gone, and
// the user cannot get it back until the host is fixed
// (spec 03-configuration.md 3.12).
func TestRebuildChecksBeforeDestroying(t *testing.T) {
	cfg := mustParse(t, rootYAML+"secrets:\n  API_TOKEN:\n    env: NOT_SET_ANYWHERE\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{},
		CheckIDMap: func(int, int) error { return nil },
		LookupEnv:  func(string) (string, bool) { return "", false },
	})

	if err := app.Rebuild(context.Background()); err == nil {
		t.Fatal("Rebuild() = nil error, want the unresolvable secret reported")
	}
	if _, ok := client.Instances["dev-example-project"]; !ok {
		t.Error("the instance was destroyed before the host was checked")
	}
}

// shell, exec and provision say so too when the workspace is another
// checkout's.
//
// They operate on whatever up mounted last, so running them from the second
// checkout reaches into the first one's tree — `idev exec -- rm -rf build`
// deletes the other clone's build directory, on the host.
func TestCommandsWarnAboutAnotherCheckout(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	newFake := func() *incustest.Fake {
		client := incustest.New()
		client.AddInstance(&incus.Instance{
			Name:     "dev-example-project",
			Status:   "Running",
			Profiles: []string{"default"},
			Config: map[string]string{
				managedProjectKey: "example-project",
				managedRootKey:    "/home/u/other-checkout",
			},
		})
		return client
	}

	for _, tt := range []struct {
		name string
		run  func(*App) error
	}{
		{"exec", func(a *App) error { return a.Exec(context.Background(), []string{"true"}) }},
		{"shell", func(a *App) error { return a.Shell(context.Background(), nil) }},
		{"provision", func(a *App) error {
			return a.Provision(context.Background(), provision.Selection{})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			errOut := &bytes.Buffer{}
			app := NewApp(AppOptions{
				Config: cfg, Client: newFake(), Runner: &runnertest.Fake{},
				Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
			})

			if err := tt.run(app); err != nil {
				t.Fatalf("%s error = %v", tt.name, err)
			}
			if !strings.Contains(errOut.String(), "other-checkout") {
				t.Errorf("output = %q, want it to name the checkout the workspace points at", errOut.String())
			}
		})
	}
}

// rebuild resolves the image before it destroys anything.
func TestRebuildChecksTheImageBeforeDestroying(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})
	client.Hook = func(call string) error {
		if strings.HasPrefix(call, "image check") {
			return errBoom
		}
		return nil
	}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Rebuild(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want the image failure", err)
	}
	if _, ok := client.Instances["dev-example-project"]; !ok {
		t.Error("the instance was destroyed although the image does not resolve")
	}
}

// A stopped instance owes no restart, so the preview does not claim one.
func TestPlanSaysNothingAboutARestartOnAStoppedInstance(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Stopped",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedRestartKey: recordRestart(time.Time{}, map[string]bootedValue{"security.nesting": {value: "false", known: true}}),
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Plan(context.Background()); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if strings.Contains(errOut.String(), "waiting on a restart") {
		t.Errorf("plan = %q, want nothing owed by a stopped instance", errOut.String())
	}
}

// A restart record idev did not write is ignored rather than half-read.
func TestPendingRestartIgnoresAMalformedRecord(t *testing.T) {
	for _, record := range []string{"security.nesting", "not-a-time|security.nesting"} {
		if got := pendingRestart(map[string]string{managedRestartKey: record}, time.Time{}); got != nil {
			t.Errorf("pendingRestart(%q) = %v, want none", record, got)
		}
	}
}

// A rebuild records the image even when it carries a volume record.
//
// The carry was implemented by handing the create path a non-nil "current"
// config, and the image marker was written only when that was nil — so a
// project with volumes lost the marker on every rebuild, permanently, and
// nothing warned about an image change afterwards.
func TestRebuildStillRecordsTheImage(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedImageKey:   "images:ubuntu/24.04",
			managedVolumesKey: "default/dev-example-project-cache",
		},
	})
	client.Volumes["default/dev-example-project-cache"] = true

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	got := client.Instances["dev-example-project"].Config
	if got[managedImageKey] != "images:ubuntu/24.04" {
		t.Errorf("image record = %q, want it written at creation", got[managedImageKey])
	}
	if !strings.Contains(got[managedVolumesKey], "cache") {
		t.Errorf("volume record = %q, want it carried", got[managedVolumesKey])
	}
}

// up does not allocate storage for an instance it turns out not to manage.
func TestUpDoesNotCreateVolumesForAnUnmanagedInstance(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		// Somebody else's instance: no marker.
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err == nil {
		t.Fatal("Up() = nil error, want it to refuse an instance it does not manage")
	}
	if len(client.Volumes) != 0 {
		t.Errorf("volumes = %v, want none allocated before the refusal", client.Volumes)
	}
}

// A rebuild checks the host once, not once before and once after.
func TestRebuildChecksTheHostOnce(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})

	checks := 0
	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut,
		CheckIDMap: func(int, int) error { checks++; return nil },
	})

	if err := app.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if checks != 1 {
		t.Errorf("host checks = %d, want 1", checks)
	}
	if n := strings.Count(errOut.String(), "Project: example-project"); n != 1 {
		t.Errorf("the run was announced %d times, want 1", n)
	}
}

// A rebuild whose provisioning fails still records the carried volumes.
//
// The record is part of the creation request, not something written after the
// run succeeds: a failing step would otherwise strand the volume, and a step
// failing during a rebuild is the ordinary case.
func TestRebuildKeepsTheRecordWhenProvisioningFails(t *testing.T) {
	cfg := mustParse(t, rootYAML+"provision:\n  - run: \"false\"\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-old",
		},
	})
	client.Volumes["default/dev-example-project-old"] = true
	client.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) { return 1, nil }

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Rebuild(context.Background()); err == nil {
		t.Fatal("Rebuild() = nil error, want the failing step reported")
	}

	got := client.Instances["dev-example-project"].Config[managedVolumesKey]
	if !strings.Contains(got, "dev-example-project-old") {
		t.Errorf("record = %q, want the carried volume recorded despite the failure", got)
	}
}

// up --dry-run says everything up would say.
//
// The preview is the preflight (spec 04-cli.md 4.8), so the warnings about
// what up cannot apply have to appear in it — they are the whole point of the
// markers.
func TestPlanReportsTheSameWarningsAsUp(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedImageKey:   "images:debian/12",
			managedRootKey:    "/home/u/other-checkout",
		},
	})

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Plan(context.Background()); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	for _, want := range []string{"images:debian/12", "other-checkout"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("plan warnings = %q, want them to mention %q", errOut.String(), want)
		}
	}
	if !strings.Contains(out.String(), "Create volume dev-example-project-cache") {
		t.Errorf("plan =\n%s\nwant the volume it would allocate", out.String())
	}
	// A preview changes nothing.
	if client.Volumes["default/dev-example-project-cache"] {
		t.Error("the dry run created the volume")
	}

	// A volume that is already there is not offered again.
	client.Volumes["default/dev-example-project-cache"] = true
	out.Reset()
	if err := app.Plan(context.Background()); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if strings.Contains(out.String(), "Create volume") {
		t.Errorf("plan =\n%s\nwant no creation for a volume that exists", out.String())
	}

	// A pool that cannot be read is reported rather than guessed at.
	client.Hook = func(call string) error {
		if strings.HasPrefix(call, "volume exists") {
			return errBoom
		}
		return nil
	}
	if err := app.Plan(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// A volume added to dev.yml is created for an instance that already exists.
func TestUpCreatesVolumesForAnExistingInstance(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !client.Volumes["default/dev-example-project-cache"] {
		t.Errorf("volumes = %v, want the newly declared one created", client.Volumes)
	}
}

// A volume the user deleted by hand leaves the record, so up stops mentioning
// it.
//
// The record only ever grew, so a warning about a volume outlived the volume:
// the offered remedy, destroy --volumes, also destroys the instance, so
// deleting one by hand is what a user would do.
func TestVolumeRecordDropsWhatIsGone(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache,malformed",
		},
	})
	// The volume is not on the pool: the user removed it themselves.

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "no longer declared") {
		t.Errorf("warning = %q, want none for a volume that is gone", errOut.String())
	}
	if got := client.Instances["dev-example-project"].Config[managedVolumesKey]; got != "" {
		t.Errorf("record = %q, want the gone and the malformed entries dropped", got)
	}
}

// rebuild carries the volume record across, since the record lives on the
// instance it destroys.
func TestRebuildKeepsTheVolumeRecord(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache",
		},
	})
	client.Volumes["default/dev-example-project-cache"] = true

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	got := client.Instances["dev-example-project"].Config[managedVolumesKey]
	if !strings.Contains(got, "dev-example-project-cache") {
		t.Errorf("record = %q, want the volume still reachable after a rebuild", got)
	}

	// And it can still be deleted.
	if err := app.Destroy(context.Background(), DestroyOptions{Volumes: true}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if client.Volumes["default/dev-example-project-cache"] {
		t.Error("the carried volume was not deleted")
	}
}

// Taking the idmap over removes the shift idev had set.
//
// idev sets no shift of its own then, and replacing the device is what clears
// the one from before: left in place beside the user's raw.idmap it would map
// the workspace twice, with no edit to dev.yml able to undo it.
func TestUpClearsShiftWhenTheUserTakesOverTheIDMap(t *testing.T) {
	cfg := mustParse(t, rootYAML+"  config:\n    raw.idmap: \"both 1000 0\"\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
		Devices: map[string]incus.Device{
			"workspace": {"type": "disk", "path": "/workspace", "source": "/old", "shift": "true"},
		},
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if got := client.Instances["dev-example-project"].Devices["workspace"]; got["shift"] != "" {
		t.Errorf("workspace = %v, want the shift idev set to be gone", got)
	}
}

// Failures reading the pool while pruning, and writing the carried record,
// reach the caller.
func TestVolumeRecordErrors(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	newFake := func() *incustest.Fake {
		client := incustest.New()
		client.AddInstance(&incus.Instance{
			Name:     "dev-example-project",
			Status:   "Running",
			Profiles: []string{"default"},
			Config: map[string]string{
				managedProjectKey: "example-project",
				managedVolumesKey: "default/dev-example-project-cache",
			},
		})
		client.Volumes["default/dev-example-project-cache"] = true
		return client
	}

	newApp := func(client *incustest.Fake) *App {
		return NewApp(AppOptions{
			Config: cfg, Client: client, Runner: &runnertest.Fake{},
			Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
		})
	}

	// A pool that cannot be reached must not stop up: nothing declared needs
	// the recorded volume, and the record is only there to be tidied.
	t.Run("reading the pool while pruning", func(t *testing.T) {
		client := newFake()
		client.Hook = func(call string) error {
			if strings.HasPrefix(call, "volume exists gone ") {
				return errBoom
			}
			return nil
		}
		client.Instances["dev-example-project"].Config[managedVolumesKey] = "gone/dev-example-project-old"

		if err := newApp(client).Up(context.Background(), UpOptions{}); err != nil {
			t.Errorf("Up() error = %v, want an unreachable pool not to stop the run", err)
		}
		if got := client.Instances["dev-example-project"].Config[managedVolumesKey]; got == "" {
			t.Error("the record was dropped although it could not be checked")
		}
	})

	t.Run("deleting a volume", func(t *testing.T) {
		client := newFake()
		client.Hook = func(call string) error {
			if strings.HasPrefix(call, "volume delete") {
				return errBoom
			}
			return nil
		}

		err := newApp(client).Destroy(context.Background(), DestroyOptions{Volumes: true})
		if !errors.Is(err, errBoom) {
			t.Errorf("error = %v, want %v", err, errBoom)
		}
	})

}

// An instance last used from another checkout is said out loud.
//
// With the default scope two checkouts share one instance, and up repoints the
// workspace at whichever ran last, so the other one quietly builds this tree.
func TestUpWarnsWhenTheCheckoutChanged(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedRootKey:    "/home/u/other-checkout",
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	for _, want := range []string{"/home/u/other-checkout", "project.scope"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("warning = %q, want it to mention %q", errOut.String(), want)
		}
	}

	// The same checkout says nothing.
	errOut.Reset()
	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "last used from") {
		t.Errorf("second run = %q, want no warning for the same checkout", errOut.String())
	}
}

// status reports the image the instance was made from, not the declaration.
func TestStatusReportsTheInstanceImage(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedImageKey:   "images:debian/12",
		},
		Devices: map[string]incus.Device{
			"workspace": {"type": "disk", "source": "/home/u/other-checkout", "path": "/workspace"},
		},
	})

	out := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Status(context.Background(), false); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !strings.Contains(out.String(), "images:debian/12") {
		t.Errorf("status =\n%s\nwant the image the instance was made from", out.String())
	}
	if !strings.Contains(out.String(), "/home/u/other-checkout") {
		t.Errorf("status =\n%s\nwant the workspace actually mounted", out.String())
	}
	// And the declaration beside it, so the row is not quietly one or the
	// other.
	if !strings.Contains(out.String(), "images:ubuntu/24.04") {
		t.Errorf("status =\n%s\nwant the declared image shown as differing", out.String())
	}
}

// provision checks the same prerequisites, and only for the steps selected.
func TestProvisionChecksPrerequisitesOfSelectedSteps(t *testing.T) {
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
  - name: tools
    run: "true"
  - name: playbook step
    ansible:
      playbook: .incus-dev/ansible/site.yml
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = root

	newApp := func(client *incustest.Fake) *App {
		return NewApp(AppOptions{
			Config: cfg, Client: client,
			Runner: &runnertest.Fake{Err: map[string]error{"ansible-playbook --version": errBoom}},
			Out:    &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
		})
	}

	t.Run("the ansible step is selected", func(t *testing.T) {
		client := incustest.New()
		managed(client, "Running")

		if err := newApp(client).Provision(context.Background(), provision.Selection{}); err == nil {
			t.Fatal("Provision() = nil error, want it to stop on the missing prerequisite")
		}
		for _, call := range client.Calls {
			if strings.HasPrefix(call, "exec ") {
				t.Errorf("calls = %v, want nothing run in the instance", client.Calls)
				break
			}
		}
	})

	t.Run("only a run step is selected", func(t *testing.T) {
		client := incustest.New()
		managed(client, "Running")

		if err := newApp(client).Provision(context.Background(), provision.Selection{Only: []string{"tools"}}); err != nil {
			t.Errorf("Provision() error = %v, want ansible not to be required", err)
		}
	})
}

// When unsetting fails while switching idmap strategies, the reason reaches the
// caller.
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

	err := appWith(t, rootYAML+"workspace:\n  idmap: shift\n", client).Up(context.Background(), UpOptions{})
	if !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// Running, but with no change needing a restart, produces no warning.
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

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart") {
		t.Errorf("warned despite nothing having changed: %q", errOut.String())
	}
}

// Unsetting a raw.idmap idev set needs a restart too.
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

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(errOut.String(), idmapConfigKey) {
		t.Errorf("warning = %q", errOut.String())
	}
}

// A provisioning failure under up propagates too.
func TestUpPropagatesProvisionError(t *testing.T) {
	client := incustest.New()
	client.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) { return 1, errBoom }

	err := appWith(t, rootYAML+"provision:\n  - run: failing\n", client).Up(context.Background(), UpOptions{})
	if !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// shell returns the container command's exit code as an ExitCodeError.
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

// With no profiles, status omits that row.
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
		t.Errorf("status = %q, want empty rows left out", out.String())
	}
}

// When reading the state before starting fails.
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

	if err := appWith(t, rootYAML, client).Up(context.Background(), UpOptions{}); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// The list of keys needing a restart is not mixed up (spec 05-incus.md 5.4.5).
// Removing a key that needs a restart warns too.
//
// Only the "set" half of restartRequiredChanges was covered: the loop over the
// unset keys could be deleted with the suite still green, so dropping
// security.nesting from dev.yml on a running instance said nothing about the
// restart it needs (spec 05-incus.md 5.4.5).
func TestRestartRequiredForRemovedKeys(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey:  "example-project",
			managedKeysKey:     "security.nesting",
			"security.nesting": "true",
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	// The restart warning itself, not any other line that happens to name the
	// key: "Removing config no longer declared" mentions it too.
	line := lineContaining(errOut.String(), "restart it to apply")
	if !strings.Contains(line, "security.nesting") {
		t.Errorf("restart warning = %q, want it to name the removed key", line)
	}
}

// lineContaining returns the first line holding sub, or "".
func lineContaining(out, sub string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}

// up --restart restarts the instance when run in answer to the warning.
//
// The warning is emitted by the run that writes the key, so by the time the
// user acts on it the config already matches the declaration and there is
// nothing left to compare. Remembering that a restart is owed is what makes
// the advice work (spec 05-incus.md 5.4.5).
func TestRestartIsOwedUntilItHappens(t *testing.T) {
	cfg := mustParse(t, rootYAML+"  config:\n    security.nesting: \"true\"\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(errOut.String(), "restart it to apply") {
		t.Fatalf("first run = %q, want the warning", errOut.String())
	}

	// A plain run keeps saying it.
	errOut.Reset()
	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(errOut.String(), "security.nesting") {
		t.Errorf("second run = %q, want the restart still owed", errOut.String())
	}

	// And --restart does it.
	client.Calls = nil
	if err := app.Up(context.Background(), UpOptions{Restart: true}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !client.Called("stop dev-example-project") || !client.Called("start dev-example-project") {
		t.Errorf("calls = %v, want the instance restarted", client.Calls)
	}

	// Restarted by the user or by the host coming back up: the change is in
	// effect, so there is nothing left to say.
	client.Instances["dev-example-project"].LastUsedAt = time.Now().Add(time.Hour)
	errOut.Reset()
	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart it to apply") {
		t.Errorf("after an outside restart = %q, want nothing owed", errOut.String())
	}
	client.Instances["dev-example-project"].LastUsedAt = time.Time{}
	client.Instances["dev-example-project"].Config[managedRestartKey] =
		recordRestart(time.Time{}, map[string]bootedValue{"security.nesting": {value: "false", known: true}})

	// A stopped instance owes nothing: starting it applies everything.
	client.Instances["dev-example-project"].Status = "Stopped"
	client.Instances["dev-example-project"].Config[managedRestartKey] = "security.nesting"
	errOut.Reset()
	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart it to apply") {
		t.Errorf("stopped run = %q, want no restart owed when it is about to start", errOut.String())
	}
	if got := client.Instances["dev-example-project"].Config[managedRestartKey]; got != "" {
		t.Errorf("record = %q, want it cleared by the start", got)
	}

	// --restart on a run with nothing to apply restarts nothing.
	client.Calls = nil
	if err := app.Up(context.Background(), UpOptions{Restart: true}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if client.Called("stop dev-example-project") {
		t.Errorf("calls = %v, want no restart when nothing needs one", client.Calls)
	}

	// Once restarted, nothing is owed.
	errOut.Reset()
	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart it to apply") {
		t.Errorf("fourth run = %q, want no warning once it has been restarted", errOut.String())
	}
}

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

			if err := app.Up(context.Background(), UpOptions{}); err != nil {
				t.Fatalf("Up() error = %v", err)
			}
			// Matching against the whole output passes by accident, since the
			// workspace path and the like contain the key. Look at the warning
			// line itself.
			if got := warningLine(errOut.String()); !strings.Contains(got, key) {
				t.Errorf("warning = %q, want a warning about the change to %q", got, key)
			}
		})
	}
}

// warningLine returns the line of the warning that asks for a restart, or the
// empty string.
func warningLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "restart it to apply") {
			return line
		}
	}
	return ""
}

// A key the declaration dropped is not unset, so it is not warned about as a
// change. Otherwise the warning would appear on every run, having done nothing.
func TestNoWarningForKeysRemovedFromConfig(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey:     "example-project",
			"security.nesting":    "true",
			"security.privileged": "true",
			// The same value idev sets, so nothing changes.
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

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart") {
		t.Errorf("warning = %q, want no warning about an unchanged key", errOut.String())
	}
}

// Provisioning runs even without a network address. Some configurations never
// show one, and stopping here would leave no way around it.
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

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v, want a warning and then carrying on", err)
	}
	if !strings.Contains(errOut.String(), "network") {
		t.Errorf("warning = %q, want it to say no network address was assigned", errOut.String())
	}
	if len(client.Execs) == 0 {
		t.Error("no provisioning step ran")
	}
}

// A step selection that cannot be resolved is rejected before the instance is
// touched.
func TestProvisionRejectsUnknownStepBeforeTouchingInstance(t *testing.T) {
	client := incustest.New()

	err := appWith(t, rootYAML+"provision:\n  - name: only-one\n    run: \"true\"\n", client).
		Provision(context.Background(), provision.Selection{Only: []string{"nope"}})
	if err == nil {
		t.Fatal("Provision() = nil error, want error")
	}
	if len(client.Calls) != 0 {
		t.Errorf("calls = %v, want Incus untouched when the selection does not resolve", client.Calls)
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
			t.Errorf("output = %q, want it to contain %q", out.String(), want)
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

// status also shows the devices, what it operates on in Incus, and the runtime
// version (spec 04-cli.md 4.4).
func TestStatusShowsAdditionalFields(t *testing.T) {
	out := &bytes.Buffer{}
	cfg, err := config.Parse([]byte("schema: 1\nruntime:\n  version: \"1.0\"\n"+
		"project:\n  name: example-project\ninstance:\n  image: images:ubuntu/24.04\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	client := incustest.New().AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
		Devices: map[string]incus.Device{
			"workspace": {"type": "disk"},
			"gpu0":      {"type": "gpu"},
		},
	})
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{}, Out: out,
		IncusProject: "development",
		CheckIDMap:   func(int, int) error { return nil },
	})

	if err := app.Status(context.Background(), false); err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	text := out.String()
	for _, want := range []string{"gpu0(gpu), workspace(disk)", "Runtime:", "1.0", "development"} {
		if !strings.Contains(text, want) {
			t.Errorf("status =\n%s\nwant it to contain %q", text, want)
		}
	}
}

// --dry-run changes nothing in Incus (spec 04-cli.md 4.8).
func TestPlanDoesNotModifyAnything(t *testing.T) {
	client := incustest.New()
	out := &bytes.Buffer{}

	cfg, err := config.Parse([]byte(rootYAML+"provision:\n  - run: \"true\"\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Plan(context.Background()); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !strings.Contains(out.String(), "Create instance dev-example-project") {
		t.Errorf("output = %q", out.String())
	}

	for _, call := range client.Calls {
		for _, mutating := range []string{"create", "start", "delete", "config", "devices", "exec", "unset"} {
			if strings.HasPrefix(call, mutating) {
				t.Errorf("performed a mutating operation: %q", call)
			}
		}
	}
}

// dry-run still checks the prerequisites.
func TestPlanChecksPrerequisites(t *testing.T) {
	t.Run("a missing profile", func(t *testing.T) {
		client := incustest.New()
		client.Profiles = nil

		err := appWith(t, rootYAML+"  profiles:\n    - default\n", client).Plan(context.Background())
		if err == nil || !strings.Contains(err.Error(), "default") {
			t.Errorf("error = %v, want the missing profile reported", err)
		}
	})

	t.Run("idmap does not work", func(t *testing.T) {
		cfg, err := config.Parse([]byte(rootYAML+"workspace:\n  idmap: raw\n"), config.Options{})
		if err != nil {
			t.Fatal(err)
		}
		cfg.Root = t.TempDir()

		app := NewApp(AppOptions{
			Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
			Out: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return errBoom },
		})
		if err := app.Plan(context.Background()); !errors.Is(err, errBoom) {
			t.Errorf("error = %v, want %v", err, errBoom)
		}
	})
}

func TestPlanRefusesUnmanagedInstance(t *testing.T) {
	client := incustest.New().AddInstance(&incus.Instance{Name: "dev-example-project", Status: "Running"})

	err := appWith(t, rootYAML, client).Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Errorf("error = %v", err)
	}
}

func TestPlanPropagatesLookupError(t *testing.T) {
	client := incustest.New()
	client.FailOn = map[string]error{"instance": errBoom}

	if err := appWith(t, rootYAML, client).Plan(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

func TestPlanReportsWriteError(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	app := NewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
		Out: errWriter{}, CheckIDMap: func(int, int) error { return nil },
	})
	if err := app.Plan(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// dry-run warns about the idmap fallback too.
func TestPlanWarnsOnIDMapFallback(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut,
		CheckIDMap: func(int, int) error { return errBoom },
	})

	if err := app.Plan(context.Background()); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !strings.Contains(errOut.String(), "shift") {
		t.Errorf("warning = %q, want it to say it fell back", errOut.String())
	}
}

// shell and exec follow the shell settings in dev.yml (spec 3.13).
func TestShellUsesConfiguredSettings(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")

	var got incus.ExecOptions
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		got = opt
		return 0, nil
	}

	body := rootYAML + "shell:\n  command: /bin/bash\n  cwd: /workspace/src\n  user: \"1000\"\n"
	if err := appWith(t, body, client).Shell(context.Background(), nil); err != nil {
		t.Fatalf("Shell() error = %v", err)
	}

	if diff := cmp.Diff([]string{"/bin/bash"}, client.Execs[0]); diff != "" {
		t.Errorf("argv mismatch (-want +got):\n%s", diff)
	}
	if got.Cwd != "/workspace/src" || got.User != "1000" {
		t.Errorf("ExecOptions = %+v", got)
	}
}

// A user name is switched to with su, since the exec API only accepts a uid.
func TestShellWithNamedUser(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")

	body := rootYAML + "shell:\n  user: developer\n  command: /bin/bash\n"
	if err := appWith(t, body, client).Shell(context.Background(), []string{"make", "test"}); err != nil {
		t.Fatalf("Shell() error = %v", err)
	}

	want := []string{"su", "-s", "/bin/bash", "developer", "-c", `'make' 'test'`}
	if diff := cmp.Diff(want, client.Execs[0]); diff != "" {
		t.Errorf("argv mismatch (-want +got):\n%s", diff)
	}
}

// exec allocates no terminal.
func TestExecDoesNotAllocateTTY(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")

	var tty bool
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		tty = opt.TTY
		return 0, nil
	}

	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, Interactive: true, // exec allocates none even with a terminal
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Exec(context.Background(), []string{"make", "test"}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if tty {
		t.Error("want exec not to allocate a pseudo-terminal")
	}
}

// Of the settings and devices the declaration dropped, only what idev created
// is undone (spec 05-incus.md 5.4.4).
func TestUpRemovesUndeclaredManagedConfig(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Stopped",
		Config: map[string]string{
			managedProjectKey:  "example-project",
			managedKeysKey:     "limits.cpu,limits.memory",
			managedDevicesKey:  "extdata,workspace",
			"limits.cpu":       "8",
			"limits.memory":    "16GiB", // dropped from the declaration
			"security.nesting": "true",  // added by the user, by hand
		},
		Devices: map[string]incus.Device{
			"workspace": {"type": "disk"},
			"extdata":   {"type": "disk"}, // dropped from the declaration
			"manual":    {"type": "nic"},  // added by the user, by hand
		},
	})

	body := rootYAML + "  config:\n    limits.cpu: \"8\"\n"
	if err := appWith(t, body, client).Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	cfg := client.Instances["dev-example-project"].Config
	if _, ok := cfg["limits.memory"]; ok {
		t.Error("a key the declaration dropped survived")
	}
	if _, ok := cfg["security.nesting"]; !ok {
		t.Error("removed a key the user added by hand")
	}

	devices := client.Instances["dev-example-project"].Devices
	if _, ok := devices["extdata"]; ok {
		t.Error("a device the declaration dropped survived")
	}
	if _, ok := devices["manual"]; !ok {
		t.Error("removed a device the user added by hand")
	}
}

// With --restart, a change needing a restart gets one.
func TestUpRestartsWhenRequested(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
	})

	body := rootYAML + "  config:\n    security.nesting: \"true\"\n"
	if err := appWith(t, body, client).Up(context.Background(), UpOptions{Restart: true}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	if !client.Called("stop dev-example-project") {
		t.Errorf("calls = %v, nothing was restarted", client.Calls)
	}
	if !client.Instances["dev-example-project"].IsRunning() {
		t.Error("it stayed stopped after the restart")
	}
}

// With no restart needed, --restart stops nothing.
func TestUpDoesNotRestartWithoutChanges(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			idmapConfigKey:    fmt.Sprintf("uid %d 0\ngid %d 0", os.Getuid(), os.Getgid()),
		},
	})

	if err := appWith(t, rootYAML, client).Up(context.Background(), UpOptions{Restart: true}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if client.Called("stop") {
		t.Errorf("calls = %v, want no restart when nothing changed", client.Calls)
	}
}

func TestUpPropagatesRestartErrors(t *testing.T) {
	for _, failOn := range []string{"stop", "start"} {
		t.Run(failOn, func(t *testing.T) {
			client := incustest.New()
			client.AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: map[string]string{managedProjectKey: "example-project"},
			})
			client.FailOn = map[string]error{failOn: errBoom}

			body := rootYAML + "  config:\n    security.nesting: \"true\"\n"
			err := appWith(t, body, client).Up(context.Background(), UpOptions{Restart: true})
			if !errors.Is(err, errBoom) {
				t.Errorf("error = %v, want %v", err, errBoom)
			}
		})
	}
}

func TestUpPropagatesRemoveDeviceError(t *testing.T) {
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Stopped",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedDevicesKey: "gone,workspace",
		},
		Devices: map[string]incus.Device{"gone": {"type": "disk"}},
	})
	client.FailOn = map[string]error{"removedevices": errBoom}

	if err := appWith(t, rootYAML, client).Up(context.Background(), UpOptions{}); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// The snapshot operations (spec 09-roadmap.md).
func TestSnapshotOperations(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")
	out := &bytes.Buffer{}

	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, CheckIDMap: func(int, int) error { return nil },
	})
	ctx := context.Background()

	// List, with none.
	if err := app.ListSnapshots(ctx); err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if !strings.Contains(out.String(), "no snapshots") {
		t.Errorf("output = %q", out.String())
	}

	// Create with a name.
	if err := app.CreateSnapshot(ctx, "before-upgrade"); err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	if !client.Called("snapshot create dev-example-project before-upgrade") {
		t.Errorf("calls = %v", client.Calls)
	}

	// Omitting the name gives it the current date and time.
	if err := app.CreateSnapshot(ctx, ""); err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	snapshots := client.SnapshotsByInstance["dev-example-project"]
	if len(snapshots) != 2 {
		t.Fatalf("snapshots = %v", snapshots)
	}
	if _, err := time.Parse("20060102-150405", snapshots[1].Name); err != nil {
		t.Errorf("auto-generated name = %q, want the date-time format", snapshots[1].Name)
	}

	// List.
	out.Reset()
	if err := app.ListSnapshots(ctx); err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if !strings.Contains(out.String(), "before-upgrade") {
		t.Errorf("output = %q", out.String())
	}

	// Restore and delete.
	if err := app.RestoreSnapshot(ctx, "before-upgrade"); err != nil {
		t.Fatalf("RestoreSnapshot() error = %v", err)
	}
	if !client.Called("snapshot restore dev-example-project before-upgrade") {
		t.Errorf("calls = %v", client.Calls)
	}
	if err := app.DeleteSnapshot(ctx, "before-upgrade"); err != nil {
		t.Fatalf("DeleteSnapshot() error = %v", err)
	}
	if len(client.SnapshotsByInstance["dev-example-project"]) != 1 {
		t.Errorf("it was not deleted: %v", client.SnapshotsByInstance)
	}
}

// The snapshot operations are limited to instances idev manages too.
func TestSnapshotRequiresManagedInstance(t *testing.T) {
	ctx := context.Background()

	ops := map[string]func(*App) error{
		"create":  func(a *App) error { return a.CreateSnapshot(ctx, "x") },
		"list":    func(a *App) error { return a.ListSnapshots(ctx) },
		"restore": func(a *App) error { return a.RestoreSnapshot(ctx, "x") },
		"delete":  func(a *App) error { return a.DeleteSnapshot(ctx, "x") },
	}

	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			client := incustest.New().AddInstance(&incus.Instance{
				Name: "dev-example-project", Status: "Running",
			})

			if err := op(appWith(t, rootYAML, client)); err == nil {
				t.Error("error = nil, want a failure on an unmanaged instance")
			}
		})
	}
}

func TestSnapshotPropagatesErrors(t *testing.T) {
	ctx := context.Background()

	ops := map[string]struct {
		failOn string
		call   func(*App) error
	}{
		"create":  {"snapshot create", func(a *App) error { return a.CreateSnapshot(ctx, "x") }},
		"list":    {"snapshot list", func(a *App) error { return a.ListSnapshots(ctx) }},
		"restore": {"snapshot restore", func(a *App) error { return a.RestoreSnapshot(ctx, "x") }},
		"delete":  {"snapshot delete", func(a *App) error { return a.DeleteSnapshot(ctx, "x") }},
	}

	for name, tt := range ops {
		t.Run(name, func(t *testing.T) {
			client := incustest.New()
			managed(client, "Running")
			client.FailOn = map[string]error{tt.failOn: errBoom}

			if err := tt.call(appWith(t, rootYAML, client)); !errors.Is(err, errBoom) {
				t.Errorf("error = %v, want %v", err, errBoom)
			}
		})
	}
}

func TestListSnapshotsWriteError(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")

	cfg, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: errWriter{}, CheckIDMap: func(int, int) error { return nil },
	})

	// When it is empty.
	if err := app.ListSnapshots(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
	// When there is one.
	client.SnapshotsByInstance = map[string][]incus.Snapshot{
		"dev-example-project": {{Name: "s1", CreatedAt: time.Now()}},
	}
	if err := app.ListSnapshots(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}
}

// Persistent volumes (spec 03-configuration.md 3.13).
func TestVolumeLifecycle(t *testing.T) {
	client := incustest.New()
	body := rootYAML + `
volumes:
  cache:
    path: /home/dev/.cache
    size: 10GiB
`

	// The volume is created and attached as a device.
	if err := appWith(t, body, client).Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !client.Volumes["default/dev-example-project-cache"] {
		t.Errorf("the volume was not created: %v", client.Volumes)
	}
	if !client.Called("volume create default dev-example-project-cache [size=10GiB]") {
		t.Errorf("calls = %v, want the size passed", client.Calls)
	}

	dev := client.Instances["dev-example-project"].Devices["cache"]
	if dev["type"] != "disk" || dev["pool"] != "default" ||
		dev["source"] != "dev-example-project-cache" || dev["path"] != "/home/dev/.cache" {
		t.Errorf("device = %v", dev)
	}

	// destroy keeps it by default.
	if err := appWith(t, body, client).Destroy(context.Background(), DestroyOptions{}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if !client.Volumes["default/dev-example-project-cache"] {
		t.Error("destroy deleted the volume as well")
	}

	// Only --volumes deletes it.
	if err := appWith(t, body, client).Up(context.Background(), UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := appWith(t, body, client).Destroy(context.Background(), DestroyOptions{Volumes: true}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if client.Volumes["default/dev-example-project-cache"] {
		t.Error("--volumes did not delete the volume")
	}
}

// An existing volume is not recreated.
func TestVolumeIsReusedWhenPresent(t *testing.T) {
	client := incustest.New()
	client.Volumes = map[string]bool{"default/dev-example-project-cache": true}

	body := rootYAML + "volumes:\n  cache:\n    path: /cache\n"
	if err := appWith(t, body, client).Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if client.Called("volume create") {
		t.Errorf("calls = %v, want an existing volume left alone", client.Calls)
	}
}

// rebuild keeps the volumes.
func TestRebuildKeepsVolumes(t *testing.T) {
	client := incustest.New()
	body := rootYAML + "volumes:\n  cache:\n    path: /cache\n"

	if err := appWith(t, body, client).Up(context.Background(), UpOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := appWith(t, body, client).Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if !client.Volumes["default/dev-example-project-cache"] {
		t.Error("rebuild deleted the volume")
	}
}

func TestVolumePropagatesErrors(t *testing.T) {
	body := rootYAML + "volumes:\n  cache:\n    path: /cache\n"

	for _, failOn := range []string{"volume exists", "volume create"} {
		t.Run(failOn, func(t *testing.T) {
			client := incustest.New()
			client.FailOn = map[string]error{failOn: errBoom}

			if err := appWith(t, body, client).Up(context.Background(), UpOptions{}); !errors.Is(err, errBoom) {
				t.Errorf("error = %v, want %v", err, errBoom)
			}
		})
	}

	t.Run("volume delete", func(t *testing.T) {
		client := incustest.New()
		client.Volumes = map[string]bool{"default/dev-example-project-cache": true}
		managed(client, "Running")
		client.FailOn = map[string]error{"volume delete": errBoom}

		err := appWith(t, body, client).Destroy(context.Background(), DestroyOptions{Volumes: true})
		if !errors.Is(err, errBoom) {
			t.Errorf("error = %v, want %v", err, errBoom)
		}
	})
}

// Secrets are read from the host and handed to the steps
// (spec 03-configuration.md 3.12).
func TestSecretsAreInjected(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")

	var got incus.ExecOptions
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		got = opt
		return 0, nil
	}

	cfg, err := config.Parse([]byte(rootYAML+`
secrets:
  API_TOKEN:
    env: HOST_TOKEN
provision:
  - run: deploy
`), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	app := NewApp(AppOptions{
		Config: cfg,
		Client: client,
		Runner: &runnertest.Fake{},
		Out:    &bytes.Buffer{},
		LookupEnv: func(k string) (string, bool) {
			return "s3cret-from-host", k == "HOST_TOKEN"
		},
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Provision(context.Background(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	if got.Env["API_TOKEN"] != "s3cret-from-host" {
		t.Errorf("env = %v, want the value read from the host passed", got.Env)
	}
	// They do not mix with the variables that may be displayed (spec 04-cli.md 4.10).
	if _, ok := got.PublicEnv["API_TOKEN"]; ok {
		t.Errorf("public env = %v, want it not to carry the secret", got.PublicEnv)
	}
}

// A secret that cannot be read stops it before the instance is touched.
func TestProvisionFailsOnMissingSecret(t *testing.T) {
	client := incustest.New()
	managed(client, "Running")

	cfg, err := config.Parse([]byte(rootYAML+"secrets:\n  API_TOKEN:\n    env: NOT_SET\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out:        &bytes.Buffer{},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckIDMap: func(int, int) error { return nil },
	})

	err = app.Provision(context.Background(), provision.Selection{})
	if err == nil || !strings.Contains(err.Error(), "API_TOKEN") {
		t.Errorf("error = %v, want the missing secret reported", err)
	}
	if len(client.Execs) != 0 {
		t.Error("ran a step despite the secret not resolving")
	}
}

// A secret that does not resolve stops it before the instance is created.
func TestUpFailsBeforeCreatingOnMissingSecret(t *testing.T) {
	client := incustest.New()

	cfg, err := config.Parse([]byte(rootYAML+"secrets:\n  API_TOKEN:\n    env: NOT_SET\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out:        &bytes.Buffer{},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err == nil {
		t.Fatal("Up() = nil error, want error")
	}
	if client.Called("create") {
		t.Errorf("calls = %v, created the instance despite an unmet prerequisite", client.Calls)
	}
}

// dry-run checks the secrets prerequisite too.
func TestPlanChecksSecrets(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML+"secrets:\n  API_TOKEN:\n    env: NOT_SET\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Root = t.TempDir()

	app := NewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
		Out:        &bytes.Buffer{},
		LookupEnv:  func(string) (string, bool) { return "", false },
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "API_TOKEN") {
		t.Errorf("error = %v", err)
	}
}

// A command reaches the container unchanged when it runs as a named user.
//
// su -c takes one string, so the argv has to be quoted for the shell rather
// than joined: without that, an argument holding a space or a shell
// metacharacter is re-split inside the container.
func TestAsUserKeepsArgumentBoundaries(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"a space in an argument", []string{"cat", "my file"}, `'cat' 'my file'`},
		{"shell metacharacters", []string{"bash", "-lc", "cd sub && make"}, `'bash' '-lc' 'cd sub && make'`},
		{"a single quote", []string{"echo", "it's"}, `'echo' 'it'\''s'`},
		{"nothing to quote", []string{"make", "test"}, `'make' 'test'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, user := asUser(tt.argv, config.Shell{User: "developer", Command: "/bin/sh"})

			if user != "" {
				t.Errorf("user = %q, want it switched inside the container", user)
			}
			want := []string{"su", "-s", "/bin/sh", "developer", "-c", tt.want}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("asUser() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// A destroy that fails after the daemon has already deleted the instance
// leaves the undeclared volumes with nothing left to name them: the record
// naming them went with the instance. --volumes says so; plain destroy, the
// more common command and the one that keeps the volumes, returned the bare
// error.
func TestDestroyNamesTheUnnameableVolumesWhenItFails(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache,default/dev-example-project-old",
		},
	})
	client.FailOn = map[string]error{"delete dev-example-project": context.Canceled}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Destroy(context.Background(), DestroyOptions{})
	if err == nil {
		t.Fatal("Destroy() = nil error, want the failure reported")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Destroy() error = %v, want it to wrap the cause", err)
	}
	// The declared one is adopted by the next up; the undeclared one is what
	// nothing will name again.
	if !strings.Contains(err.Error(), "dev-example-project-old") {
		t.Errorf("Destroy() error = %q, want it to name the volume nothing else can", err)
	}
	if strings.Contains(err.Error(), "dev-example-project-cache") {
		t.Errorf("Destroy() error = %q, want it not to offer to delete a declared volume", err)
	}
}

// rebuild carries the volume record in memory across the destroy, so if the
// create half fails the record is gone from the only place it was durable.
// The next rebuild writes a record naming the declared volumes only, and a
// volume that had left the declaration -- the case rebuild is recommended for
// -- is never named by idev again.
func TestRebuildNamesTheCarriedRecordWhenTheCreateFails(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache,default/dev-example-project-old",
		},
	})
	client.FailOn = map[string]error{"create dev-example-project": context.Canceled}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Rebuild(context.Background())
	if err == nil {
		t.Fatal("Rebuild() = nil error, want the create failure reported")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Rebuild() error = %v, want it to wrap the cause", err)
	}
	if !strings.Contains(err.Error(), "dev-example-project-old") {
		t.Errorf("Rebuild() error = %q, want it to name the volume the record no longer holds", err)
	}
	// The declared one the next up adopts by name, so offering to delete it
	// would hand the user a command that destroys data idev is about to keep.
	if strings.Contains(err.Error(), "dev-example-project-cache") {
		t.Errorf("Rebuild() error = %q, want it not to offer to delete a declared volume", err)
	}
}

// With nothing but declared volumes there is nothing the next up cannot find,
// so the failure is reported on its own.
func TestRebuildAddsNothingWhenEveryVolumeIsDeclared(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache",
		},
	})
	client.FailOn = map[string]error{"create dev-example-project": context.Canceled}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Rebuild(context.Background())
	if err == nil {
		t.Fatal("Rebuild() = nil error, want the create failure reported")
	}
	if got := err.Error(); got != context.Canceled.Error() {
		t.Errorf("Rebuild() error = %q, want just the cause", got)
	}
}
