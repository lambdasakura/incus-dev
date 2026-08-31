//go:build integration

package integration_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// minimalYAML はidmapに依存しない最小構成。
// idmap の検証は idmap_test.go で個別に行う。
const minimalYAML = `
schema: 1
workspace:
  idmap: none
project:
  name: {{PROJECT}}
instance:
  image: {{IMAGE}}
  config:
    limits.cpu: "1"
`

func TestValidate(t *testing.T) {
	f := newFixture(t, minimalYAML)

	out := f.mustRun("validate")
	if !strings.Contains(out, "configuration is valid") {
		t.Errorf("validate output = %q", out)
	}
	// Incusへ変更を加えないこと
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Errorf("validate がinstanceを作成した: %q", got)
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	f := newFixture(t, minimalYAML+"\nfeatures:\n  python: {}\n")

	out := f.mustFail("validate")
	if !strings.Contains(out, "features") {
		t.Errorf("validate output = %q, 未知フィールドを報告すること", out)
	}
}

// idev up でコンテナが起動し、workspaceからホストのファイルが見えること
func TestUpCreatesRunningInstanceWithWorkspace(t *testing.T) {
	f := newFixture(t, minimalYAML)

	f.mustRun("up")

	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "ns"); !strings.Contains(got, "RUNNING") {
		t.Fatalf("instance state = %q, want RUNNING", got)
	}

	out := f.mustRun("shell", "--", "cat", "/workspace/src/marker.txt")
	if !strings.Contains(out, "hello from host") {
		t.Errorf("workspace越しにホストのファイルが見えない: %q", out)
	}

	// devkit管理下であることの印
	if got := incusOut(t, "config", "get", f.instance, "user.incus-devkit.project"); got != f.project {
		t.Errorf("user.incus-devkit.project = %q, want %q", got, f.project)
	}
}

func TestStatusReportsRunningInstance(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")

	out := f.mustRun("status")
	for _, want := range []string{f.project, f.instance, "Running", "/workspace"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, %q を含むこと", out, want)
		}
	}

	if js := f.mustRun("status", "--json"); !strings.Contains(js, `"status": "Running"`) {
		t.Errorf("status --json = %q", js)
	}
}

// provisionステップが実行され、連続実行しても成功すること（REQ-005）
func TestProvisionIsRepeatable(t *testing.T) {
	f := newFixture(t, minimalYAML+`
provision:
  - name: create marker
    run: |
      [ -f /etc/idev-marker ] || echo created > /etc/idev-marker
  - name: read workspace
    run: cat "$DEVKIT_WORKSPACE/src/marker.txt"
`)
	out := f.mustRun("up")
	if !strings.Contains(out, "hello from host") {
		t.Errorf("provisionステップの出力が見えない: %q", out)
	}

	f.mustRun("provision")
	f.mustRun("provision")

	if got := f.mustRun("shell", "--", "cat", "/etc/idev-marker"); !strings.Contains(got, "created") {
		t.Errorf("marker = %q", got)
	}
}

// 失敗したステップを特定できること
func TestProvisionReportsFailingStep(t *testing.T) {
	f := newFixture(t, minimalYAML+`
provision:
  - name: ok step
    run: "true"
  - name: broken step
    run: exit 3
  - name: never reached
    run: touch /etc/should-not-exist
`)
	out := f.mustFail("up")

	for _, want := range []string{"broken step", "2/3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, %q を含むこと", out, want)
		}
	}
	if _, err := f.run("shell", "--", "test", "-f", "/etc/should-not-exist"); err == nil {
		t.Error("失敗後のステップが実行されている")
	}
}

// idev shell -- cmd は端末以外へ出力しても内容を壊さず、終了コードを伝播する
func TestShellCommandOutputAndExitCode(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")

	out := f.mustRun("shell", "--", "printf", `a\nb\n`)
	if strings.Contains(out, "\r") {
		t.Errorf("output = %q, パイプ経由の出力にCRを混入させないこと", out)
	}
	if out != "a\nb\n" {
		t.Errorf("output = %q, want %q", out, "a\nb\n")
	}

	cmd := exec.Command(idevBin, "shell", "--", "sh", "-c", "exit 42")
	cmd.Dir = f.root
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %v, want exit error", err)
	}
	if exitErr.ExitCode() != 42 {
		t.Errorf("exit code = %d, want 42 (コマンドの終了コードを伝播すること)", exitErr.ExitCode())
	}
}

// idev provision は暗黙的に up へ切り替えない（仕様 04-cli.md 4.2）
func TestProvisionRequiresExistingInstance(t *testing.T) {
	f := newFixture(t, minimalYAML)

	out := f.mustFail("provision")
	if !strings.Contains(out, "idev up") {
		t.Errorf("output = %q, idev up の案内を含むこと", out)
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Errorf("provision がinstanceを作成した: %q", got)
	}
}

// 既存instanceを破壊せずに dev.yml の変更を反映すること
func TestUpReappliesConfigWithoutRecreating(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")
	f.mustRun("shell", "--", "sh", "-c", "echo keep > /etc/idev-state")

	cfgPath := filepath.Join(f.root, ".incus-dev", "dev.yml")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfgPath, strings.Replace(string(body), `limits.cpu: "1"`, `limits.cpu: "2"`, 1))

	f.mustRun("up")

	if got := incusOut(t, "config", "get", f.instance, "limits.cpu"); got != "2" {
		t.Errorf("limits.cpu = %q, want 2", got)
	}
	if got := f.mustRun("shell", "--", "cat", "/etc/idev-state"); !strings.Contains(got, "keep") {
		t.Errorf("instanceが作り直されている: %q", got)
	}
}

// rebuild はinstance内の状態を破棄する
func TestRebuildDiscardsInstanceState(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")
	f.mustRun("shell", "--", "sh", "-c", "echo old > /etc/idev-state")

	f.mustRun("rebuild", "--force")

	if _, err := f.run("shell", "--", "test", "-f", "/etc/idev-state"); err == nil {
		t.Error("rebuild後もinstance内の状態が残っている")
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "ns"); !strings.Contains(got, "RUNNING") {
		t.Errorf("rebuild後のstate = %q, want RUNNING", got)
	}
}

// destroy はinstanceのみ削除し、ホスト側のソースには触れない
func TestDestroyKeepsHostFiles(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")

	f.mustRun("destroy", "--force")

	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Errorf("instanceが削除されていない: %q", got)
	}
	for _, path := range []string{
		filepath.Join(f.root, "src", "marker.txt"),
		filepath.Join(f.root, ".incus-dev", "dev.yml"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s が削除されている: %v", path, err)
		}
	}
}

// devkit管理外のinstanceには触れない（仕様 05-incus.md 5.2）
func TestRefusesUnmanagedInstance(t *testing.T) {
	f := newFixture(t, minimalYAML)

	if out, err := runIncus("create", testImage, f.instance); err != nil {
		t.Fatalf("準備に失敗: %v\n%s", err, out)
	}

	out := f.mustFail("up")
	if !strings.Contains(out, "not managed by devkit") {
		t.Errorf("output = %q, 管理外である旨を報告すること", out)
	}
	if out := f.mustFail("destroy", "--force"); !strings.Contains(out, "not managed by devkit") {
		t.Errorf("destroy output = %q", out)
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got == "" {
		t.Error("管理外instanceを削除している")
	}
}

// 存在しないProfileを指定した場合は明示的に失敗する（仕様 05-incus.md 5.3）
func TestFailsWhenProfileMissing(t *testing.T) {
	f := newFixture(t, minimalYAML+`
  profiles:
    - default
    - idev-nonexistent-profile
`)
	out := f.mustFail("up")

	if !strings.Contains(out, "idev-nonexistent-profile") {
		t.Errorf("output = %q, 不足しているProfile名を含むこと", out)
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Errorf("Profile確認前にinstanceを作成している: %q", got)
	}
}

// devkitはProfileを同梱しない。config/devicesだけで環境を宣言できること（REQ-007）
func TestWorksWithoutAnyProfile(t *testing.T) {
	f := newFixture(t, `
schema: 1
project:
  name: {{PROJECT}}
instance:
  image: {{IMAGE}}
  profiles: []
  config:
    limits.cpu: "1"
  devices:
    root:
      type: disk
      pool: default
      path: /
    eth0:
      type: nic
      network: incusbr0
workspace:
  idmap: none
provision:
  - run: test -f /workspace/src/marker.txt
`)
	f.mustRun("up")

	if got := incusOut(t, "config", "get", f.instance, "limits.cpu"); got != "1" {
		t.Errorf("limits.cpu = %q", got)
	}
}

// 新規作成直後でも、ネットワークを使うステップが成功すること。
//
// instanceが起動してコマンドを実行できるようになった時点では、
// まだIPv4が割り当てられておらず外部へ出られない。ここを待たないと
// パッケージ導入を伴うプロジェクトは初回のupが必ず失敗する。
func TestFirstUpCanUseNetwork(t *testing.T) {
	f := newFixture(t, minimalYAML+`
provision:
  - name: install package
    run: command -v jq >/dev/null 2>&1 || apk add --no-cache jq
`)

	f.mustRun("up")

	if got := f.mustRun("shell", "--", "sh", "-c", "command -v jq"); !strings.Contains(got, "jq") {
		t.Errorf("パッケージが導入されていない: %q", got)
	}
}

// provision の部分実行（仕様 04-cli.md 4.2）
func TestProvisionPartialExecution(t *testing.T) {
	f := newFixture(t, minimalYAML+`
provision:
  - name: first
    run: echo first >> /etc/idev-order
  - name: second
    run: echo second >> /etc/idev-order
  - name: third
    run: echo third >> /etc/idev-order
`)
	f.mustRun("up")
	f.mustRun("shell", "--", "sh", "-c", ": > /etc/idev-order")

	if out := f.mustRun("provision", "--list"); !strings.Contains(out, "second") {
		t.Errorf("--list = %q, ステップ名を示すこと", out)
	}

	f.mustRun("provision", "--step", "second")
	if got := f.mustRun("shell", "--", "cat", "/etc/idev-order"); strings.TrimSpace(got) != "second" {
		t.Errorf("実行結果 = %q, 指定したステップのみ実行すること", got)
	}

	f.mustRun("shell", "--", "sh", "-c", ": > /etc/idev-order")
	f.mustRun("provision", "--from", "2")

	want := "second\nthird"
	if got := f.mustRun("shell", "--", "cat", "/etc/idev-order"); strings.TrimSpace(got) != want {
		t.Errorf("実行結果 = %q, want %q", got, want)
	}

	// 解決できない指定はその場で失敗する
	if out := f.mustFail("provision", "--step", "no-such-step"); !strings.Contains(out, "available steps") {
		t.Errorf("output = %q, 選べるステップを示すこと", out)
	}
}
