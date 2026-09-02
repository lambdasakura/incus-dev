package runnertest_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/runner"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
)

func TestFakeRecordsCommands(t *testing.T) {
	f := &runnertest.Fake{}

	if got := f.LastCommand(); got != "" {
		t.Errorf("LastCommand() = %q, want empty", got)
	}
	if got := f.LastArgv(); got != "" {
		t.Errorf("LastArgv() = %q, want empty", got)
	}
	if got := f.LastStdin(); got != "" {
		t.Errorf("LastStdin() = %q, want empty", got)
	}

	if _, err := f.Run(context.Background(), runner.Command{Name: "incus", Args: []string{"list"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Run(context.Background(), runner.Command{
		Name:   "incus",
		Args:   []string{"config", "set", "dev-x", "TOKEN=s3cret"},
		Redact: []int{3},
		Stdin:  strings.NewReader("payload"),
	}); err != nil {
		t.Fatal(err)
	}

	if len(f.Calls) != 2 {
		t.Fatalf("Calls = %d, want 2", len(f.Calls))
	}
	if got := f.LastCommand(); !strings.Contains(got, "TOKEN=***") {
		t.Errorf("LastCommand() = %q, want the masked rendering", got)
	}
	if got := f.LastArgv(); !strings.Contains(got, "TOKEN=s3cret") {
		t.Errorf("LastArgv() = %q, want the real arguments", got)
	}
	if got := f.LastStdin(); got != "payload" {
		t.Errorf("LastStdin() = %q", got)
	}
	if got := f.Commands(); len(got) != 2 || !strings.Contains(got[1], "***") {
		t.Errorf("Commands() = %v", got)
	}
	if got := f.Argvs(); len(got) != 2 || !strings.Contains(got[1], "s3cret") {
		t.Errorf("Argvs() = %v", got)
	}
}

func TestFakeStdoutAndError(t *testing.T) {
	wantErr := errors.New("failed")
	f := &runnertest.Fake{
		Stdout: map[string]string{"incus list": "output"},
		Err:    map[string]error{"incus delete": wantErr},
	}

	var buf bytes.Buffer
	res, err := f.Run(context.Background(), runner.Command{
		Name:   "incus",
		Args:   []string{"list"},
		Stdout: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "output" || buf.String() != "output" {
		t.Errorf("Stdout = %q, writer = %q", res.Stdout, buf.String())
	}

	if _, err := f.Run(context.Background(), runner.Command{Name: "incus", Args: []string{"delete"}}); !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}

	// A command that matches nothing is treated as a success.
	if _, err := f.Run(context.Background(), runner.Command{Name: "other"}); err != nil {
		t.Errorf("error = %v, want nil", err)
	}
}

func TestFakeHandlerTakesPrecedence(t *testing.T) {
	f := &runnertest.Fake{
		Stdout: map[string]string{"incus": "ignored"},
		Handler: func(runner.Command) (runner.Result, error) {
			return runner.Result{Stdout: []byte("from handler")}, nil
		},
	}

	res, err := f.Run(context.Background(), runner.Command{Name: "incus"})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "from handler" {
		t.Errorf("Stdout = %q", res.Stdout)
	}
}
