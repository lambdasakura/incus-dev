package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/incus/incustest"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
)

const rootYAML = `
schema: 1
project:
  name: example-project
instance:
  image: images:ubuntu/24.04
`

// errNoIncus stands in for the failure of building an App that connects.
var errNoIncus = errors.New("connect to the local incus")

// stub is an appFactory handing out an App the test prepared.
func stub(app *App) appFactory {
	return func(context.Context, *globalFlags) (*App, error) { return app, nil }
}

// failing is an appFactory that cannot build an App.
func failing(err error) appFactory {
	return func(context.Context, *globalFlags) (*App, error) { return nil, err }
}

// testProject creates a temporary directory holding a dev.yml.
func testProject(t *testing.T, body string) string {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(root, ".incus-dev", "dev.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// fakeApp returns an App built on fakes, and the Incus client behind it.
func fakeApp(t *testing.T, out *bytes.Buffer) (*App, *incustest.Fake) {
	t.Helper()
	return fakeAppWith(t, rootYAML, out)
}

// fakeAppWith is fakeApp for a particular dev.yml.
func fakeAppWith(t *testing.T, yaml string, out *bytes.Buffer) (*App, *incustest.Fake) {
	t.Helper()

	cfg, err := config.Load(filepath.Join(testProject(t, yaml), ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}
	client := incustest.New()
	app := NewApp(AppOptions{
		Config:     cfg,
		Client:     client,
		Runner:     &runnertest.Fake{},
		Out:        out,
		CheckIDMap: func(int, int) error { return nil },
	})
	return app, client
}

// execRoot runs the root command against a fake App.
func execRoot(t *testing.T, stdin string, args ...string) (*incustest.Fake, string, error) {
	t.Helper()

	out := &bytes.Buffer{}
	app, client := fakeApp(t, out)

	root := newRootCommand("test", stub(app), stub(app))
	root.SetArgs(args)
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(out)
	root.SetErr(out)

	err := root.ExecuteContext(context.Background())
	return client, out.String(), err
}

func TestCommandsDispatchToApp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string // the Incus operation that must be called
	}{
		{"up", []string{"up"}, "create"},
		{"status", []string{"status"}, "instance"},
		{"validate", []string{"validate"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _, err := execRoot(t, "", tt.args...)
			if err != nil {
				t.Fatalf("execute %v: %v", tt.args, err)
			}
			if tt.want != "" && !client.Called(tt.want) {
				t.Errorf("calls = %v, want it to contain %q", client.Calls, tt.want)
			}
		})
	}
}

func TestValidateCommandOutput(t *testing.T) {
	_, out, err := execRoot(t, "", "validate")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	for _, want := range []string{"configuration is valid", "example-project", "dev-example-project"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
}

func TestStatusJSONFlag(t *testing.T) {
	_, out, err := execRoot(t, "", "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	if !strings.Contains(out, `"instance": "dev-example-project"`) {
		t.Errorf("output = %q", out)
	}
}

func TestProvisionCommandRequiresInstance(t *testing.T) {
	_, _, err := execRoot(t, "", "provision")
	if err == nil {
		t.Fatal("provision = nil error, want a failure without an instance")
	}
}

func TestShellCommandPassesArguments(t *testing.T) {
	out := &bytes.Buffer{}
	app, client := fakeApp(t, out)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-dev.project": "example-project"},
	})

	root := newRootCommand("test", stub(app), stub(app))
	root.SetArgs([]string{"shell", "--", "make", "test"})
	root.SetOut(out)

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("shell: %v", err)
	}
	if len(client.Execs) == 0 {
		t.Fatal("nothing was executed")
	}
	if got := strings.Join(client.Execs[0], " "); got != "make test" {
		t.Errorf("argv = %q, want %q", got, "make test")
	}
}

// Destructive operations ask for confirmation by default (spec 04-cli.md 4.14).
func TestDestructiveCommandsConfirm(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		stdin      string
		wantDelete bool
		wantErr    bool
	}{
		{"destroy accepted", []string{"destroy"}, "y\n", true, false},
		{"destroy declined", []string{"destroy"}, "n\n", false, true},
		{"destroy with empty input", []string{"destroy"}, "\n", false, true},
		{"destroy EOF", []string{"destroy"}, "", false, true},
		{"destroy --force", []string{"destroy", "--force"}, "", true, false},
		{"rebuild accepted", []string{"rebuild"}, "yes\n", true, false},
		{"rebuild declined", []string{"rebuild"}, "no\n", false, true},
		{"rebuild --force", []string{"rebuild", "-f"}, "", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			app, client := fakeApp(t, out)
			client.AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: map[string]string{"user.incus-dev.project": "example-project"},
			})

			root := newRootCommand("test", stub(app), stub(app))
			root.SetArgs(tt.args)
			root.SetIn(strings.NewReader(tt.stdin))
			root.SetOut(out)
			root.SetErr(out)

			err := root.ExecuteContext(context.Background())

			if tt.wantErr && err == nil {
				t.Error("want an error when the confirmation is declined")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("error = %v", err)
			}
			if got := client.Called("delete"); got != tt.wantDelete {
				t.Errorf("delete ran = %v, want %v (calls=%v)", got, tt.wantDelete, client.Calls)
			}
		})
	}
}

// When building the App fails, the command returns that error.
func TestCommandsPropagateFactoryError(t *testing.T) {
	wantErr := errors.New("factory failed")

	for _, args := range [][]string{
		{"up"}, {"provision"}, {"shell"}, {"status"},
		{"destroy", "--force"}, {"rebuild", "--force"}, {"validate"},
	} {
		t.Run(args[0], func(t *testing.T) {
			root := newRootCommand("test", failing(wantErr), failing(wantErr))
			root.SetArgs(args)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			if err := root.ExecuteContext(context.Background()); !errors.Is(err, wantErr) {
				t.Errorf("error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestRootCommandMetadata(t *testing.T) {
	root := NewRootCommand("1.2.3")

	if root.Use != "idev" {
		t.Errorf("Use = %q", root.Use)
	}
	if root.Version != "1.2.3" {
		t.Errorf("Version = %q", root.Version)
	}
	for _, name := range []string{"up", "provision", "shell", "status", "destroy", "rebuild", "validate"} {
		if _, _, err := root.Find([]string{name}); err != nil {
			t.Errorf("subcommand %q is missing: %v", name, err)
		}
	}
}

func TestExecuteReportsUnknownCommand(t *testing.T) {
	if err := Execute(context.Background(), "test", []string{"no-such-command"}); err == nil {
		t.Error("want an unknown command to be an error")
	}
}

// --- newApp ---

// noIncus points the client at a socket that does not exist, so connecting
// fails whether or not a daemon is running on the machine running the tests.
func noIncus(t *testing.T) {
	t.Helper()
	t.Setenv("INCUS_SOCKET", filepath.Join(t.TempDir(), "does-not-exist.socket"))
}

func TestNewAppDiscoversProject(t *testing.T) {
	root := testProject(t, rootYAML)

	app, err := newOfflineApp(context.Background(), &globalFlags{directory: root, incusProject: "default"})
	if err != nil {
		t.Fatalf("newOfflineApp() error = %v", err)
	}
	if got := app.InstanceName(); got != "dev-example-project" {
		t.Errorf("InstanceName() = %q", got)
	}
}

func TestNewAppUsesWorkingDirectoryByDefault(t *testing.T) {
	t.Chdir(testProject(t, rootYAML))

	if _, err := newOfflineApp(context.Background(), &globalFlags{}); err != nil {
		t.Errorf("newOfflineApp() error = %v", err)
	}
}

// Commands that make no Incus call must not connect. Otherwise `idev validate`
// stops working where no Incus is reachable (spec 04-cli.md 4.7).
func TestNewOfflineAppDoesNotConnect(t *testing.T) {
	root := testProject(t, rootYAML)

	noIncus(t)

	if _, err := newOfflineApp(context.Background(), &globalFlags{directory: root}); err != nil {
		t.Errorf("newOfflineApp() error = %v, want no attempt to connect", err)
	}
}

// The other commands do connect, and report it when they cannot.
func TestNewAppConnects(t *testing.T) {
	root := testProject(t, rootYAML)

	noIncus(t)

	_, err := newApp(context.Background(), &globalFlags{directory: root})
	if err == nil || !strings.Contains(err.Error(), "incus") {
		t.Errorf("error = %v, want the unreachable daemon reported", err)
	}
}

// validate and provision --list work where the instance name cannot be
// derived; the commands that operate on the instance still fail.
//
// project.scope: branch needs git, which a CI job running from a source
// tarball may not have — and the declaration is not what is wrong there
// (spec 04-cli.md 4.7, 4.2).
func TestOfflineAppToleratesAnUnderivableName(t *testing.T) {
	root := testProject(t, `
schema: 1
project:
  name: example-project
  scope: branch
instance:
  image: images:ubuntu/24.04
`)

	app, err := newOfflineApp(context.Background(), &globalFlags{directory: root})
	if err != nil {
		t.Fatalf("newOfflineApp() error = %v", err)
	}
	if err := app.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}

	noIncus(t)
	if _, err := newApp(context.Background(), &globalFlags{directory: root}); err == nil {
		t.Error("newApp() = nil error, want the commands that need the instance to fail")
	}
}

func TestNewAppErrors(t *testing.T) {
	t.Run("no project", func(t *testing.T) {
		if _, err := newOfflineApp(context.Background(), &globalFlags{directory: t.TempDir()}); err == nil {
			t.Error("error = nil, want error")
		}
	})

	t.Run("invalid configuration", func(t *testing.T) {
		root := testProject(t, "schema: 1\nfeatures: {}\n")
		_, err := newOfflineApp(context.Background(), &globalFlags{directory: root})
		if err == nil || !strings.Contains(err.Error(), "features") {
			t.Errorf("error = %v, want the configuration problem reported", err)
		}
	})
}

// --- Report ---

func TestReport(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantOut  bool
	}{
		{"success", nil, 0, false},
		{"an ordinary error", errors.New("boom"), 1, true},
		{"the exit code of a command in the container", &ExitCodeError{Code: 42}, 42, false},
		{"a wrapped exit code", errors.Join(&ExitCodeError{Code: 3}), 3, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			if got := Report(&buf, tt.err); got != tt.wantCode {
				t.Errorf("Report() = %d, want %d", got, tt.wantCode)
			}
			if got := buf.Len() > 0; got != tt.wantOut {
				t.Errorf("produced output = %v, want %v (%q)", got, tt.wantOut, buf.String())
			}
		})
	}
}

// --- confirm ---

func TestConfirm(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{" y \n", true},
		// Without a trailing newline, as a pipe gives: `printf y | idev destroy`
		// must not be read as a refusal.
		{"y", true},
		{"yes", true},
		{"n\n", false},
		{"\n", false},
		{"maybe\n", false},
	}

	for _, tt := range tests {
		t.Run(strings.TrimSpace(tt.input), func(t *testing.T) {
			var out bytes.Buffer

			got, err := confirm(strings.NewReader(tt.input), &out, "Continue?", false)
			if err != nil {
				t.Fatalf("confirm(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("confirm(%q) = %v, want %v", tt.input, got, tt.want)
			}
			if !strings.Contains(out.String(), "Continue?") {
				t.Errorf("the prompt was not printed: %q", out.String())
			}
		})
	}
}

// Nobody there to answer is not the same as an answer of no. Spec 04-cli.md
// 4.14 designs these commands to be driven from CI, where the difference is
// the whole diagnosis: --force exists for exactly this.
func TestConfirmWithNobodyToAsk(t *testing.T) {
	var out bytes.Buffer

	got, err := confirm(strings.NewReader(""), &out, "Continue?", false)
	if got {
		t.Error("confirm on closed input = true, want false")
	}
	if !errors.Is(err, errNoAnswer) {
		t.Fatalf("confirm on closed input error = %v, want errNoAnswer", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want it to name the flag that proceeds without asking", err)
	}

	// A declined confirmation must stay distinguishable from this one.
	if _, err := confirm(strings.NewReader("n\n"), &out, "Continue?", false); err != nil {
		t.Errorf("confirm(\"n\") error = %v, want nil", err)
	}
}

// Ctrl-D at a prompt is a person declining, so the answer must not be advice
// to re-run without the prompt -- that is the thing they just declined.
func TestConfirmAtATerminalReadsEOFAsARefusal(t *testing.T) {
	var out bytes.Buffer

	got, err := confirm(strings.NewReader(""), &out, "Continue?", true)
	if got {
		t.Error("confirm on Ctrl-D = true, want false")
	}
	if err != nil {
		t.Errorf("confirm on Ctrl-D error = %v, want nil: it is a refusal, not a missing answer", err)
	}
}

func TestIsTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()

	if isTerminal(r) {
		t.Error("a pipe is not a terminal")
	}
}

// The global flags reach the Incus layer. The manual, 03-commands.md, presents
// this as a contract.
func TestNewAppWiresIncusFlags(t *testing.T) {
	cfg, err := config.Load(filepath.Join(testProject(t, rootYAML), ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	target := resolveTarget(&globalFlags{incusProject: "development"}, cfg)
	if target.Project != "development" {
		t.Errorf("target = %+v", target)
	}

	// The values handed to the Ansible inventory match.
	app := NewApp(AppOptions{
		Config:       cfg,
		Client:       incustest.New(),
		Runner:       &runnertest.Fake{},
		IncusProject: target.Project,
		CheckIDMap:   func(int, int) error { return nil },
	})

	env, err := app.env()
	if err != nil {
		t.Fatal(err)
	}
	if env.IncusProject != "development" {
		t.Errorf("env = %+v, the project does not match", env)
	}
}

// The flags for a partial provisioning run (spec 04-cli.md 4.2).
func TestProvisionPartialExecutionFlags(t *testing.T) {
	const yaml = rootYAML + `
provision:
  - name: first
    run: echo 1
  - name: second
    run: echo 2
  - name: third
    run: echo 3
`

	tests := []struct {
		name string
		args []string
		want []string // the steps that must run
		skip []string // the steps that must not run
	}{
		{"everything by default", []string{"provision"}, []string{"echo 1", "echo 2", "echo 3"}, nil},
		{"--step", []string{"provision", "--step", "second"}, []string{"echo 2"}, []string{"echo 1", "echo 3"}},
		{"--step repeated", []string{"provision", "--step", "first", "--step", "3"}, []string{"echo 1", "echo 3"}, []string{"echo 2"}},
		{"--from", []string{"provision", "--from", "second"}, []string{"echo 2", "echo 3"}, []string{"echo 1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			cfg, err := config.Load(filepath.Join(testProject(t, yaml), ".incus-dev", "dev.yml"))
			if err != nil {
				t.Fatal(err)
			}
			client := incustest.New().AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: map[string]string{managedProjectKey: "example-project"},
			})
			app := NewApp(AppOptions{
				Config: cfg, Client: client, Runner: &runnertest.Fake{},
				Out: out, CheckIDMap: func(int, int) error { return nil },
			})

			root := newRootCommand("test", stub(app), stub(app))
			root.SetArgs(tt.args)
			root.SetOut(out)
			root.SetErr(out)

			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute %v: %v", tt.args, err)
			}

			executed := strings.Join(client.Calls, "\n")
			for _, want := range tt.want {
				if !strings.Contains(executed, want) {
					t.Errorf("%q never ran: %v", want, client.Calls)
				}
			}
			for _, skip := range tt.skip {
				if strings.Contains(executed, skip) {
					t.Errorf("%q ran: %v", skip, client.Calls)
				}
			}
		})
	}
}

func TestProvisionListFlag(t *testing.T) {
	out := &bytes.Buffer{}

	root := testProject(t, rootYAML+`
provision:
  - name: named step
    run: echo 1
  - ansible:
      playbook: .incus-dev/site.yml
`)
	if err := os.WriteFile(filepath.Join(root, ".incus-dev", "site.yml"), []byte("---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	client := incustest.New()
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, CheckIDMap: func(int, int) error { return nil },
	})

	// The connecting factory must not be reached: --list needs no Incus, and
	// on a machine with no daemon building that App fails (spec 04-cli.md 4.2).
	cmd := newRootCommand("test", failing(errNoIncus), stub(app))
	cmd.SetArgs([]string{"provision", "--list"})
	cmd.SetOut(out)

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("provision --list: %v", err)
	}
	for _, want := range []string{"1", "named step", "run", "2", "ansible"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want it to contain %q", out.String(), want)
		}
	}
	// Incus is not touched.
	if len(client.Calls) != 0 {
		t.Errorf("calls = %v, want --list not to touch Incus", client.Calls)
	}
}

// A flag-shaped argument after shell or exec belongs to the command being run,
// not to idev.
//
// SetInterspersed(false) is what stops cobra from claiming it. Every other test
// writes -- first, which works either way.
func TestShellAndExecDoNotClaimTheCommandsFlags(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"shell", []string{"shell", "bash", "-lc", "make"}, "exec dev-example-project bash -lc make"},
		{"exec", []string{"exec", "ls", "-l"}, "exec dev-example-project ls -l"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			app, client := fakeApp(t, out)
			client.AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: map[string]string{managedProjectKey: "example-project"},
			})

			root := newRootCommand("test", stub(app), stub(app))
			root.SetArgs(tt.args)
			root.SetOut(out)
			root.SetErr(out)

			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute %v: %v", tt.args, err)
			}
			if !client.Called(tt.want) {
				t.Errorf("calls = %v, want it to contain %q", client.Calls, tt.want)
			}
		})
	}
}

// destroy --volumes asks about the data, not only the instance.
//
// Everything else about idev says the instance is the cheap, recreatable
// half; a prompt that mentions only the instance is not the question being
// answered when the volumes go too.
func TestDestroyVolumesPromptNamesTheData(t *testing.T) {
	for _, tt := range []struct {
		name string
		yaml string
		args []string
		want string
	}{
		{"instance only", rootYAML, []string{"destroy"}, "Delete instance dev-example-project?"},
		{
			"with volumes", rootYAML + "volumes:\n  cache:\n    path: /cache\n",
			[]string{"destroy", "--volumes"}, "persistent volumes",
		},
		// Nothing to lose, so the scarier question is not asked: it is the one
		// most likely to make a user answer N to a harmless destroy.
		{"volumes flag with none declared", rootYAML, []string{"destroy", "--volumes"},
			"Delete instance dev-example-project?"},
		// The volume left the declaration but not the pool, and --volumes
		// still deletes it. This is the case the flag exists for, so it is the
		// worst one to ask the mild question about.
		{"dropped from the declaration", rootYAML, []string{"destroy", "--volumes"},
			"persistent volumes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			app, client := fakeAppWith(t, tt.yaml, out)
			config := map[string]string{managedProjectKey: "example-project"}
			switch tt.name {
			case "with volumes":
				// up created it; the prompt asks about data that is there.
				client.Volumes["default/dev-example-project-cache"] = true
			case "dropped from the declaration":
				config[managedVolumesKey] = "default/dev-example-project-cache"
				client.Volumes["default/dev-example-project-cache"] = true
			}
			client.AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: config,
			})

			root := newRootCommand("test", stub(app), stub(app))
			root.SetArgs(tt.args)
			root.SetIn(strings.NewReader("y\n"))
			root.SetOut(out)
			root.SetErr(out)

			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute %v: %v", tt.args, err)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Errorf("prompt = %q, want it to contain %q", out.String(), tt.want)
			}
		})
	}
}

// exec without a command says so before it reaches Incus.
//
// It is the one condition the user can fix from what they typed, so an
// unreachable daemon must not be reported ahead of it (spec 04-cli.md 4.3.1).
func TestExecRequiresACommand(t *testing.T) {
	never := func(context.Context, *globalFlags) (*App, error) {
		t.Fatal("built the App before the arguments were checked")
		return nil, nil
	}
	root := newRootCommand("test", never, never)
	root.SetArgs([]string{"exec"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("error = nil, want the missing command reported")
	}
	// The one command that does what they meant, rather than cobra's generic
	// count of arguments.
	if !strings.Contains(err.Error(), "idev shell") {
		t.Errorf("error = %v, want it to point at idev shell", err)
	}
}

// Combining mutually exclusive flags is an error.
func TestMutuallyExclusiveFlags(t *testing.T) {
	for _, args := range [][]string{
		{"provision", "--step", "a", "--from", "b"},
		{"provision", "--step", "a", "--list"},
		{"provision", "--from", "a", "--list"},
		// --dry-run wins over --restart, so accepting both would silently
		// ignore the restart the user asked for.
		{"up", "--dry-run", "--restart"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			never := func(context.Context, *globalFlags) (*App, error) {
				t.Fatal("built the App before the flags were checked")
				return nil, nil
			}
			root := newRootCommand("test", never, never)
			root.SetArgs(args)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			if err := root.ExecuteContext(context.Background()); err == nil {
				t.Error("error = nil, want error")
			}
		})
	}
}

func TestUpDryRunFlag(t *testing.T) {
	out := &bytes.Buffer{}
	app, client := fakeApp(t, out)

	root := newRootCommand("test", stub(app), stub(app))
	root.SetArgs([]string{"up", "--dry-run"})
	root.SetOut(out)

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("up --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "Create instance") {
		t.Errorf("output = %q", out.String())
	}
	if client.Called("create") {
		t.Errorf("calls = %v, want --dry-run not to create an instance", client.Calls)
	}
}

// The Incus project is decided by the CLI, then dev.yml, then "default".
func TestIncusProjectPrecedence(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		flag string
		want string
	}{
		{"the default", rootYAML, "", "default"},
		{"named in dev.yml", rootYAML + "incus:\n  project: from-config\n", "", "from-config"},
		{"the CLI wins", rootYAML + "incus:\n  project: from-config\n", "from-flag", "from-flag"},
		{"the CLI alone", rootYAML, "from-flag", "from-flag"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.Load(filepath.Join(testProject(t, tt.yaml), ".incus-dev", "dev.yml"))
			if err != nil {
				t.Fatal(err)
			}

			got := resolveTarget(&globalFlags{incusProject: tt.flag}, cfg)
			if got.Project != tt.want {
				t.Errorf("Project = %q, want %q", got.Project, tt.want)
			}
		})
	}
}

// How the exec and snapshot commands are wired up.
func TestExecAndSnapshotCommands(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin string
		want  string // the expected prefix of the Incus operation
	}{
		{"exec", []string{"exec", "--", "make", "test"}, "", "exec dev-example-project make test"},
		// Not s1: the fixture already has one by that name, and Incus refuses
		// a duplicate.
		{"snapshot create", []string{"snapshot", "create", "s2"}, "", "snapshot create dev-example-project s2"},
		{"snapshot create without a name", []string{"snapshot", "create"}, "", "snapshot create dev-example-project"},
		{"snapshot list", []string{"snapshot", "list"}, "", "snapshot list dev-example-project"},
		{"snapshot restore", []string{"snapshot", "restore", "s1", "--force"}, "", "snapshot restore dev-example-project s1"},
		{"snapshot restore with a confirmation", []string{"snapshot", "restore", "s1"}, "y\n", "snapshot restore dev-example-project s1"},
		{"snapshot delete", []string{"snapshot", "delete", "s1", "-f"}, "", "snapshot delete dev-example-project s1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			app, client := fakeApp(t, out)
			client.AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: map[string]string{managedProjectKey: "example-project"},
			})
			client.SnapshotsByInstance["dev-example-project"] = []incus.Snapshot{{Name: "s1"}}

			root := newRootCommand("test", stub(app), stub(app))
			root.SetArgs(tt.args)
			root.SetIn(strings.NewReader(tt.stdin))
			root.SetOut(out)
			root.SetErr(out)

			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute %v: %v", tt.args, err)
			}
			if !client.Called(tt.want) {
				t.Errorf("calls = %v, want it to contain %q", client.Calls, tt.want)
			}
		})
	}
}

// Declining the confirmation runs nothing.
func TestSnapshotDestructiveCommandsConfirm(t *testing.T) {
	for _, args := range [][]string{
		{"snapshot", "restore", "s1"},
		{"snapshot", "delete", "s1"},
	} {
		t.Run(args[1], func(t *testing.T) {
			out := &bytes.Buffer{}
			app, client := fakeApp(t, out)
			client.AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: map[string]string{managedProjectKey: "example-project"},
			})

			root := newRootCommand("test", stub(app), stub(app))
			root.SetArgs(args)
			root.SetIn(strings.NewReader("n\n"))
			root.SetOut(out)
			root.SetErr(out)

			if err := root.ExecuteContext(context.Background()); err == nil {
				t.Error("succeeded despite being declined")
			}
			if client.Called("snapshot " + args[1]) {
				t.Errorf("calls = %v, want nothing run once it is declined", client.Calls)
			}
		})
	}
}

// How destroy --volumes is wired up.
func TestDestroyVolumesFlag(t *testing.T) {
	out := &bytes.Buffer{}
	cfg, err := config.Load(filepath.Join(
		testProject(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n"), ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	client := incustest.New()
	client.Volumes = map[string]bool{"default/dev-example-project-cache": true}
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
	})
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, CheckIDMap: func(int, int) error { return nil },
	})

	root := newRootCommand("test", stub(app), stub(app))
	root.SetArgs([]string{"destroy", "--force", "--volumes"})
	root.SetOut(out)

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("destroy --volumes: %v", err)
	}
	if client.Volumes["default/dev-example-project-cache"] {
		t.Error("--volumes did not delete the volume")
	}
}

// How a failure to build the command propagates.
func TestSnapshotCommandsPropagateFactoryError(t *testing.T) {
	wantErr := errors.New("factory failed")

	for _, args := range [][]string{
		{"exec", "--", "true"},
		{"snapshot", "create"},
		{"snapshot", "list"},
		{"snapshot", "restore", "s", "--force"},
		{"snapshot", "delete", "s", "--force"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newRootCommand("test", failing(wantErr), failing(wantErr))
			root.SetArgs(args)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			if err := root.ExecuteContext(context.Background()); !errors.Is(err, wantErr) {
				t.Errorf("error = %v, want %v", err, wantErr)
			}
		})
	}
}

// The CLI text users see is English, and therefore ASCII.
//
// This tool is published, so usage, flag descriptions and confirmation prompts
// are English by default. The Japanese manual carries the Japanese explanation.
func TestUserFacingTextIsASCII(t *testing.T) {
	nonASCII := regexp.MustCompile(`[^\x00-\x7F]`)

	t.Run("help", func(t *testing.T) {
		var walk func(c *cobra.Command)
		walk = func(c *cobra.Command) {
			for label, text := range map[string]string{
				"Short": c.Short, "Long": c.Long, "Example": c.Example, "Use": c.Use,
			} {
				if found := nonASCII.FindString(text); found != "" {
					t.Errorf("%s: %s contains the non-ASCII %q: %q",
						c.CommandPath(), label, found, text)
				}
			}
			check := func(f *pflag.Flag) {
				if found := nonASCII.FindString(f.Usage); found != "" {
					t.Errorf("%s: the description of --%s contains the non-ASCII %q: %q",
						c.CommandPath(), f.Name, found, f.Usage)
				}
			}
			c.Flags().VisitAll(check)
			c.PersistentFlags().VisitAll(check)
			for _, sub := range c.Commands() {
				walk(sub)
			}
		}
		walk(NewRootCommand("test"))
	})

	t.Run("confirm", func(t *testing.T) {
		for _, args := range [][]string{
			{"destroy"}, {"rebuild"},
			{"snapshot", "restore", "s1"}, {"snapshot", "delete", "s1"},
		} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				out := &bytes.Buffer{}
				app, client := fakeApp(t, out)
				client.AddInstance(&incus.Instance{
					Name:   "dev-example-project",
					Status: "Running",
					Config: map[string]string{managedProjectKey: "example-project"},
				})

				root := newRootCommand("test", stub(app), stub(app))
				root.SetArgs(args)
				root.SetIn(strings.NewReader("n\n"))
				root.SetOut(out)
				root.SetErr(out)
				_ = root.ExecuteContext(context.Background())

				if found := nonASCII.FindString(out.String()); found != "" {
					t.Errorf("the confirmation prompt contains the non-ASCII %q: %q", found, out.String())
				}
			})
		}
	})
}

// warningCount reads the logger idev builds, and nothing else.
func TestWarningCountIgnoresAnotherLogger(t *testing.T) {
	if got := warningCount(slog.New(slog.NewTextHandler(io.Discard, nil))); got != 0 {
		t.Errorf("warningCount() = %d, want 0 for a logger idev did not build", got)
	}
}

// Every command that confirms tells a caller with no stdin the same thing,
// and runs nothing. Declining and having nobody to ask both stop the command;
// only the second one has a way forward to offer.
func TestDestructiveCommandsReportNobodyToAsk(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		call string
	}{
		{"destroy", []string{"destroy"}, "delete "},
		{"rebuild", []string{"rebuild"}, "delete "},
		{"snapshot restore", []string{"snapshot", "restore", "s1"}, "snapshot restore"},
		{"snapshot delete", []string{"snapshot", "delete", "s1"}, "snapshot delete"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			app, client := fakeApp(t, out)
			client.AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: map[string]string{managedProjectKey: "example-project"},
			})

			root := newRootCommand("test", stub(app), stub(app))
			root.SetArgs(tt.args)
			root.SetIn(strings.NewReader(""))
			root.SetOut(out)
			root.SetErr(out)

			err := root.ExecuteContext(context.Background())
			if !errors.Is(err, errNoAnswer) {
				t.Fatalf("%v error = %v, want errNoAnswer", tt.args, err)
			}
			if client.Called(tt.call) {
				t.Errorf("calls = %v, want nothing run when there was no answer", client.Calls)
			}
		})
	}
}
