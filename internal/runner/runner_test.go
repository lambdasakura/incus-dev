package runner_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lambdasakura/incus-dev/internal/runner"
)

func TestRunCapturesOutput(t *testing.T) {
	r := runner.New()

	res, err := r.Run(context.Background(), runner.Command{
		Name: "sh",
		Args: []string{"-c", "echo out; echo err >&2"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "out" {
		t.Errorf("Stdout = %q, want %q", got, "out")
	}
	if got := strings.TrimSpace(string(res.Stderr)); got != "err" {
		t.Errorf("Stderr = %q, want %q", got, "err")
	}
}

func TestRunNonZeroExitReturnsExitError(t *testing.T) {
	r := runner.New()

	_, err := r.Run(context.Background(), runner.Command{
		Label: "provision step 2",
		Name:  "sh",
		Args:  []string{"-c", "echo boom >&2; exit 3"},
	})
	if err == nil {
		t.Fatal("Run() = nil error, want error")
	}

	var exitErr *runner.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %T, want *runner.ExitError", err)
	}
	if exitErr.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", exitErr.ExitCode)
	}
	// 仕様 04-cli.md 4.10: 操作・コマンド・exit code・エラー内容を含める
	for _, want := range []string{"provision step 2", "sh", "3", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, %q を含むこと", err.Error(), want)
		}
	}
}

func TestRunMissingBinary(t *testing.T) {
	r := runner.New()

	_, err := r.Run(context.Background(), runner.Command{
		Name: "no-such-command-idev-test",
	})
	if err == nil {
		t.Fatal("Run() = nil error, want error")
	}
	if !strings.Contains(err.Error(), "no-such-command-idev-test") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestRunInDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := runner.New()

	res, err := r.Run(context.Background(), runner.Command{
		Name: "ls",
		Dir:  dir,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(string(res.Stdout), "marker") {
		t.Errorf("Stdout = %q, Dir が反映されていない", res.Stdout)
	}
}

func TestRunWithEnv(t *testing.T) {
	r := runner.New()

	res, err := r.Run(context.Background(), runner.Command{
		Name: "sh",
		Args: []string{"-c", "printf %s \"$IDEV_TEST_VAR\""},
		Env:  []string{"IDEV_TEST_VAR=hello"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(res.Stdout) != "hello" {
		t.Errorf("Stdout = %q, want %q", res.Stdout, "hello")
	}
}

// 環境変数の値はSecretを含みうるためエラーへ出さない（仕様 04-cli.md 4.10）
func TestErrorDoesNotLeakEnvValues(t *testing.T) {
	r := runner.New()

	_, err := r.Run(context.Background(), runner.Command{
		Name: "sh",
		Args: []string{"-c", "exit 1"},
		Env:  []string{"SECRET_TOKEN=supersecret"},
	})
	if err == nil {
		t.Fatal("Run() = nil error, want error")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Errorf("error = %q, 環境変数の値を含めないこと", err.Error())
	}
}

func TestRunStreamsToWriters(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := runner.New()

	_, err := r.Run(context.Background(), runner.Command{
		Name:   "sh",
		Args:   []string{"-c", "echo streamed; echo streamederr >&2"},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "streamed") {
		t.Errorf("stdout writer = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "streamederr") {
		t.Errorf("stderr writer = %q", stderr.String())
	}
}

func TestRunRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	r := runner.New()
	start := time.Now()
	_, err := r.Run(ctx, runner.Command{Name: "sleep", Args: []string{"10"}})
	if err == nil {
		t.Fatal("Run() = nil error, want error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("elapsed = %v, contextで中断されていない", elapsed)
	}
}

func TestCommandString(t *testing.T) {
	c := runner.Command{Name: "ansible-playbook", Args: []string{"-i", "inv", "--", "sh", "-c", "echo hi"}}
	got := c.String()
	want := `ansible-playbook -i inv -- sh -c "echo hi"`
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestRunLogsCommandWhenLoggerSet(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	r := runner.NewWithLogger(logger)
	if _, err := r.Run(context.Background(), runner.Command{Name: "true"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(buf.String(), "true") {
		t.Errorf("log = %q, 実行したコマンドを記録すること", buf.String())
	}
}

// 起動そのものに失敗した場合は ExitError ではなくラップされたエラーを返す
func TestRunStartFailureIsNotExitError(t *testing.T) {
	r := runner.New()

	_, err := r.Run(context.Background(), runner.Command{
		Label: "operation",
		Name:  "no-such-command-idev-test",
	})
	if err == nil {
		t.Fatal("Run() = nil error, want error")
	}

	var exitErr *runner.ExitError
	if errors.As(err, &exitErr) {
		t.Error("起動失敗を ExitError として扱わないこと")
	}
	if !strings.Contains(err.Error(), "operation") {
		t.Errorf("error = %q, 操作名を含むこと", err.Error())
	}
}

func TestExitErrorWithoutLabel(t *testing.T) {
	err := &runner.ExitError{Cmd: "ansible-playbook site.yml", ExitCode: 1}

	got := err.Error()
	if !strings.Contains(got, "ansible-playbook site.yml") || !strings.Contains(got, "exit code 1") {
		t.Errorf("Error() = %q", got)
	}
}

func TestCommandStringWithoutArgs(t *testing.T) {
	if got := (runner.Command{Name: "ansible-playbook"}).String(); got != "ansible-playbook" {
		t.Errorf("String() = %q, want %q", got, "ansible-playbook")
	}
}

// Secretを含みうる引数は表示用文字列でマスクする（仕様 04-cli.md 4.10）
func TestCommandStringRedactsMarkedArgs(t *testing.T) {
	c := runner.Command{
		Name:   "ansible-playbook",
		Args:   []string{"site.yml", "-e", "token=s3cret", "-e", "mode=debug"},
		Redact: []int{2, 4},
	}

	got := c.String()

	if strings.Contains(got, "s3cret") || strings.Contains(got, "debug") {
		t.Errorf("String() = %q, 値を含めないこと", got)
	}
	for _, want := range []string{"token=***", "mode=***", "ansible-playbook site.yml"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, %q を含むこと", got, want)
		}
	}
}

func TestCommandStringRedactsValueWithoutKey(t *testing.T) {
	c := runner.Command{Name: "tool", Args: []string{"--password", "hunter2"}, Redact: []int{1}}

	if got := c.String(); strings.Contains(got, "hunter2") {
		t.Errorf("String() = %q", got)
	}
}

// マスクは表示用のみで、実行される引数は変わらない
func TestRedactDoesNotChangeExecutedArgs(t *testing.T) {
	r := runner.New()

	res, err := r.Run(context.Background(), runner.Command{
		Name:   "sh",
		Args:   []string{"-c", "printf %s \"$TOKEN\""},
		Env:    []string{"TOKEN=s3cret"},
		Redact: []int{1},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(res.Stdout) != "s3cret" {
		t.Errorf("Stdout = %q, 実行時の値は変えないこと", res.Stdout)
	}
}

// 失敗時のエラーメッセージにもマスクが効くこと
func TestExitErrorUsesRedactedCommand(t *testing.T) {
	r := runner.New()

	_, err := r.Run(context.Background(), runner.Command{
		Name:   "sh",
		Args:   []string{"-c", "exit 1", "SECRET=s3cret"},
		Redact: []int{2},
	})
	if err == nil {
		t.Fatal("Run() = nil error, want error")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error = %q, Secretを含めないこと", err.Error())
	}
}

// 出力を中継している場合、エラーに同じstderrを重複させない
func TestExitErrorOmitsStderrWhenStreamed(t *testing.T) {
	var stderr bytes.Buffer
	r := runner.New()

	// stderrの内容がコマンド文字列に現れないようにする
	_, err := r.Run(context.Background(), runner.Command{
		Name:   "sh",
		Args:   []string{"-c", "echo $((6 * 7)) >&2; exit 1"},
		Stderr: &stderr,
	})
	if err == nil {
		t.Fatal("Run() = nil error, want error")
	}

	if strings.TrimSpace(stderr.String()) != "42" {
		t.Errorf("中継されていない: %q", stderr.String())
	}
	if strings.Contains(err.Error(), "42") {
		t.Errorf("error = %q, 中継済みの内容を重複させないこと", err.Error())
	}
}

// シグナルで終了した場合、シェルの慣例に合わせた終了コードを返す
func TestExitCodeForSignaledProcess(t *testing.T) {
	r := runner.New()

	res, err := r.Run(context.Background(), runner.Command{
		Name: "sh",
		Args: []string{"-c", "kill -TERM $$"},
	})
	if err == nil {
		t.Fatal("Run() = nil error, want error")
	}
	if res.ExitCode != 128+int(syscall.SIGTERM) {
		t.Errorf("ExitCode = %d, want %d", res.ExitCode, 128+int(syscall.SIGTERM))
	}
}

func TestCollapseMultilineArgs(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{"単一行はそのまま", "echo hi", `"echo hi"`},
		{"複数行は折り畳む", "one\ntwo\nthree", `"one … (+2 lines)"`},
		{"末尾改行は数えない", "one\ntwo\n", `"one … (+1 lines)"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runner.Command{Name: "sh", Args: []string{"-c", tt.arg}}.String()

			if !strings.Contains(got, tt.want) {
				t.Errorf("String() = %q, %q を含むこと", got, tt.want)
			}
		})
	}
}
