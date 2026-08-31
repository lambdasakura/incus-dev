package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus/incustest"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner/runnertest"
)

const rootYAML = `
schema: 1
project:
  name: example-project
instance:
  image: images:ubuntu/24.04
`

// testProject は dev.yml を持つ一時ディレクトリを作る。
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

// fakeApp はfakeで構成したAppと、その裏のIncusクライアントを返す。
func fakeApp(t *testing.T, out *bytes.Buffer) (*App, *incustest.Fake) {
	t.Helper()

	cfg, err := config.Load(filepath.Join(testProject(t, rootYAML), ".incus-dev", "dev.yml"))
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

// execRoot はfakeのAppでルートコマンドを実行する。
func execRoot(t *testing.T, stdin string, args ...string) (*incustest.Fake, string, error) {
	t.Helper()

	out := &bytes.Buffer{}
	app, client := fakeApp(t, out)

	root := newRootCommand("test", func(*globalFlags) (*App, error) { return app, nil })
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
		want string // 呼ばれるべきIncus操作
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
				t.Errorf("calls = %v, %q を含むこと", client.Calls, tt.want)
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
			t.Errorf("output = %q, %q を含むこと", out, want)
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
		t.Fatal("provision = nil error, instanceが無ければ失敗すること")
	}
}

func TestShellCommandPassesArguments(t *testing.T) {
	out := &bytes.Buffer{}
	app, client := fakeApp(t, out)
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{"user.incus-devkit.project": "example-project"},
	})

	root := newRootCommand("test", func(*globalFlags) (*App, error) { return app, nil })
	root.SetArgs([]string{"shell", "--", "make", "test"})
	root.SetOut(out)

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("shell: %v", err)
	}
	if len(client.Execs) == 0 {
		t.Fatal("execされていない")
	}
	if got := strings.Join(client.Execs[0], " "); got != "make test" {
		t.Errorf("argv = %q, want %q", got, "make test")
	}
}

// 破壊操作は既定で確認を求める（仕様 04-cli.md 4.14）
func TestDestructiveCommandsConfirm(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		stdin      string
		wantDelete bool
		wantErr    bool
	}{
		{"destroy 承諾", []string{"destroy"}, "y\n", true, false},
		{"destroy 拒否", []string{"destroy"}, "n\n", false, true},
		{"destroy 空入力", []string{"destroy"}, "\n", false, true},
		{"destroy EOF", []string{"destroy"}, "", false, true},
		{"destroy --force", []string{"destroy", "--force"}, "", true, false},
		{"rebuild 承諾", []string{"rebuild"}, "yes\n", true, false},
		{"rebuild 拒否", []string{"rebuild"}, "no\n", false, true},
		{"rebuild --force", []string{"rebuild", "-f"}, "", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			app, client := fakeApp(t, out)
			client.AddInstance(&incus.Instance{
				Name:   "dev-example-project",
				Status: "Running",
				Config: map[string]string{"user.incus-devkit.project": "example-project"},
			})

			root := newRootCommand("test", func(*globalFlags) (*App, error) { return app, nil })
			root.SetArgs(tt.args)
			root.SetIn(strings.NewReader(tt.stdin))
			root.SetOut(out)
			root.SetErr(out)

			err := root.ExecuteContext(context.Background())

			if tt.wantErr && err == nil {
				t.Error("確認を拒否した場合はエラーになること")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("error = %v", err)
			}
			if got := client.Called("delete"); got != tt.wantDelete {
				t.Errorf("delete実行 = %v, want %v (calls=%v)", got, tt.wantDelete, client.Calls)
			}
		})
	}
}

// App の生成に失敗した場合、コマンドはそのエラーを返す
func TestCommandsPropagateFactoryError(t *testing.T) {
	wantErr := errors.New("factory failed")

	for _, args := range [][]string{
		{"up"}, {"provision"}, {"shell"}, {"status"},
		{"destroy", "--force"}, {"rebuild", "--force"}, {"validate"},
	} {
		t.Run(args[0], func(t *testing.T) {
			root := newRootCommand("test", func(*globalFlags) (*App, error) { return nil, wantErr })
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
			t.Errorf("サブコマンド %q が無い: %v", name, err)
		}
	}
}

func TestExecuteReportsUnknownCommand(t *testing.T) {
	if err := Execute(context.Background(), "test", []string{"no-such-command"}); err == nil {
		t.Error("未知のコマンドはエラーになること")
	}
}

// --- newApp ---

func TestNewAppDiscoversProject(t *testing.T) {
	root := testProject(t, rootYAML)

	app, err := newApp(&globalFlags{directory: root, incusRemote: "local", incusProject: "default"})
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	if got := app.InstanceName(); got != "dev-example-project" {
		t.Errorf("InstanceName() = %q", got)
	}
}

func TestNewAppUsesWorkingDirectoryByDefault(t *testing.T) {
	t.Chdir(testProject(t, rootYAML))

	if _, err := newApp(&globalFlags{}); err != nil {
		t.Errorf("newApp() error = %v", err)
	}
}

func TestNewAppErrors(t *testing.T) {
	t.Run("プロジェクトが無い", func(t *testing.T) {
		if _, err := newApp(&globalFlags{directory: t.TempDir()}); err == nil {
			t.Error("error = nil, want error")
		}
	})

	t.Run("設定が不正", func(t *testing.T) {
		root := testProject(t, "schema: 1\nfeatures: {}\n")
		_, err := newApp(&globalFlags{directory: root})
		if err == nil || !strings.Contains(err.Error(), "features") {
			t.Errorf("error = %v, 設定の問題を報告すること", err)
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
		{"成功", nil, 0, false},
		{"通常のエラー", errors.New("boom"), 1, true},
		{"コンテナ内コマンドの終了コード", &ExitCodeError{Code: 42}, 42, false},
		{"ラップされた終了コード", errors.Join(&ExitCodeError{Code: 3}), 3, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			if got := Report(&buf, tt.err); got != tt.wantCode {
				t.Errorf("Report() = %d, want %d", got, tt.wantCode)
			}
			if got := buf.Len() > 0; got != tt.wantOut {
				t.Errorf("出力の有無 = %v, want %v (%q)", got, tt.wantOut, buf.String())
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
		{"n\n", false},
		{"\n", false},
		{"", false},
		{"maybe\n", false},
	}

	for _, tt := range tests {
		t.Run(strings.TrimSpace(tt.input), func(t *testing.T) {
			var out bytes.Buffer

			if got := confirm(strings.NewReader(tt.input), &out, "続けますか?"); got != tt.want {
				t.Errorf("confirm(%q) = %v, want %v", tt.input, got, tt.want)
			}
			if !strings.Contains(out.String(), "続けますか?") {
				t.Errorf("プロンプトが出力されていない: %q", out.String())
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()

	if isTerminal(r) {
		t.Error("パイプは端末ではない")
	}
}

// グローバルフラグが Incus 操作層まで届くこと
// （マニュアル 03-commands.md が契約として提示している）
func TestNewAppWiresIncusFlags(t *testing.T) {
	root := testProject(t, rootYAML)

	app, err := newApp(&globalFlags{
		directory:    root,
		incusRemote:  "dev-server",
		incusProject: "development",
	})
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}

	client, ok := app.client.(*incus.CLI)
	if !ok {
		t.Fatalf("client = %T, want *incus.CLI", app.client)
	}
	if client.Remote != "dev-server" {
		t.Errorf("Remote = %q, want dev-server", client.Remote)
	}
	if client.Project != "development" {
		t.Errorf("Project = %q, want development", client.Project)
	}

	// ansible inventory へ渡す値も同じであること
	env := app.env()
	if env.Remote != "dev-server" || env.IncusProject != "development" {
		t.Errorf("env = %+v, remote/project が一致しないこと", env)
	}
}

// provision の部分実行フラグ（仕様 04-cli.md 4.2）
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
		want []string // 実行されるべきステップ
		skip []string // 実行されてはいけないステップ
	}{
		{"既定は全部", []string{"provision"}, []string{"echo 1", "echo 2", "echo 3"}, nil},
		{"--step", []string{"provision", "--step", "second"}, []string{"echo 2"}, []string{"echo 1", "echo 3"}},
		{"--step 複数", []string{"provision", "--step", "first", "--step", "3"}, []string{"echo 1", "echo 3"}, []string{"echo 2"}},
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

			root := newRootCommand("test", func(*globalFlags) (*App, error) { return app, nil })
			root.SetArgs(tt.args)
			root.SetOut(out)
			root.SetErr(out)

			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute %v: %v", tt.args, err)
			}

			executed := strings.Join(client.Calls, "\n")
			for _, want := range tt.want {
				if !strings.Contains(executed, want) {
					t.Errorf("%q が実行されていない: %v", want, client.Calls)
				}
			}
			for _, skip := range tt.skip {
				if strings.Contains(executed, skip) {
					t.Errorf("%q を実行している: %v", skip, client.Calls)
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

	cmd := newRootCommand("test", func(*globalFlags) (*App, error) { return app, nil })
	cmd.SetArgs([]string{"provision", "--list"})
	cmd.SetOut(out)

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("provision --list: %v", err)
	}
	for _, want := range []string{"1", "named step", "run", "2", "ansible"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, %q を含むこと", out.String(), want)
		}
	}
	// Incusへは触れない
	if len(client.Calls) != 0 {
		t.Errorf("calls = %v, --list はIncusへ触れないこと", client.Calls)
	}
}

// 排他のフラグを同時に指定した場合はエラーになる
func TestProvisionFlagsAreMutuallyExclusive(t *testing.T) {
	for _, args := range [][]string{
		{"provision", "--step", "a", "--from", "b"},
		{"provision", "--step", "a", "--list"},
		{"provision", "--from", "a", "--list"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			root := newRootCommand("test", func(*globalFlags) (*App, error) {
				t.Fatal("フラグの検査より前にAppを生成している")
				return nil, nil
			})
			root.SetArgs(args)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			if err := root.ExecuteContext(context.Background()); err == nil {
				t.Error("error = nil, want error")
			}
		})
	}
}
