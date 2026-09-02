package provision_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/incus/incustest"
	"github.com/lambdasakura/incus-dev/internal/provision"
	"github.com/lambdasakura/incus-dev/internal/runner"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
)

func testEnv() provision.Env {
	return provision.Env{
		ProjectName:     "example-project",
		ProjectRoot:     "/home/u/src/example",
		Instance:        "dev-example-project",
		Workspace:       "/workspace",
		WorkspaceSource: "/home/u/src/example",
		IncusProject:    "default",
	}
}

func newExecutor(f *runnertest.Fake) *provision.Executor {
	return newExecutorWith(f, newIncus())
}

func newExecutorWith(f *runnertest.Fake, client *fakeIncus) *provision.Executor {
	return &provision.Executor{Incus: client, Runner: f}
}

// execCall is what a run step executed.
type execCall struct {
	Argv []string
	Opt  incus.ExecOptions
}

// fakeIncus is an Incus client that records what run steps executed.
type fakeIncus struct {
	*incustest.Fake
	calls []execCall
	// code maps a script to the exit code to return. The key "" applies to
	// every script.
	code map[string]int
}

func newIncus() *fakeIncus {
	f := &fakeIncus{Fake: incustest.New(), code: map[string]int{}}
	f.AddInstance(&incus.Instance{Name: "dev-example-project", Status: "Running"})
	f.ExecFunc = func(_ string, argv []string, opt incus.ExecOptions) (int, error) {
		f.calls = append(f.calls, execCall{Argv: argv, Opt: opt})
		if code, ok := f.code[script(argv)]; ok {
			return code, nil
		}
		return f.code[""], nil
	}
	return f
}

// last returns the most recent execution.
func (f *fakeIncus) last(t *testing.T) execCall {
	t.Helper()
	if len(f.calls) == 0 {
		t.Fatal("no run step was executed")
	}
	return f.calls[len(f.calls)-1]
}

// scripts returns the scripts that ran, in order.
func (f *fakeIncus) scripts() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, script(c.Argv))
	}
	return out
}

// script pulls the script out of a run step's argv.
func script(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return argv[len(argv)-1]
}

func parseConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(yaml), config.Options{})
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	return c
}

const base = `
schema: 1
project:
  name: example-project
instance:
  image: images:ubuntu/24.04
`

// --- The default bootstrap (spec 06-provisioning.md 6.3.2) ---

func TestBootstrapStepsDefaultWhenAnsibleStepExists(t *testing.T) {
	cfg := parseConfig(t, base+`
provision:
  - ansible:
      playbook: p.yml
`)
	steps := provision.BootstrapSteps(cfg)

	if len(steps) != 1 {
		t.Fatalf("BootstrapSteps() = %d steps, want 1", len(steps))
	}
	if steps[0].Run == nil {
		t.Fatal("want the default bootstrap to be a run step")
	}
	if !strings.Contains(steps[0].Run.Script, "python3") {
		t.Errorf("script = %q, want it to install python3", steps[0].Run.Script)
	}
}

func TestBootstrapStepsEmptyWhenNoAnsibleStep(t *testing.T) {
	cfg := parseConfig(t, base+`
provision:
  - run: echo hi
`)
	if steps := provision.BootstrapSteps(cfg); len(steps) != 0 {
		t.Errorf("BootstrapSteps() = %+v, want nothing without an ansible step", steps)
	}
}

func TestBootstrapStepsExplicitOverridesDefault(t *testing.T) {
	cfg := parseConfig(t, base+`
bootstrap:
  - run: dnf install -y python3
provision:
  - ansible:
      playbook: p.yml
`)
	steps := provision.BootstrapSteps(cfg)

	if len(steps) != 1 {
		t.Fatalf("BootstrapSteps() = %d steps, want 1", len(steps))
	}
	if steps[0].Run.Script != "dnf install -y python3" {
		t.Errorf("script = %q, want it to replace the default", steps[0].Run.Script)
	}
}

func TestBootstrapStepsExplicitEmptyDisablesBootstrap(t *testing.T) {
	cfg := parseConfig(t, base+`
bootstrap: []
provision:
  - ansible:
      playbook: p.yml
`)
	if steps := provision.BootstrapSteps(cfg); len(steps) != 0 {
		t.Errorf("BootstrapSteps() = %+v, want an empty list to disable it", steps)
	}
}

// --- run steps (spec 06-provisioning.md 6.4) ---

func TestRunStepExecutesInContainer(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
provision:
  - name: hello
    run: echo hi
`)
	if err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	got := client.last(t)
	if diff := cmp.Diff([]string{"/bin/sh", "-c", "echo hi"}, got.Argv); diff != "" {
		t.Errorf("argv mismatch (-want +got):\n%s", diff)
	}
	if got.Opt.TTY {
		t.Error("want provisioning not to allocate a terminal")
	}
	if !client.Called("exec dev-example-project") {
		t.Errorf("calls = %v", client.Calls)
	}
}

func TestRunStepInjectsIdevEnv(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
provision:
  - run: echo hi
`)
	if err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	want := map[string]string{
		"IDEV_INSTANCE":         "dev-example-project",
		"IDEV_PROJECT_NAME":     "example-project",
		"IDEV_WORKSPACE":        "/workspace",
		"IDEV_WORKSPACE_SOURCE": "/home/u/src/example",
		"IDEV_INCUS_PROJECT":    "default",
	}
	if diff := cmp.Diff(want, client.last(t).Opt.PublicEnv); diff != "" {
		t.Errorf("environment mismatch (-want +got):\n%s", diff)
	}
}

func TestRunStepEnvOverridesIdevEnv(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
provision:
  - run: echo hi
    env:
      IDEV_WORKSPACE: /elsewhere
      EXTRA: value
`)
	if err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	got := client.last(t)
	if got.Opt.Env["IDEV_WORKSPACE"] != "/elsewhere" {
		t.Errorf("env = %v, want the step's env to win", got.Opt.Env)
	}
	if _, ok := got.Opt.PublicEnv["IDEV_WORKSPACE"]; ok {
		t.Errorf("env = %v, the overridden value is still there", got.Opt.PublicEnv)
	}
	if got.Opt.Env["EXTRA"] != "value" {
		t.Errorf("env = %v", got.Opt.Env)
	}
}

func TestRunStepCwdAndShell(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
provision:
  - run: echo hi
    shell: /bin/bash
    cwd: /workspace
`)
	if err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	got := client.last(t)
	if got.Opt.Cwd != "/workspace" {
		t.Errorf("cwd = %q, want cwd to take effect", got.Opt.Cwd)
	}
	if diff := cmp.Diff([]string{"/bin/bash", "-c", "echo hi"}, got.Argv); diff != "" {
		t.Errorf("argv mismatch (-want +got):\n%s", diff)
	}
}

func TestRunStepNumericUser(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
provision:
  - run: echo hi
    user: "1000"
`)
	if err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if got := client.last(t).Opt.User; got != "1000" {
		t.Errorf("user = %q, want a numeric user passed through", got)
	}
}

// The Incus exec API cannot resolve a user name, so su switches to it.
func TestRunStepNamedUser(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
provision:
  - run: echo hi
    user: developer
`)
	if err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	got := client.last(t)
	want := []string{"su", "-s", "/bin/sh", "developer", "-c", "echo hi"}
	if diff := cmp.Diff(want, got.Argv); diff != "" {
		t.Errorf("argv mismatch (-want +got):\n%s", diff)
	}
	if got.Opt.User != "" {
		t.Errorf("user = %q, want the name not passed to Incus", got.Opt.User)
	}
}

func TestStepsRunInOrder(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
provision:
  - run: first
  - run: second
  - run: third
`)
	if err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	if diff := cmp.Diff([]string{"first", "second", "third"}, client.scripts()); diff != "" {
		t.Errorf("wrong order (-want +got):\n%s", diff)
	}
}

// A failed step can be identified, and so can the script that failed. For an
// unnamed step, a number alone leaves no way to tell what broke
// (spec 04-cli.md 4.10).
func TestStepFailureShowsScript(t *testing.T) {
	client := newIncus()
	client.code[""] = 1
	cfg := parseConfig(t, base+`
provision:
  - run: |
      first-line
      second-line
      third-line
`)
	err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{})
	if err == nil {
		t.Fatal("Provision() = nil error, want error")
	}

	msg := err.Error()
	if !strings.Contains(msg, "first-line") {
		t.Errorf("error = %q, want it to show the failing script", msg)
	}
	if strings.Contains(msg, "third-line") {
		t.Errorf("error = %q, want it not to carry the whole script", msg)
	}
	if !strings.Contains(msg, "+2 lines") {
		t.Errorf("error = %q, want it to say how many lines were dropped", msg)
	}
}

// Environment values stay out of errors, since they may be secrets.
func TestStepFailureDoesNotLeakEnv(t *testing.T) {
	client := newIncus()
	client.code[""] = 1
	cfg := parseConfig(t, base+`
provision:
  - run: deploy
    env:
      API_TOKEN: s3cret-value
`)
	env := testEnv()
	env.Secrets = map[string]string{"DEPLOY_KEY": "s3cret-key"}

	err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, env, provision.Selection{})
	if err == nil {
		t.Fatal("Provision() = nil error, want error")
	}
	for _, secret := range []string{"s3cret-value", "s3cret-key"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error = %q, want it not to contain the secret", err.Error())
		}
	}
}

func TestStepFailureIdentifiesStep(t *testing.T) {
	client := newIncus()
	client.code["failing"] = 7

	cfg := parseConfig(t, base+`
provision:
  - run: ok
  - name: broken step
    run: failing
  - run: never
`)
	err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{})
	if err == nil {
		t.Fatal("Provision() = nil error, want error")
	}
	for _, want := range []string{"broken step", "2/3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if diff := cmp.Diff([]string{"ok", "failing"}, client.scripts()); diff != "" {
		t.Errorf("want no step run after the failure (-want +got):\n%s", diff)
	}
}

func TestBootstrapUsesRunSteps(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
bootstrap:
  - run: bootstrap-command
`)
	if err := newExecutorWith(&runnertest.Fake{}, client).Bootstrap(
		context.Background(), cfg, testEnv()); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if diff := cmp.Diff([]string{"bootstrap-command"}, client.scripts()); diff != "" {
		t.Errorf("script mismatch (-want +got):\n%s", diff)
	}
}

// --- ansible steps (spec 06-provisioning.md 6.5) ---

type ansibleCall struct {
	cmd       runner.Command
	inventory string
	vars      map[string]any
}

// captureAnsible records the contents of the temporary files as
// ansible-playbook runs.
func captureAnsible(t *testing.T, f *runnertest.Fake) *ansibleCall {
	t.Helper()
	call := &ansibleCall{}
	f.Handler = func(c runner.Command) (runner.Result, error) {
		if c.Name != "ansible-playbook" {
			return runner.Result{}, nil
		}
		call.cmd = c
		for i, a := range c.Args {
			switch {
			case a == "-i" && i+1 < len(c.Args):
				if b, err := os.ReadFile(c.Args[i+1]); err == nil {
					call.inventory = string(b)
				}
			case strings.HasPrefix(a, "--extra-vars=@") && call.vars == nil:
				path := strings.TrimPrefix(a, "--extra-vars=@")
				b, err := os.ReadFile(path)
				if err != nil {
					t.Errorf("read vars file: %v", err)
					continue
				}
				if err := json.Unmarshal(b, &call.vars); err != nil {
					t.Errorf("parse vars file %q: %v", b, err)
				}
			}
		}
		return runner.Result{}, nil
	}
	return call
}

func ansibleProject(t *testing.T) (root string, cfg *config.Config) {
	t.Helper()
	root = t.TempDir()
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "site.yml"), "---\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), base+`
provision:
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
`)
	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	return root, cfg
}

func TestAnsibleStepCommand(t *testing.T) {
	root, cfg := ansibleProject(t)
	f := &runnertest.Fake{}
	call := captureAnsible(t, f)

	env := testEnv()
	env.ProjectRoot = root
	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	if call.cmd.Name != "ansible-playbook" {
		t.Fatalf("command = %q, want ansible-playbook", call.cmd.Name)
	}
	if call.cmd.Dir != root {
		t.Errorf("Dir = %q, want %q (it must run from the project root)", call.cmd.Dir, root)
	}
	last := call.cmd.Args[len(call.cmd.Args)-1]
	if want := filepath.Join(root, ".incus-dev", "ansible", "site.yml"); last != want {
		t.Errorf("playbook = %q, want %q", last, want)
	}
}

func TestAnsibleInventoryContent(t *testing.T) {
	root, cfg := ansibleProject(t)
	f := &runnertest.Fake{}
	call := captureAnsible(t, f)

	env := testEnv()
	env.ProjectRoot = root
	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	// Spec 6.5.2: host name dev, community.general.incus connection.
	for _, want := range []string{
		"dev:",
		"ansible_host: dev-example-project",
		"ansible_connection: community.general.incus",
		"ansible_incus_remote: local",
		"ansible_incus_project: default",
		"idev:",
	} {
		if !strings.Contains(call.inventory, want) {
			t.Errorf("inventory =\n%s\nwant it to contain %q", call.inventory, want)
		}
	}
}

func TestAnsibleIdevVars(t *testing.T) {
	root, cfg := ansibleProject(t)
	f := &runnertest.Fake{}
	call := captureAnsible(t, f)

	env := testEnv()
	env.ProjectRoot = root
	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	want := map[string]any{
		"idev_project_name":     "example-project",
		"idev_instance":         "dev-example-project",
		"idev_workspace":        "/workspace",
		"idev_workspace_source": root,
		"idev_incus_project":    "default",
	}
	// workspace_source takes its value from env.
	want["idev_workspace_source"] = env.WorkspaceSource
	if diff := cmp.Diff(want, call.vars); diff != "" {
		t.Errorf("idev vars mismatch (-want +got):\n%s", diff)
	}
}

func TestAnsibleOptionalArguments(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "site.yml"), "---\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "vars.yml"), "k: v\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "hosts.yml"), "all:\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), base+`
provision:
  - ansible:
      playbook: .incus-dev/ansible/site.yml
      vars: .incus-dev/ansible/vars.yml
      inventory: .incus-dev/ansible/hosts.yml
      tags: [setup, tools]
      skip_tags: [slow]
      extra_args: ["--diff"]
`)
	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	f := &runnertest.Fake{}
	call := captureAnsible(t, f)
	env := testEnv()
	env.ProjectRoot = root
	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	joined := strings.Join(call.cmd.Args, " ")
	for _, want := range []string{
		"--tags setup,tools",
		"--skip-tags slow",
		"--diff",
		filepath.Join(root, ".incus-dev", "ansible", "hosts.yml"),
		"--extra-vars=@" + filepath.Join(root, ".incus-dev", "ansible", "vars.yml"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args = %q, want it to contain %q", joined, want)
		}
	}
}

// A project ansible.cfg, when present, is used as ANSIBLE_CONFIG (spec 6.5.3).
func TestAnsibleUsesProjectConfig(t *testing.T) {
	root, cfg := ansibleProject(t)
	cfgPath := filepath.Join(root, ".incus-dev", "ansible", "ansible.cfg")
	writeFile(t, cfgPath, "[defaults]\n")

	f := &runnertest.Fake{}
	call := captureAnsible(t, f)
	env := testEnv()
	env.ProjectRoot = root
	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	if !containsString(call.cmd.Env, "ANSIBLE_CONFIG="+cfgPath) {
		t.Errorf("Env = %v, want ANSIBLE_CONFIG set", call.cmd.Env)
	}
}

func TestAnsibleTempFilesAreRemoved(t *testing.T) {
	root, cfg := ansibleProject(t)
	f := &runnertest.Fake{}

	var inventoryPath string
	f.Handler = func(c runner.Command) (runner.Result, error) {
		for i, a := range c.Args {
			if a == "-i" && i+1 < len(c.Args) {
				inventoryPath = c.Args[i+1]
			}
		}
		return runner.Result{}, nil
	}
	env := testEnv()
	env.ProjectRoot = root
	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	if inventoryPath == "" {
		t.Fatal("no inventory was generated")
	}
	if _, err := os.Stat(inventoryPath); !os.IsNotExist(err) {
		t.Errorf("the temporary inventory %q was left behind", inventoryPath)
	}
}

func TestEnvVars(t *testing.T) {
	want := map[string]string{
		"IDEV_PROJECT_NAME":     "example-project",
		"IDEV_INSTANCE":         "dev-example-project",
		"IDEV_WORKSPACE":        "/workspace",
		"IDEV_WORKSPACE_SOURCE": "/home/u/src/example",
		"IDEV_INCUS_PROJECT":    "default",
	}
	if diff := cmp.Diff(want, testEnv().EnvVars()); diff != "" {
		t.Errorf("EnvVars() mismatch (-want +got):\n%s", diff)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// A step's env may hold secrets, so it is passed separately from the values
// that may be displayed (spec 04-cli.md 4.10).
func TestRunStepEnvIsSeparatedFromPublicEnv(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
provision:
  - run: deploy
    env:
      API_TOKEN: s3cret-value
`)
	if err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	got := client.last(t)
	if got.Opt.Env["API_TOKEN"] != "s3cret-value" {
		t.Errorf("env = %v, want the real value passed", got.Opt.Env)
	}
	if _, ok := got.Opt.PublicEnv["API_TOKEN"]; ok {
		t.Errorf("public env = %v, want it not to carry user-supplied values", got.Opt.PublicEnv)
	}
}

// A step with neither run nor ansible is an error at run time. Validation
// normally rejects it, so this is a defensive check.
func TestRunStepsRejectsEmptyStep(t *testing.T) {
	f := &runnertest.Fake{}

	err := newExecutor(f).RunSteps(context.Background(), []config.Step{{Name: "empty"}}, "provision", testEnv())
	if err == nil {
		t.Fatal("RunSteps() = nil error, want error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want it to name the step", err.Error())
	}
}

// When the command inside the container fails.
func TestRunStepReportsNonZeroExit(t *testing.T) {
	client := newIncus()
	client.code[""] = 5
	cfg := parseConfig(t, base+"provision:\n  - run: failing\n")

	err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{})
	if err == nil || !strings.Contains(err.Error(), "5") {
		t.Errorf("error = %v, want the exit code reported", err)
	}
}

// When the default bootstrap fails, the user is told to declare their own
// (spec 06-provisioning.md 6.3.2; the condition on which the REQ-007 exception
// rests).
func TestDefaultBootstrapFailureGuidesUser(t *testing.T) {
	client := newIncus()
	client.code[""] = 127
	cfg := parseConfig(t, base+`
provision:
  - ansible:
      playbook: p.yml
`)

	err := newExecutorWith(&runnertest.Fake{}, client).Bootstrap(context.Background(), cfg, testEnv())
	if err == nil {
		t.Fatal("Bootstrap() = nil error, want error")
	}
	for _, want := range []string{"bootstrap", "dev.yml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error =\n%v\nwant it to contain %q", err, want)
		}
	}
}

// A declared bootstrap's failure carries no such guidance.
func TestExplicitBootstrapFailureHasNoGuidance(t *testing.T) {
	client := newIncus()
	client.code[""] = 1
	cfg := parseConfig(t, base+`
bootstrap:
  - run: dnf install -y python3
`)

	err := newExecutorWith(&runnertest.Fake{}, client).Bootstrap(context.Background(), cfg, testEnv())
	if err == nil {
		t.Fatal("Bootstrap() = nil error, want error")
	}
	if strings.Contains(err.Error(), "dev.yml") {
		t.Errorf("error = %v, want no guidance when it was declared", err)
	}
}

// extra_args on an ansible step may hold secrets, so they are hidden when
// displayed (spec 04-cli.md 4.10).
func TestAnsibleExtraArgsAreRedacted(t *testing.T) {
	root, _ := ansibleProject(t)
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), base+`
provision:
  - ansible:
      playbook: .incus-dev/ansible/site.yml
      extra_args: ["-e", "vault_pass=s3cret"]
`)
	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	f := &runnertest.Fake{}
	env := testEnv()
	env.ProjectRoot = root
	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	if display := f.LastCommand(); strings.Contains(display, "s3cret") {
		t.Errorf("display = %q, want it not to contain the extra_args value", display)
	}
	if raw := f.LastArgv(); !strings.Contains(raw, "vault_pass=s3cret") {
		t.Errorf("argv = %q, want the real value passed", raw)
	}
}

// Even for a partial run, the label shows the position within the whole list.
// "step 1/1" would leave no way to tell which step ran.
func TestSelectedStepKeepsItsPosition(t *testing.T) {
	client := newIncus()
	client.code["third"] = 1
	cfg := parseConfig(t, base+`
provision:
  - run: first
  - run: second
  - name: broken
    run: third
`)

	err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{From: "3"})
	if err == nil {
		t.Fatal("Provision() = nil error, want error")
	}
	if !strings.Contains(err.Error(), "3/3") {
		t.Errorf("error = %q, want the position within the whole list", err.Error())
	}

	// Steps that were not selected do not run.
	if diff := cmp.Diff([]string{"third"}, client.scripts()); diff != "" {
		t.Errorf("executed steps mismatch (-want +got):\n%s", diff)
	}
}

func TestSelectedStepsRunInOrder(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
provision:
  - name: a
    run: first
  - name: b
    run: second
  - name: c
    run: third
`)

	err := newExecutorWith(&runnertest.Fake{}, client).Provision(context.Background(), cfg, testEnv(),
		provision.Selection{Only: []string{"c", "a"}})
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	// They run in declaration order, not in the order given.
	if diff := cmp.Diff([]string{"first", "third"}, client.scripts()); diff != "" {
		t.Errorf("wrong order (-want +got):\n%s", diff)
	}
}

func TestProvisionRejectsUnknownStep(t *testing.T) {
	f := &runnertest.Fake{}
	cfg := parseConfig(t, base+"provision:\n  - name: only-one\n    run: \"true\"\n")

	err := newExecutor(f).Provision(context.Background(), cfg, testEnv(),
		provision.Selection{Only: []string{"nope"}})
	if err == nil {
		t.Fatal("Provision() = nil error, want error")
	}
	if len(f.Calls) != 0 {
		t.Errorf("calls = %v, want nothing run when the selection cannot be resolved", f.Commands())
	}
}

// Without the prerequisites for an ansible step, it stops and says what to do
// (spec 06-provisioning.md 6.5.1).
func TestAnsiblePrerequisiteGuidance(t *testing.T) {
	root, cfg := ansibleProject(t)

	tests := []struct {
		name    string
		failOn  string
		wantAny []string
	}{
		{
			name:    "ansible-playbook is missing",
			failOn:  "ansible-playbook --version",
			wantAny: []string{"ansible-playbook", "install"},
		},
		{
			name:    "community.general is missing",
			failOn:  "ansible-doc",
			wantAny: []string{"community.general", "ansible-galaxy collection install"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &runnertest.Fake{Err: map[string]error{tt.failOn: errors.New("not found")}}

			env := testEnv()
			env.ProjectRoot = root
			err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{})
			if err == nil {
				t.Fatal("Provision() = nil error, want error")
			}
			for _, want := range tt.wantAny {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), want)
				}
			}

			// Without the prerequisites, the playbook does not run.
			for _, c := range f.Argvs() {
				if strings.HasPrefix(c, "ansible-playbook -i") {
					t.Errorf("ran despite the prerequisites being unmet: %q", c)
				}
			}
		})
	}
}

// A failing step names the instance it ran against.
//
// Spec 04-cli.md 4.10 lists Target among the minimum a failure reports, and
// with project.scope: path or branch the instance name is derived rather than
// obvious — a CI log without it cannot say which environment failed.
func TestStepFailureNamesTheInstance(t *testing.T) {
	cfg := parseConfig(t, base+`
provision:
  - name: setup
    run: "false"
`)

	client := newIncus()
	client.code[""] = 1

	err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{})

	if err == nil {
		t.Fatal("Provision() = nil error, want error")
	}
	if !strings.Contains(err.Error(), "dev-example-project") {
		t.Errorf("error = %q, want it to name the instance", err.Error())
	}
}

// A galaxy step is what installs community.general, so requiring it up front
// would make the documented pattern impossible.
//
// The manual pairs a galaxy step with an ansible step precisely so nothing
// outside .incus-dev/ has to be arranged first (manual 5.4).
func TestGalaxyStepIsNotGatedOnWhatItInstalls(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "site.yml"), "---\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "requirements.yml"), "---\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), base+`
provision:
  - name: collections
    galaxy:
      requirements: .incus-dev/ansible/requirements.yml
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
`)
	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	// The plugin is missing until the galaxy step installs it.
	installed := false
	f := &runnertest.Fake{}
	f.Handler = func(c runner.Command) (runner.Result, error) {
		cmd := c.String()
		switch {
		case strings.HasPrefix(cmd, "ansible-galaxy install"):
			installed = true
		case strings.HasPrefix(cmd, "ansible-doc") && !installed:
			return runner.Result{ExitCode: 1}, errors.New("not found")
		}
		return runner.Result{}, nil
	}

	e := newExecutor(f)
	env := testEnv()
	env.ProjectRoot = root

	if err := e.CheckPrerequisites(context.Background(), cfg.Provision); err != nil {
		t.Fatalf("CheckPrerequisites() error = %v, want the galaxy step left to install it", err)
	}
	if err := e.Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if !installed {
		t.Error("the galaxy step never ran")
	}
}

// A galaxy step that runs first still excuses the check when a second one
// follows the ansible step. Deciding on the last galaxy step instead of the
// first would refuse a dev.yml that installs everything it needs.
func TestPluginCheckLooksAtTheFirstGalaxyStep(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "site.yml"), "---\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "requirements.yml"), "---\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), base+`
provision:
  - name: collections
    galaxy:
      requirements: .incus-dev/ansible/requirements.yml
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
  - name: more collections
    galaxy:
      requirements: .incus-dev/ansible/requirements.yml
`)
	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	// The plugin is not there yet; the first galaxy step is what installs it.
	f := &runnertest.Fake{Err: map[string]error{"ansible-doc": errors.New("not found")}}

	if err := newExecutor(f).CheckPrerequisites(context.Background(), cfg.Provision); err != nil {
		t.Fatalf("CheckPrerequisites() error = %v, want the first galaxy step to excuse the check", err)
	}
}

// A galaxy step only excuses the plugin check when it runs first.
//
// Ordered the other way round it cannot install anything in time, so the check
// belongs up front — otherwise the instance is created, started and
// bootstrapped before the ansible step reports the missing plugin.
func TestPluginIsCheckedWhenGalaxyRunsAfterAnsible(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "site.yml"), "---\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "requirements.yml"), "---\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), base+`
provision:
  - name: provision
    ansible:
      playbook: .incus-dev/ansible/site.yml
  - name: collections
    galaxy:
      requirements: .incus-dev/ansible/requirements.yml
`)
	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	f := &runnertest.Fake{Err: map[string]error{"ansible-doc": errors.New("not found")}}

	err = newExecutor(f).CheckPrerequisites(context.Background(), cfg.Provision)
	if err == nil || !strings.Contains(err.Error(), "community.general") {
		t.Errorf("error = %v, want the missing plugin reported up front", err)
	}
}

// A galaxy step needs ansible-galaxy, which is the tool it actually runs.
func TestGalaxyStepChecksAnsibleGalaxy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "requirements.yml"), "---\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), base+`
provision:
  - galaxy:
      requirements: .incus-dev/ansible/requirements.yml
`)
	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	f := &runnertest.Fake{Err: map[string]error{"ansible-galaxy --version": errors.New("not found")}}

	err = newExecutor(f).CheckPrerequisites(context.Background(), cfg.Provision)
	if err == nil || !strings.Contains(err.Error(), "ansible-galaxy") {
		t.Errorf("error = %v, want the missing ansible-galaxy reported", err)
	}
}

// The prerequisites are checked once.
func TestAnsiblePrerequisiteCheckedOnce(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "site.yml"), "---\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), base+`
provision:
  - ansible:
      playbook: .incus-dev/ansible/site.yml
  - ansible:
      playbook: .incus-dev/ansible/site.yml
`)
	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	f := &runnertest.Fake{}
	env := testEnv()
	env.ProjectRoot = root
	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	checks := 0
	for _, c := range f.Argvs() {
		if strings.HasPrefix(c, "ansible-doc") {
			checks++
		}
	}
	if checks != 1 {
		t.Errorf("prerequisite checks = %d, want 1", checks)
	}
}

// A galaxy step runs ansible-galaxy install on the host.
func TestGalaxyStep(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".incus-dev", "ansible", "requirements.yml"), "collections: []\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), base+`
provision:
  - name: collections
    galaxy:
      requirements: .incus-dev/ansible/requirements.yml
      extra_args: ["--force"]
`)
	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	f := &runnertest.Fake{}
	env := testEnv()
	env.ProjectRoot = root
	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	got := f.LastArgv()
	for _, want := range []string{
		"ansible-galaxy install -r",
		filepath.Join(root, ".incus-dev", "ansible", "requirements.yml"),
		"--force",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("command = %q, want it to contain %q", got, want)
		}
	}
	if dir := f.Calls[len(f.Calls)-1].Dir; dir != root {
		t.Errorf("Dir = %q, want %q", dir, root)
	}
}

// A galaxy step checks the Ansible prerequisites too.
func TestGalaxyStepChecksPrerequisites(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".incus-dev", "requirements.yml"), "collections: []\n")
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), base+`
provision:
  - galaxy:
      requirements: .incus-dev/requirements.yml
`)
	cfg, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}

	// A galaxy step runs ansible-galaxy, so that is what it needs.
	f := &runnertest.Fake{Err: map[string]error{"ansible-galaxy --version": errors.New("not found")}}
	env := testEnv()
	env.ProjectRoot = root

	err = newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{})
	if err == nil || !strings.Contains(err.Error(), "ansible-galaxy") {
		t.Errorf("error = %v, want the missing prerequisite reported", err)
	}
}

// Secrets reach an ansible step through a file of their own.
func TestSecretsArePassedToAnsible(t *testing.T) {
	root, cfg := ansibleProject(t)

	f := &runnertest.Fake{}
	var secretsFile string
	f.Handler = func(c runner.Command) (runner.Result, error) {
		if c.Name != "ansible-playbook" {
			return runner.Result{}, nil
		}
		for _, a := range c.Args {
			if strings.HasSuffix(a, "secrets.json") {
				secretsFile = strings.TrimPrefix(a, "--extra-vars=@")
				data, err := os.ReadFile(secretsFile)
				if err != nil {
					t.Errorf("read secrets: %v", err)
					continue
				}
				var got map[string]string
				if err := json.Unmarshal(data, &got); err != nil {
					t.Errorf("parse secrets: %v", err)
				}
				if got["API_TOKEN"] != "s3cret" {
					t.Errorf("secrets = %v", got)
				}

				info, err := os.Stat(secretsFile)
				if err == nil && info.Mode().Perm() != 0o600 {
					t.Errorf("permission = %o, want 600", info.Mode().Perm())
				}
			}
		}
		return runner.Result{}, nil
	}

	env := testEnv()
	env.ProjectRoot = root
	env.Secrets = map[string]string{"API_TOKEN": "s3cret"}

	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if secretsFile == "" {
		t.Fatal("no secrets file was passed")
	}
	if _, err := os.Stat(secretsFile); !os.IsNotExist(err) {
		t.Errorf("the temporary file %q was left behind", secretsFile)
	}
}

// With no secrets, no extra file is passed.
func TestNoSecretsFileWhenEmpty(t *testing.T) {
	root, cfg := ansibleProject(t)

	f := &runnertest.Fake{}
	env := testEnv()
	env.ProjectRoot = root

	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if strings.Contains(f.LastArgv(), "secrets.json") {
		t.Errorf("args = %q, want nothing passed when there are no secrets", f.LastArgv())
	}
}

// A run step's env wins over a secret.
func TestStepEnvOverridesSecret(t *testing.T) {
	f := &runnertest.Fake{}
	cfg := parseConfig(t, base+"provision:\n  - run: deploy\n    env:\n      API_TOKEN: from-step\n")

	env := testEnv()
	env.Secrets = map[string]string{"API_TOKEN": "from-secret"}

	client := newIncus()
	if err := newExecutorWith(f, client).Provision(
		context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if got := client.last(t).Opt.Env["API_TOKEN"]; got != "from-step" {
		t.Errorf("API_TOKEN = %q, want from-step", got)
	}
}
