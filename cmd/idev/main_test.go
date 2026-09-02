package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSuccess(t *testing.T) {
	var stderr bytes.Buffer

	if code := run([]string{"--version"}, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0 (%s)", code, stderr.String())
	}
}

func TestRunReportsError(t *testing.T) {
	var stderr bytes.Buffer

	code := run([]string{"validate", "-C", t.TempDir()}, &stderr)

	if code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "[idev] error:") {
		t.Errorf("stderr = %q, want the error to be reported", stderr.String())
	}
}

func TestRunValidatesProject(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".incus-dev", "dev.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "schema: 1\nproject:\n  name: main-test\ninstance:\n  image: images:ubuntu/24.04\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// With no Incus reachable: validate must not need one at all
	// (spec 04-cli.md 4.7).
	t.Setenv("INCUS_SOCKET", filepath.Join(t.TempDir(), "does-not-exist.socket"))

	var stderr bytes.Buffer
	args := []string{"validate", "-C", root}
	if code := run(args, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0 (%s)", code, stderr.String())
	}
}

func TestMainUsesRunExitCode(t *testing.T) {
	original := osExit
	defer func() { osExit = original }()

	var got int
	osExit = func(code int) { got = code }

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"idev", "validate", "-C", t.TempDir()}

	main()

	if got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
}
