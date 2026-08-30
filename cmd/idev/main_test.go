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
		t.Errorf("stderr = %q, エラーを報告すること", stderr.String())
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

	var stderr bytes.Buffer
	if code := run([]string{"validate", "-C", root}, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0 (%s)", code, stderr.String())
	}
}
