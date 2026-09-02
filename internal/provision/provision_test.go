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
		Remote:          "local",
		IncusProject:    "default",
	}
}

func newExecutor(f *runnertest.Fake) *provision.Executor {
	return newExecutorWith(f, newIncus())
}

func newExecutorWith(f *runnertest.Fake, client *fakeIncus) *provision.Executor {
	return &provision.Executor{Incus: client, Runner: f}
}

// execCall は run ステップの実行内容。
type execCall struct {
	Argv []string
	Opt  incus.ExecOptions
}

// fakeIncus は run ステップの実行内容を記録するIncusクライアント。
type fakeIncus struct {
	*incustest.Fake
	calls []execCall
	// code はスクリプトに対して返す終了コード。キー "" は全スクリプトに適用する。
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

// last は最後の実行内容を返す。
func (f *fakeIncus) last(t *testing.T) execCall {
	t.Helper()
	if len(f.calls) == 0 {
		t.Fatal("run ステップが実行されていない")
	}
	return f.calls[len(f.calls)-1]
}

// scripts は実行されたスクリプトを順に返す。
func (f *fakeIncus) scripts() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, script(c.Argv))
	}
	return out
}

// script は run ステップのargvからスクリプト部分を取り出す。
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

// --- bootstrapの既定動作（仕様 06-provisioning.md 6.3.2） ---

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
		t.Fatal("既定bootstrapは run ステップであること")
	}
	if !strings.Contains(steps[0].Run.Script, "python3") {
		t.Errorf("script = %q, python3の導入を含むこと", steps[0].Run.Script)
	}
}

func TestBootstrapStepsEmptyWhenNoAnsibleStep(t *testing.T) {
	cfg := parseConfig(t, base+`
provision:
  - run: echo hi
`)
	if steps := provision.BootstrapSteps(cfg); len(steps) != 0 {
		t.Errorf("BootstrapSteps() = %+v, ansibleが無ければ何もしないこと", steps)
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
		t.Errorf("script = %q, 既定を置き換えること", steps[0].Run.Script)
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
		t.Errorf("BootstrapSteps() = %+v, 空リストは無効化を意味すること", steps)
	}
}

// --- run ステップ（仕様 06-provisioning.md 6.4） ---

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
		t.Error("provisionは端末を割り当てないこと")
	}
	if !client.Called("exec dev-example-project") {
		t.Errorf("calls = %v", client.Calls)
	}
}

func TestRunStepInjectsDevkitEnv(t *testing.T) {
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
		"DEVKIT_INSTANCE":         "dev-example-project",
		"DEVKIT_PROJECT_NAME":     "example-project",
		"DEVKIT_WORKSPACE":        "/workspace",
		"DEVKIT_WORKSPACE_SOURCE": "/home/u/src/example",
		"DEVKIT_INCUS_PROJECT":    "default",
		"DEVKIT_INCUS_REMOTE":     "local",
	}
	if diff := cmp.Diff(want, client.last(t).Opt.PublicEnv); diff != "" {
		t.Errorf("環境変数 mismatch (-want +got):\n%s", diff)
	}
}

func TestRunStepEnvOverridesDevkitEnv(t *testing.T) {
	client := newIncus()
	cfg := parseConfig(t, base+`
provision:
  - run: echo hi
    env:
      DEVKIT_WORKSPACE: /elsewhere
      EXTRA: value
`)
	if err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	got := client.last(t)
	if got.Opt.Env["DEVKIT_WORKSPACE"] != "/elsewhere" {
		t.Errorf("env = %v, ステップのenvが優先されること", got.Opt.Env)
	}
	if _, ok := got.Opt.PublicEnv["DEVKIT_WORKSPACE"]; ok {
		t.Errorf("env = %v, 上書きされた値が残っている", got.Opt.PublicEnv)
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
		t.Errorf("cwd = %q, cwdを反映すること", got.Opt.Cwd)
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
		t.Errorf("user = %q, 数値ユーザーはそのまま渡すこと", got)
	}
}

// Incusのexecはユーザー名を解決できないため、su で切り替える
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
		t.Errorf("user = %q, 名前をIncusへ渡さないこと", got.Opt.User)
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
		t.Errorf("実行順が違う (-want +got):\n%s", diff)
	}
}

// 失敗したステップを特定できること（仕様 04-cli.md 4.10）
// 失敗したスクリプトが分かること。名前を付けていないステップでは
// 番号だけでは何が落ちたのか分からない（仕様 04-cli.md 4.10）
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
		t.Errorf("error = %q, 失敗したスクリプトを示すこと", msg)
	}
	if strings.Contains(msg, "third-line") {
		t.Errorf("error = %q, 全文は載せないこと", msg)
	}
	if !strings.Contains(msg, "+2 lines") {
		t.Errorf("error = %q, 省略した行数を示すこと", msg)
	}
}

// エラーへ環境変数の値を混ぜないこと（Secretを含みうる）
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
			t.Errorf("error = %q, 秘密情報を含めないこと", err.Error())
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
			t.Errorf("error = %q, %q を含むこと", err.Error(), want)
		}
	}
	if diff := cmp.Diff([]string{"ok", "failing"}, client.scripts()); diff != "" {
		t.Errorf("失敗後のステップを実行しないこと (-want +got):\n%s", diff)
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
		t.Errorf("スクリプト mismatch (-want +got):\n%s", diff)
	}
}

// --- ansible ステップ（仕様 06-provisioning.md 6.5） ---

type ansibleCall struct {
	cmd       runner.Command
	inventory string
	vars      map[string]any
}

// captureAnsible は ansible-playbook 実行時に一時ファイルの内容を記録する。
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
		t.Errorf("Dir = %q, want %q (project rootで実行すること)", call.cmd.Dir, root)
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

	// 仕様 6.5.2: ホスト名 dev、community.general.incus 接続
	for _, want := range []string{
		"dev:",
		"ansible_host: dev-example-project",
		"ansible_connection: community.general.incus",
		"ansible_incus_remote: local",
		"ansible_incus_project: default",
		"devkit:",
	} {
		if !strings.Contains(call.inventory, want) {
			t.Errorf("inventory =\n%s\n%q を含むこと", call.inventory, want)
		}
	}
}

func TestAnsibleDevkitVars(t *testing.T) {
	root, cfg := ansibleProject(t)
	f := &runnertest.Fake{}
	call := captureAnsible(t, f)

	env := testEnv()
	env.ProjectRoot = root
	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	want := map[string]any{
		"devkit_project_name":     "example-project",
		"devkit_instance":         "dev-example-project",
		"devkit_workspace":        "/workspace",
		"devkit_workspace_source": root,
		"devkit_incus_remote":     "local",
		"devkit_incus_project":    "default",
	}
	// workspace_source は env の値を使う
	want["devkit_workspace_source"] = env.WorkspaceSource
	if diff := cmp.Diff(want, call.vars); diff != "" {
		t.Errorf("devkit vars mismatch (-want +got):\n%s", diff)
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
			t.Errorf("args = %q, %q を含むこと", joined, want)
		}
	}
}

// プロジェクトの ansible.cfg があれば ANSIBLE_CONFIG として使う（仕様 6.5.3）
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
		t.Errorf("Env = %v, ANSIBLE_CONFIG を設定すること", call.cmd.Env)
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
		t.Fatal("inventoryが生成されていない")
	}
	if _, err := os.Stat(inventoryPath); !os.IsNotExist(err) {
		t.Errorf("一時inventory %q が残っている", inventoryPath)
	}
}

func TestEnvVars(t *testing.T) {
	want := map[string]string{
		"DEVKIT_PROJECT_NAME":     "example-project",
		"DEVKIT_INSTANCE":         "dev-example-project",
		"DEVKIT_WORKSPACE":        "/workspace",
		"DEVKIT_WORKSPACE_SOURCE": "/home/u/src/example",
		"DEVKIT_INCUS_REMOTE":     "local",
		"DEVKIT_INCUS_PROJECT":    "default",
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

// ステップのenvはSecretを含みうるため、表示しうる値と分けて渡す
// （仕様 04-cli.md 4.10）
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
		t.Errorf("env = %v, 実際の値を渡すこと", got.Opt.Env)
	}
	if _, ok := got.Opt.PublicEnv["API_TOKEN"]; ok {
		t.Errorf("公開env = %v, 利用者指定の値を含めないこと", got.Opt.PublicEnv)
	}
}

// run も ansible も持たないステップは実行時にエラーとする
// （通常はvalidationで弾かれるため、防御的な検査）
func TestRunStepsRejectsEmptyStep(t *testing.T) {
	f := &runnertest.Fake{}

	err := newExecutor(f).RunSteps(context.Background(), []config.Step{{Name: "empty"}}, "provision", testEnv())
	if err == nil {
		t.Fatal("RunSteps() = nil error, want error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, ステップ名を含むこと", err.Error())
	}
}

// コンテナ内でのコマンド実行が失敗した場合
func TestRunStepReportsNonZeroExit(t *testing.T) {
	client := newIncus()
	client.code[""] = 5
	cfg := parseConfig(t, base+"provision:\n  - run: failing\n")

	err := newExecutorWith(&runnertest.Fake{}, client).Provision(
		context.Background(), cfg, testEnv(), provision.Selection{})
	if err == nil || !strings.Contains(err.Error(), "5") {
		t.Errorf("error = %v, 終了コードを報告すること", err)
	}
}

// 既定bootstrapが失敗した場合、bootstrapを明示するよう促すこと
// （仕様 06-provisioning.md 6.3.2、REQ-007例外の成立条件）
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
			t.Errorf("error =\n%v\n%q を含むこと", err, want)
		}
	}
}

// 明示されたbootstrapの失敗には案内を付けない
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
		t.Errorf("error = %v, 明示済みの場合は案内しないこと", err)
	}
}

// ansible ステップの extra_args はSecretを含みうるため表示時に隠す
// （仕様 04-cli.md 4.10）
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
		t.Errorf("表示 = %q, extra_args の値を含めないこと", display)
	}
	if raw := f.LastArgv(); !strings.Contains(raw, "vault_pass=s3cret") {
		t.Errorf("実引数 = %q, 実際の値を渡すこと", raw)
	}
}

// 一部だけ実行しても、ラベルは全体の中での位置を示すこと。
// "step 1/1" では、どのステップを流したのか分からなくなる。
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
		t.Errorf("error = %q, 全体の中での位置を示すこと", err.Error())
	}

	// 選ばれなかったステップは実行しない
	if diff := cmp.Diff([]string{"third"}, client.scripts()); diff != "" {
		t.Errorf("実行したステップ mismatch (-want +got):\n%s", diff)
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

	// 指定順ではなく、宣言順で実行する
	if diff := cmp.Diff([]string{"first", "third"}, client.scripts()); diff != "" {
		t.Errorf("実行順が違う (-want +got):\n%s", diff)
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
		t.Errorf("calls = %v, 解決できない指定では何も実行しないこと", f.Commands())
	}
}

// ansibleステップの前提が揃っていない場合、対処方法を示して止まる
// （仕様 06-provisioning.md 6.5.1）
func TestAnsiblePrerequisiteGuidance(t *testing.T) {
	root, cfg := ansibleProject(t)

	tests := []struct {
		name    string
		failOn  string
		wantAny []string
	}{
		{
			name:    "ansible-playbookが無い",
			failOn:  "ansible-playbook --version",
			wantAny: []string{"ansible-playbook", "install"},
		},
		{
			name:    "community.generalが無い",
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
					t.Errorf("error = %q, %q を含むこと", err.Error(), want)
				}
			}

			// 前提が揃っていないなら playbook は実行しない
			for _, c := range f.Argvs() {
				if strings.HasPrefix(c, "ansible-playbook -i") {
					t.Errorf("前提を満たさないのに実行している: %q", c)
				}
			}
		})
	}
}

// 前提の確認は1度だけ行う
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
		t.Errorf("前提確認 = %d回, want 1", checks)
	}
}

// galaxy ステップはホスト側で ansible-galaxy install を実行する
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
			t.Errorf("command = %q, %q を含むこと", got, want)
		}
	}
	if dir := f.Calls[len(f.Calls)-1].Dir; dir != root {
		t.Errorf("Dir = %q, want %q", dir, root)
	}
}

// galaxy ステップでも ansible の前提を確認する
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

	f := &runnertest.Fake{Err: map[string]error{"ansible-playbook --version": errors.New("not found")}}
	env := testEnv()
	env.ProjectRoot = root

	err = newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{})
	if err == nil || !strings.Contains(err.Error(), "ansible") {
		t.Errorf("error = %v, 前提の不足を報告すること", err)
	}
}

// 秘密情報は ansible ステップへも別ファイルで渡す
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
		t.Fatal("秘密情報のファイルが渡されていない")
	}
	if _, err := os.Stat(secretsFile); !os.IsNotExist(err) {
		t.Errorf("一時ファイル %q が残っている", secretsFile)
	}
}

// 秘密情報が無ければ余計なファイルを渡さない
func TestNoSecretsFileWhenEmpty(t *testing.T) {
	root, cfg := ansibleProject(t)

	f := &runnertest.Fake{}
	env := testEnv()
	env.ProjectRoot = root

	if err := newExecutor(f).Provision(context.Background(), cfg, env, provision.Selection{}); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if strings.Contains(f.LastArgv(), "secrets.json") {
		t.Errorf("args = %q, 秘密情報が無ければ渡さないこと", f.LastArgv())
	}
}

// run ステップの env は秘密情報より優先される
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
