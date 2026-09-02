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
	// Spec 04-cli.md 4.10: include the operation, the command, the exit code
	// and the error.
	for _, want := range []string{"provision step 2", "sh", "3", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
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
		t.Errorf("Stdout = %q, want Dir to have taken effect", res.Stdout)
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

// Environment variable values may be secrets, so they stay out of errors
// (spec 04-cli.md 4.10).
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
		t.Errorf("error = %q, want it not to contain the environment value", err.Error())
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
		t.Errorf("elapsed = %v, want the context to have interrupted it", elapsed)
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
		t.Errorf("log = %q, want the command that ran to be recorded", buf.String())
	}
}

// A failure to start at all yields a wrapped error, not an ExitError.
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
		t.Error("want a start failure not to be treated as an ExitError")
	}
	if !strings.Contains(err.Error(), "operation") {
		t.Errorf("error = %q, want it to name the operation", err.Error())
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

// Arguments that may carry a secret are masked in the display string
// (spec 04-cli.md 4.10).
//
// A marked argument is hidden whole. Showing anything of it, such as the part
// before an "=", leaks a secret that happens to contain one — a base64 value
// ending in "=" would be printed in full.
func TestCommandStringRedactsMarkedArgs(t *testing.T) {
	secrets := []string{
		"token=s3cret",
		`{"api_token":"dG9rZW4="}`,
		"-----BEGIN KEY-----\nabc=\n",
		"plain",
	}

	c := runner.Command{
		Name:   "ansible-playbook",
		Args:   append([]string{"site.yml"}, secrets...),
		Redact: []int{1, 2, 3, 4},
	}

	got := c.String()

	for _, secret := range []string{"s3cret", "dG9rZW4", "api_token", "BEGIN KEY", "plain", "token"} {
		if strings.Contains(got, secret) {
			t.Errorf("String() = %q, want no trace of %q", got, secret)
		}
	}
	if !strings.Contains(got, "ansible-playbook site.yml") {
		t.Errorf("String() = %q, want the command itself still readable", got)
	}
}

func TestCommandStringRedactsValueWithoutKey(t *testing.T) {
	c := runner.Command{Name: "tool", Args: []string{"--password", "hunter2"}, Redact: []int{1}}

	if got := c.String(); strings.Contains(got, "hunter2") {
		t.Errorf("String() = %q", got)
	}
}

// Masking is display-only; the arguments executed are unchanged.
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
		t.Errorf("Stdout = %q, want the executed value to be unchanged", res.Stdout)
	}
}

// Masking applies to the error message on failure too.
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
		t.Errorf("error = %q, want it not to contain the secret", err.Error())
	}
}

// When output is streamed, the error does not repeat the same stderr.
func TestExitErrorOmitsStderrWhenStreamed(t *testing.T) {
	var stderr bytes.Buffer
	r := runner.New()

	// Keep the stderr content out of the command string.
	_, err := r.Run(context.Background(), runner.Command{
		Name:   "sh",
		Args:   []string{"-c", "echo $((6 * 7)) >&2; exit 1"},
		Stderr: &stderr,
	})
	if err == nil {
		t.Fatal("Run() = nil error, want error")
	}

	if strings.TrimSpace(stderr.String()) != "42" {
		t.Errorf("not streamed: %q", stderr.String())
	}
	if strings.Contains(err.Error(), "42") {
		t.Errorf("error = %q, want streamed content not to be repeated", err.Error())
	}
}

// A process killed by a signal yields the exit code shells use.
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
		{"single line is left alone", "echo hi", `"echo hi"`},
		{"multiple lines are folded", "one\ntwo\nthree", `"one … (+2 lines)"`},
		{"a trailing newline is not counted", "one\ntwo\n", `"one … (+1 lines)"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runner.Command{Name: "sh", Args: []string{"-c", tt.arg}}.String()

			if !strings.Contains(got, tt.want) {
				t.Errorf("String() = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}
