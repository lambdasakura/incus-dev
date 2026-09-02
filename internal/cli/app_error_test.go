package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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

	return MustNewApp(AppOptions{
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
		app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{}})
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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

	// The playbook run itself, not the prerequisite checks: ansible-playbook
	// --version has to succeed, and ansible-doc has to print documentation,
	// which is how a present connection plugin is told from an absent one.
	cmdRunner := &runnertest.Fake{
		Err:    map[string]error{"ansible-playbook -i": errBoom},
		Stdout: map[string]string{"ansible-doc": "> COMMUNITY.GENERAL.INCUS    (connection plugin)\n"},
	}
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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
			app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

// The record is written into the creation request, so once the instance
// exists it is durable and the advice about a lost record must not appear.
// Following it would delete a volume the record still names, on the ordinary
// way for a rebuild to fail.
func TestRebuildDoesNotOfferToDeleteARecordedVolume(t *testing.T) {
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

	app := MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Rebuild(context.Background())
	if err == nil {
		t.Fatal("Rebuild() = nil error, want the failing step reported")
	}
	if strings.Contains(err.Error(), "storage volume delete") {
		t.Errorf("Rebuild() error = %q, want no advice to delete a volume the record still names", err)
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
	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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
		return MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
		return MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
			app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

	errOut := &bytes.Buffer{}
	app := MustNewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
		Out: out, ErrOut: errOut,
	})

	if err := app.ListSteps(); err != nil {
		t.Fatalf("ListSteps() error = %v", err)
	}
	// Said, but not as a row: stdout is one line per step.
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", out)
	}
	if !strings.Contains(errOut.String(), "No provision steps") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestListStepsWriteError(t *testing.T) {
	cfg, err := config.Parse([]byte(rootYAML+"provision:\n  - run: \"true\"\n"), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	app := MustNewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{}, Out: errWriter{},
	})

	if err := app.ListSteps(); !errors.Is(err, errBoom) {
		t.Errorf("error = %v, want %v", err, errBoom)
	}

	// With no steps there is nothing to write, so a broken stdout cannot
	// fail: the note about there being none goes to stderr.
	empty, err := config.Parse([]byte(rootYAML), config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	app = MustNewApp(AppOptions{
		Config: empty, Client: incustest.New(), Runner: &runnertest.Fake{}, Out: errWriter{},
	})
	if err := app.ListSteps(); err != nil {
		t.Errorf("error = %v, want none", err)
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
	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

		app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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
	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, CheckIDMap: func(int, int) error { return nil },
	})
	ctx := context.Background()

	// List, with none.
	if err := app.ListSnapshots(ctx); err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing for an empty list", out)
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

	app := MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: errWriter{}, CheckIDMap: func(int, int) error { return nil },
	})

	// When it is empty there is nothing to write, so a broken stdout cannot
	// fail.
	if err := app.ListSnapshots(context.Background()); err != nil {
		t.Errorf("error = %v, want none", err)
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

	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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
		Status: "Stopped",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache,default/dev-example-project-old",
		},
	})
	app := MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	// The one ambiguous case: the wait is cut short and the daemon goes on
	// deleting. The instance is still there when anything asks, which is why
	// this cannot be settled by looking.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := app.Destroy(ctx, DestroyOptions{})
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

	app := MustNewApp(AppOptions{
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

	app := MustNewApp(AppOptions{
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

// DeleteInstance fails at the lookup, at the force-stop, at a rejected
// request and at the wait; only the last leaves the instance possibly gone.
// On the others the record and the volumes are provably where they were, so
// advice to delete them by hand is advice to lose data for no reason.
func TestDestroyDoesNotOfferToDeleteWhileTheInstanceIsThere(t *testing.T) {
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
	client.FailOn = map[string]error{"delete dev-example-project": errBoom}

	app := MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Destroy(context.Background(), DestroyOptions{})
	if err == nil {
		t.Fatal("Destroy() = nil error, want the failure reported")
	}
	if strings.Contains(err.Error(), "storage volume delete") {
		t.Errorf("Destroy() error = %q, want no delete advice while the instance is still there", err)
	}
}

// project.name accepts dots, underscores and capitals, all of which the
// instance name drops, so several names the schema treats as distinct claim
// one instance. The loser was told the instance "is not managed by idev",
// which reads as someone having made it by hand.
func TestUnmanagedErrorExplainsANameCollision(t *testing.T) {
	cfg := mustParse(t, "schema: 1\nproject:\n  name: My.Project\n"+
		"instance:\n  image: images:ubuntu/24.04\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-my-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "my_project"},
	})

	app := MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Shell(context.Background(), []string{"true"})
	if err == nil {
		t.Fatal("Shell() = nil error, want the collision reported")
	}
	if !strings.Contains(err.Error(), "my_project") {
		t.Errorf("error = %q, want it to name the project that owns the instance", err)
	}
	if strings.Contains(err.Error(), "remove the instance manually") {
		t.Errorf("error = %q, want it not to suggest deleting another project's environment", err)
	}
}

// project.scope puts a suffix on the instance name, which both projects share,
// so the explanation has to compare the names rather than the instances.
func TestUnmanagedErrorExplainsACollisionUnderScope(t *testing.T) {
	cfg := mustParse(t, "schema: 1\nproject:\n  name: My.Project\n  scope: branch\n"+
		"instance:\n  image: images:ubuntu/24.04\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-my-project-main",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "my_project"},
	})

	app := MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Branch: func() (string, error) { return "main", nil },
		Out:    &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Shell(context.Background(), []string{"true"})
	if err == nil {
		t.Fatal("Shell() = nil error, want the collision reported")
	}
	if !strings.Contains(err.Error(), "differ only in characters") {
		t.Errorf("error = %q, want the normalisation explained under scope too", err)
	}
}

// An instance idev owns whose recorded project does not derive this name --
// renamed by hand, say -- is still two projects claiming one instance, but
// the normalisation explanation would be false, so it is not given.
func TestUnmanagedErrorWithoutANormalisationExplanation(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "totally-different"},
	})

	app := MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Shell(context.Background(), []string{"true"})
	if err == nil {
		t.Fatal("Shell() = nil error, want the clash reported")
	}
	if !strings.Contains(err.Error(), "totally-different") {
		t.Errorf("error = %q, want it to name the project that owns the instance", err)
	}
	if strings.Contains(err.Error(), "differ only in characters") {
		t.Errorf("error = %q, want no normalisation explanation: these names do not collide that way", err)
	}
}

// An instance genuinely made by hand still gets the original advice.
func TestUnmanagedErrorForAnInstanceIdevDidNotMake(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{Name: "dev-example-project", Status: "Running"})

	app := MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Shell(context.Background(), []string{"true"})
	if err == nil {
		t.Fatal("Shell() = nil error, want the unmanaged instance reported")
	}
	if !strings.Contains(err.Error(), "not managed by idev") {
		t.Errorf("error = %q, want the unmanaged-instance wording", err)
	}
}

// The instance is still present when the wait is cut short -- the daemon has
// not finished -- so a check that looks would answer "still there" in exactly
// the case the advice exists for. The failure itself is what says so.
// Stopped, because that is the only way the delete is ever sent: the real
// client force-stops first and that refuses outright once the context is done,
// so a running instance never reaches the step whose outcome is in doubt.
func TestDestroyAdvisesWhileTheInstanceIsStillPresent(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Stopped",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache,default/dev-example-project-old",
		},
	})

	app := MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := app.Destroy(ctx, DestroyOptions{})
	if err == nil {
		t.Fatal("Destroy() = nil error, want the interrupted wait reported")
	}
	if _, present := client.Instances["dev-example-project"]; !present {
		t.Fatal("the fake deleted the instance; this test needs it still there")
	}
	if !strings.Contains(err.Error(), "dev-example-project-old") {
		t.Errorf("Destroy() error = %q, want the volume named even though the instance is still there", err)
	}
}

// A volume key becomes a device name (max 63) and, with the instance name in
// front, a storage volume name (max 64). Only the second depends on the
// instance, so only the second can be checked here rather than by the schema.
func TestValidateRefusesAVolumeNameIncusCannotHold(t *testing.T) {
	// dev-example-project is 19 characters, so 45 makes a 65-character name.
	cfg := mustParse(t, rootYAML+"volumes:\n  "+strings.Repeat("a", 45)+":\n    path: /cache\n")

	app := MustNewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Validate()
	if err == nil {
		t.Fatal("Validate() = nil error, want the derived volume name refused")
	}
	if !strings.Contains(err.Error(), "64") {
		t.Errorf("Validate() error = %q, want it to name the limit", err)
	}
}

// One character shorter fits, so it is the length being refused.
func TestValidateAcceptsTheLongestVolumeNameThatFits(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  "+strings.Repeat("a", 44)+":\n    path: /cache\n")

	app := MustNewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want a 64-character volume name accepted", err)
	}
}

// rebuild stashes the volume record in memory because the instance holding it
// is about to be deleted. If anything creates the instance in that gap -- a
// second idev, most plausibly -- up takes the existing-instance branch, which
// neither applied the carried record nor reported it lost, and then succeeded.
// The user is told the volume is kept for the next up to adopt, and it is not.
func TestRebuildKeepsTheCarriedRecordWhenSomethingElseCreatedTheInstance(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache,default/dev-example-project-old",
		},
	})
	client.Volumes["default/dev-example-project-old"] = true

	// The gap: something puts the instance back between the delete and the
	// lookup up makes next, with a record of its own that knows nothing of
	// the old volume. The hook runs before each call, so this arms on the
	// delete and fires on the lookup after it.
	deleted, replaced := false, false
	client.Hook = func(call string) error {
		switch {
		case strings.HasPrefix(call, "delete dev-example-project"):
			deleted = true
		case deleted && !replaced && strings.HasPrefix(call, "instance dev-example-project"):
			replaced = true
			client.AddInstance(&incus.Instance{
				Name:     "dev-example-project",
				Status:   "Running",
				Profiles: []string{"default"},
				Config: map[string]string{
					managedProjectKey: "example-project",
					managedVolumesKey: "default/dev-example-project-cache",
				},
			})
		}
		return nil
	}

	if err := app(t, cfg, client).Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	got := client.Instances["dev-example-project"].Config[managedVolumesKey]
	if !strings.Contains(got, "dev-example-project-old") {
		t.Errorf("record = %q, want the carried volume folded back in", got)
	}
}

// And once it is folded in, it is no longer carried: a later failure must not
// call it lost and offer to delete a volume the record names.
func TestAdoptingTheCarriedRecordStopsCarryingIt(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n"+
		"provision:\n  - run: \"false\"\n")

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
	client.ExecFunc = func(string, []string, incus.ExecOptions) (int, error) { return 1, nil }

	// Through Rebuild, because recordLostWith is what would say it, and
	// only Rebuild reaches it.
	client.Instances["dev-example-project"].Config[managedVolumesKey] =
		"default/dev-example-project-cache,default/dev-example-project-old"
	client.Volumes["default/dev-example-project-old"] = true

	deleted, replaced := false, false
	client.Hook = func(call string) error {
		switch {
		case strings.HasPrefix(call, "delete dev-example-project"):
			deleted = true
		case deleted && !replaced && strings.HasPrefix(call, "instance dev-example-project"):
			replaced = true
			client.AddInstance(&incus.Instance{
				Name:     "dev-example-project",
				Status:   "Running",
				Profiles: []string{"default"},
				Config: map[string]string{
					managedProjectKey: "example-project",
					managedVolumesKey: "default/dev-example-project-cache",
				},
			})
		}
		return nil
	}

	err := app(t, cfg, client).Rebuild(context.Background())
	if err == nil {
		t.Fatal("Rebuild() = nil error, want the failing step reported")
	}
	if strings.Contains(err.Error(), "storage volume delete") {
		t.Errorf("Rebuild() error = %q, want no advice to delete a volume the record now names", err)
	}
}

// app builds an App around a config and a client, for the tests that need
// nothing else.
func app(t *testing.T, cfg *config.Config, client *incustest.Fake) *App {
	t.Helper()
	return MustNewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})
}

// validate, up and up --dry-run have to agree about a volume name Incus will
// refuse. It was checked in validate alone, so validate refused a dev.yml that
// the preflight then called fine and up created a volume for -- the weaker,
// offline check refusing more than the host-side one (spec 04-cli.md 4.7).
func TestEveryPathRefusesAVolumeNameIncusCannotHold(t *testing.T) {
	// dev-example-project is 19 characters, so 45 makes a 65-character name.
	cfg := mustParse(t, rootYAML+"volumes:\n  "+strings.Repeat("a", 45)+":\n    path: /cache\n")

	for _, tt := range []struct {
		name string
		run  func(*App) error
	}{
		{"validate", func(a *App) error { return a.Validate() }},
		{"up --dry-run", func(a *App) error { return a.Plan(context.Background()) }},
		{"up", func(a *App) error { return a.Up(context.Background(), UpOptions{}) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := incustest.New()

			err := tt.run(app(t, cfg, client))
			if err == nil {
				t.Fatal("= nil error, want the derived volume name refused")
			}
			if !strings.Contains(err.Error(), "64") {
				t.Errorf("error = %q, want it to name the limit", err)
			}
			if client.Called("volume create") {
				t.Errorf("calls = %v, want nothing created", client.Calls)
			}
		})
	}
}

// Two idevs, one project, one instance.
//
// idev decides what to record -- which volumes are its own above all -- from a
// reading of the instance, several calls before it writes the answer back.
// Another run reads and writes inside that window, and the later write erased
// what the earlier one had recorded: the volume dropped out of the record, and
// then destroy --volumes deleted what the record listed and silently left the
// rest on the pool, unnameable by any idev command.
//
// The write is refused now, and the user is told to run up again.
func TestUpRefusesToOverwriteAnotherRunsRecord(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

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

	// The other run: it lands after this one has read and before it writes,
	// recording a volume of its own.
	client.Hook = func(call string) error {
		if strings.HasPrefix(call, "volume exists") {
			client.Hook = nil
			other := client.Instances["dev-example-project"]
			other.Config[managedVolumesKey] += ",default/dev-example-project-other"
			client.Touch("dev-example-project")
		}
		return nil
	}

	err := app(t, cfg, client).Up(context.Background(), UpOptions{})
	if !errors.Is(err, incus.ErrChanged) {
		t.Fatalf("Up() error = %v, want ErrChanged", err)
	}
	if !strings.Contains(err.Error(), "idev up") {
		t.Errorf("Up() error = %q, want it to say what to do next", err)
	}

	// And nothing was lost: the other run's volume is still recorded.
	got := client.Instances["dev-example-project"].Config[managedVolumesKey]
	if !strings.Contains(got, "dev-example-project-other") {
		t.Errorf("record = %q, want the other run's volume still there", got)
	}
}

// A failed write leaves the record and the devices agreeing.
//
// idev removes the devices its own record says it has, so a record written
// ahead of the removal is a device nothing will ever take off again: the next
// up computes no stale devices, and warnUnrecorded stays quiet because the
// record exists. The volume behind it cannot be deleted either -- Incus
// refuses while it is attached.
func TestAFailedReapplyLeavesTheRecordMatchingTheDevices(t *testing.T) {
	cfg := mustParse(t, rootYAML) // no volumes: "extra" has left the declaration

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedDevicesKey: "extra,workspace",
		},
		Devices: map[string]incus.Device{
			"extra":     {"type": "disk", "source": "/srv", "path": "/extra"},
			"workspace": {"type": "disk", "source": "/old", "path": "/workspace"},
		},
	})
	client.FailOn = map[string]error{"removedevices": errBoom}

	if err := app(t, cfg, client).Up(context.Background(), UpOptions{}); err == nil {
		t.Fatal("Up() = nil error, want the failed removal reported")
	}

	inst := client.Instances["dev-example-project"]
	_, attached := inst.Devices["extra"]
	recorded := strings.Contains(inst.Config[managedDevicesKey], "extra")
	if attached != recorded {
		t.Errorf("device attached = %v but recorded = %v; the next up removes what the record lists, "+
			"so these disagreeing leaves it attached for good", attached, recorded)
	}
}

// Every write can meet a 412, so every write's failure has to say what to do.
//
// Only reapplyInstance passed its etag, but updateInstance falls back to a
// fresh read for the others, and a write can still lose to something landing
// between that read and the write. The user would have got the bare sentence
// with nothing to act on.
func TestEveryWriteExplainsAChangedInstance(t *testing.T) {
	cfg := mustParse(t, rootYAML+"  config:\n    security.nesting: \"true\"\n")

	// The two writes record the same prefix, so they are told apart by the
	// key each carries.
	for _, tt := range []struct{ name, carries string }{
		{"the main reapply", managedKeysKey},
		{"the restart record", managedRestartKey},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := incustest.New()
			client.AddInstance(&incus.Instance{
				Name:     "dev-example-project",
				Status:   "Running",
				Profiles: []string{"default"},
				Config:   map[string]string{managedProjectKey: "example-project"},
			})
			client.Hook = func(call string) error {
				if strings.HasPrefix(call, "config ") && strings.Contains(call, tt.carries) {
					return fmt.Errorf("update instance: %w", incus.ErrChanged)
				}
				return nil
			}

			err := app(t, cfg, client).Up(context.Background(), UpOptions{})
			if err == nil {
				t.Fatal("Up() = nil error, want the refused write reported")
			}
			if !errors.Is(err, incus.ErrChanged) {
				t.Fatalf("Up() error = %v, want ErrChanged", err)
			}
			if !strings.Contains(err.Error(), "idev up") {
				t.Errorf("Up() error = %q, want it to say what to do next", err)
			}
			// It must not say what was applied. This wraps writes at three
			// points in a run -- after volumes are made, after the
			// declaration has landed, after a stop and a start -- and there
			// is no one answer that is true at all of them.
			for _, claim := range []string{"left as it was", "nothing was applied"} {
				if strings.Contains(err.Error(), claim) {
					t.Errorf("Up() error = %q, want no claim about what was applied (%q)", err, claim)
				}
			}
		})
	}
}

// The two listings a script parses have one row per thing, always.
//
// "no provision steps declared" on stdout makes `provision --list | wc -l`
// answer 1 for none, and `cut -f1` yield a step named "no provision steps
// declared". Nothing is a legitimate answer, and the empty output says it.
func TestEmptyListingsWriteNoRows(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	t.Run("provision --list", func(t *testing.T) {
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		a := MustNewApp(AppOptions{
			Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
			Out: out, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
		})

		if err := a.ListSteps(); err != nil {
			t.Fatalf("ListSteps() error = %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want nothing: a caller counts rows", out)
		}
		if errOut.Len() == 0 {
			t.Error("nothing was said about there being no steps")
		}
	})

	t.Run("snapshot list", func(t *testing.T) {
		out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
		client := incustest.New()
		client.AddInstance(&incus.Instance{
			Name:   "dev-example-project",
			Status: "Running",
			Config: map[string]string{managedProjectKey: "example-project"},
		})
		a := MustNewApp(AppOptions{
			Config: cfg, Client: client, Runner: &runnertest.Fake{},
			Out: out, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
		})

		if err := a.ListSnapshots(context.Background()); err != nil {
			t.Fatalf("ListSnapshots() error = %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want nothing: a caller counts rows", out)
		}
		if errOut.Len() == 0 {
			t.Error("nothing was said about there being no snapshots")
		}
	})
}

// The carried record stops being carried the moment it is written, not when
// the function that wrote it returns.
//
// reapplyInstance also settles the restart record, so a failure there left the
// record on the instance and still in a.carried -- and rebuild then said it
// was lost and offered to delete the volumes it names. That is the false
// advice this whole line of fixes exists to remove, reached through the one
// window the last one left open.
func TestTheCarriedRecordIsClearedAsSoonAsItIsWritten(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache,default/dev-example-project-old",
		},
	})
	client.Volumes["default/dev-example-project-old"] = true

	deleted, replaced := false, false
	client.Hook = func(call string) error {
		switch {
		case strings.HasPrefix(call, "delete dev-example-project"):
			deleted = true
		case deleted && !replaced && strings.HasPrefix(call, "instance dev-example-project"):
			replaced = true
			client.AddInstance(&incus.Instance{
				Name:     "dev-example-project",
				Status:   "Running",
				Profiles: []string{"default"},
				Config: map[string]string{
					managedProjectKey: "example-project",
					managedVolumesKey: "default/dev-example-project-cache",
					managedRestartKey: "2026-01-01T00:00:00Z|security.nesting=false",
				},
			})
		}
		// The restart record is settled after the write that persists the
		// carried one, and shares its call prefix, so it is told apart by the
		// key it carries. Failing there must not make the record "lost".
		if strings.HasPrefix(call, "config ") && strings.Contains(call, managedRestartKey) {
			return errBoom
		}
		return nil
	}

	err := app(t, cfg, client).Rebuild(context.Background())
	if err == nil {
		t.Fatal("Rebuild() = nil error, want the failed restart write reported")
	}
	if strings.Contains(err.Error(), "storage volume delete") {
		t.Errorf("Rebuild() error = %q, want no advice to delete a volume the instance now records", err)
	}

	got := client.Instances["dev-example-project"].Config[managedVolumesKey]
	if !strings.Contains(got, "dev-example-project-old") {
		t.Errorf("record = %q, want the carried volume written", got)
	}
}

// clearRestartPending is a write like any other, and can lose to a 412 from
// the read updateInstance falls back on. Its failure has to say what to do.
func TestClearingTheRestartRecordExplainsAChangedInstance(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})

	// The clear is reached only once nothing is owed, so the first run settles
	// the declaration and records the restart, and the second clears it.
	a := app(t, cfg, client)
	if err := a.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("the settling run failed: %v", err)
	}
	client.Hook = func(call string) error {
		if strings.HasPrefix(call, "unset ") && strings.Contains(call, managedRestartKey) {
			return fmt.Errorf("update instance: %w", incus.ErrChanged)
		}
		return nil
	}

	err := a.Up(context.Background(), UpOptions{Restart: true})
	if !errors.Is(err, incus.ErrChanged) {
		t.Fatalf("Up() error = %v, want ErrChanged", err)
	}
	if !strings.Contains(err.Error(), "idev up") {
		t.Errorf("Up() error = %q, want it to say what to do next", err)
	}
}

// The advice about a lost volume record has been wrong four times, each in a
// different place, and each fix closed the one window it was shown. They all
// break the same invariant, so this asserts the invariant instead of the
// paths: idev may say a volume is unnameable only when the instance is not
// recording it.
//
// It fails rebuild at every call in turn, which is how a fifth window would be
// found without waiting for someone to walk into it.
func TestTheLostRecordAdviceMatchesWhatTheInstanceRecords(t *testing.T) {
	const carried = "default/dev-example-project-old"

	// Both halves of up: the one that creates the instance, and the one that
	// finds one -- every window so far has been in the second, which a plain
	// rebuild never reaches.
	for _, world := range []struct {
		name    string
		arrange func(*incustest.Fake)
		// leastCalls and mustReach are per world, because the two are not the
		// same size and one floor for both leaves the larger free to collapse
		// into the smaller. Raise them deliberately.
		leastCalls int
		mustReach  string
	}{
		{
			name:       "up creates the instance",
			arrange:    func(*incustest.Fake) {},
			leastCalls: 13,
			mustReach:  "create dev-example-project",
		},
		{
			name:    "something else creates it first",
			arrange: recreateAfterDelete,
			// The VolumeExists on the carried volume, inside
			// pruneVolumeRecord: it exists only on up's existing-instance
			// branch, which is where every window so far has been.
			leastCalls: 15,
			mustReach:  "volume exists default dev-example-project-old",
		},
	} {
		t.Run(world.name, func(t *testing.T) {
			runLostRecordInvariant(t, world.arrange, carried, world.leastCalls, world.mustReach)
		})
	}
}

// runLostRecordInvariant fails rebuild at each call in turn and checks the
// invariant after each.
func runLostRecordInvariant(t *testing.T, arrange func(*incustest.Fake), carried string,
	leastCalls int, mustReach string,
) {
	t.Helper()

	var calls []string
	{
		client, cfg := rebuildFixture(t)
		arrange(client)
		_ = app(t, cfg, client).Rebuild(context.Background())
		calls = append(calls, client.Calls...)
	}
	if len(calls) < leastCalls {
		t.Fatalf("rebuild made %d calls, want at least %d; the fixture has stopped "+
			"exercising it:\n%v", len(calls), leastCalls, calls)
	}
	if !slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, mustReach) }) {
		t.Fatalf("rebuild never reached %q; the fixture is exercising a different path:\n%v",
			mustReach, calls)
	}

	checked, swallowed := 0, 0
	for i, at := range calls {
		t.Run(fmt.Sprintf("%d %s", i, at), func(t *testing.T) {
			client, cfg := rebuildFixture(t)
			arrange(client)
			outer := client.Hook
			seen := 0
			client.Hook = func(call string) error {
				if outer != nil {
					if err := outer(call); err != nil {
						return err
					}
				}
				seen++
				if seen == i+1 {
					return errBoom
				}
				return nil
			}

			err := app(t, cfg, client).Rebuild(context.Background())
			if err == nil {
				// Some failures are swallowed on purpose --
				// warnStrandedInstances and pruneVolumeRecord both do that --
				// and rebuild then succeeds. The invariant still has to hold:
				// a run that succeeds while the record is lost is the worst
				// case there is, because nothing at all is printed. One of
				// these three calls is the VolumeExists on the carried volume
				// itself, which is precisely where that would happen.
				swallowed++
			}

			said := err != nil && strings.Contains(err.Error(), carried)
			recorded := false
			if inst, ok := client.Instances["dev-example-project"]; ok {
				recorded = strings.Contains(inst.Config[managedVolumesKey], "dev-example-project-old")
			}
			if !client.Volumes[carried] {
				t.Fatalf("the volume was deleted; rebuild keeps them, so this fixture no longer "+
					"exercises what it says it does (err = %v)", err)
			}

			checked++

			switch {
			case said && recorded:
				t.Errorf("idev called %s unnameable while the instance records it:\n%v", carried, err)
			case !said && !recorded:
				t.Errorf("%s is on the pool, the instance does not record it, "+
					"and idev did not name it: nothing will ever name it again (err = %v)", carried, err)
			}
		})
	}

	t.Logf("%d of %d calls reached the invariant, %d of them with the failure swallowed",
		checked, len(calls), swallowed)

	// Narrowing to one subtest with -run leaves the rest unrun, so the counts
	// below would fail on top of whatever was being looked at.
	if filteredToSubtests() {
		return
	}
	if checked != len(calls) {
		t.Errorf("%d of %d calls reached the invariant", checked, len(calls))
	}
	// checked rises with len(calls) by construction, so it cannot show that
	// the injected failure still does anything. A run where every call is
	// swallowed is a run where nothing was injected at all -- which is one
	// refactor of the hook away, and would leave all of this green.
	if swallowed*2 >= checked {
		t.Errorf("%d of %d calls swallowed the injected failure; it is no longer reaching "+
			"anything", swallowed, checked)
	}
}

// recreateAfterDelete puts the instance back between rebuild's delete and the
// lookup up makes next, which is what a second idev amounts to. It is the only
// way up reaches its existing-instance branch during a rebuild.
func recreateAfterDelete(client *incustest.Fake) {
	deleted, replaced := false, false
	client.Hook = func(call string) error {
		switch {
		case strings.HasPrefix(call, "delete dev-example-project"):
			deleted = true
		case deleted && !replaced && strings.HasPrefix(call, "instance dev-example-project"):
			replaced = true
			client.AddInstance(&incus.Instance{
				Name:     "dev-example-project",
				Status:   "Running",
				Profiles: []string{"default"},
				Config: map[string]string{
					managedProjectKey: "example-project",
					managedVolumesKey: "default/dev-example-project-cache",
				},
			})
		}
		return nil
	}
}

// rebuildFixture is an instance carrying a volume that has left the
// declaration, which is the case rebuild's carried record exists for.
func rebuildFixture(t *testing.T) (*incustest.Fake, *config.Config) {
	t.Helper()

	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache,default/dev-example-project-old",
		},
	})
	client.Volumes["default/dev-example-project-old"] = true
	return client, cfg
}

// Which stream carries what, pinned where it can be reverted.
//
// The spec says provisioning output goes to stderr and only shell and exec
// relay a container's stdout to stdout. That rule was written down and
// defended by nothing: moving the executor's writers to a.out left the whole
// suite green, and the integration harness reads CombinedOutput, so it cannot
// see the difference either.
func TestProvisioningOutputGoesToStderr(t *testing.T) {
	cfg := mustParse(t, rootYAML+"provision:\n  - run: \"true\"\n")

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		if opt.Stdout != nil {
			_, _ = io.WriteString(opt.Stdout, "STEP-STDOUT\n")
		}
		if opt.Stderr != nil {
			_, _ = io.WriteString(opt.Stderr, "STEP-STDERR\n")
		}
		return 0, nil
	}

	// Every command that runs the steps, because the manual names all three.
	for _, tt := range []struct {
		name string
		run  func(*App) error
	}{
		{"provision", func(a *App) error { return a.Provision(context.Background(), provision.Selection{}) }},
		{"up", func(a *App) error { return a.Up(context.Background(), UpOptions{}) }},
		{"rebuild", func(a *App) error { return a.Rebuild(context.Background()) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out.Reset()
			errOut.Reset()
			a := MustNewApp(AppOptions{
				Config: cfg, Client: client, Runner: &runnertest.Fake{},
				Out: out, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
			})
			if err := tt.run(a); err != nil {
				t.Fatalf("%s error = %v", tt.name, err)
			}

			// The result is the environment, not the log of building it.
			if strings.Contains(out.String(), "STEP-") {
				t.Errorf("stdout = %q, want no provisioning output: a caller captures stdout for results", out)
			}
			for _, want := range []string{"STEP-STDOUT", "STEP-STDERR"} {
				if !strings.Contains(errOut.String(), want) {
					t.Errorf("stderr = %q, want it to carry %s", errOut, want)
				}
			}
		})
	}
}

// exec is the other way round: the command's output is the result.
func TestExecOutputGoesToStdout(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})
	client.ExecFunc = func(_ string, _ []string, opt incus.ExecOptions) (int, error) {
		if opt.Stdout != nil {
			_, _ = io.WriteString(opt.Stdout, "CMD-STDOUT\n")
		}
		if opt.Stderr != nil {
			_, _ = io.WriteString(opt.Stderr, "CMD-STDERR\n")
		}
		return 0, nil
	}

	// shell too: the manual names both, and they are the same path.
	for _, tt := range []struct {
		name string
		run  func(*App) error
	}{
		{"exec", func(a *App) error { return a.Exec(context.Background(), []string{"true"}) }},
		{"shell", func(a *App) error { return a.Shell(context.Background(), []string{"true"}) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out.Reset()
			errOut.Reset()
			a := MustNewApp(AppOptions{
				Config: cfg, Client: client, Runner: &runnertest.Fake{},
				Out: out, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
			})
			if err := tt.run(a); err != nil {
				t.Fatalf("%s error = %v", tt.name, err)
			}

			if !strings.Contains(out.String(), "CMD-STDOUT") {
				t.Errorf("stdout = %q, want the command's own stdout", out)
			}
			if strings.Contains(out.String(), "CMD-STDERR") {
				t.Errorf("stdout = %q, want the command's stderr kept off it", out)
			}
			if !strings.Contains(errOut.String(), "CMD-STDERR") {
				t.Errorf("stderr = %q, want the command's own stderr", errOut)
			}
		})
	}
}

// filteredToSubtests reports whether -run names a subtest, in which case a
// count over all of them says nothing: the rest were never run.
//
// internal/incus/contract asks the same question for the same reason.
func filteredToSubtests() bool {
	f := flag.Lookup("test.run")
	if f == nil {
		return false
	}
	_, subtest, ok := strings.Cut(f.Value.String(), "/")
	return ok && strings.TrimSpace(subtest) != ""
}
