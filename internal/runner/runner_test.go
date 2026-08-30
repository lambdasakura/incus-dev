package runner_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	c := runner.Command{Name: "incus", Args: []string{"exec", "dev-x", "--", "sh", "-c", "echo hi"}}
	got := c.String()
	want := `incus exec dev-x -- sh -c "echo hi"`
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
