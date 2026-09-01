//go:build integration

// Package integration holds the integration tests that run against a real Incus.
//
//	go test -tags integration ./test/integration/...
package integration_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	// idevBin is the path of the binary under test.
	idevBin string
	// testImage is a lightweight image to test against.
	testImage = envOr("IDEV_TEST_IMAGE", "images:alpine/3.21")
	// ansibleImage includes python3, for testing the ansible steps.
	ansibleImage = envOr("IDEV_TEST_ANSIBLE_IMAGE", "images:ubuntu/noble")
	// bootstrapImage is a Debian-family image without python3, for testing the
	// default bootstrap.
	bootstrapImage = envOr("IDEV_TEST_BOOTSTRAP_IMAGE", "images:debian/12")
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// skipReason says why Incus is unavailable. Empty means it is available.
var skipReason string

// requireIncus skips the test when Incus is unavailable. It skips rather than
// passes, so the result shows the test never ran.
func requireIncus(t *testing.T) {
	t.Helper()

	if skipReason != "" {
		t.Skip(skipReason)
	}
}

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("incus"); err != nil {
		skipReason = "the incus command was not found"
	} else if out, err := exec.Command("incus", "info").CombinedOutput(); err != nil {
		skipReason = fmt.Sprintf("cannot reach the Incus daemon: %v\n%s", err, out)
	}
	if skipReason != "" {
		os.Exit(m.Run())
	}

	dir, err := os.MkdirTemp("", "idev-integration-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	idevBin = filepath.Join(dir, "idev")
	build := exec.Command("go", "build", "-o", idevBin, "../../cmd/idev")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "the build failed: %v\n%s", err, out)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// fixture is a project directory for a test.
type fixture struct {
	t        *testing.T
	root     string
	project  string
	instance string
}

// newFixture creates a .incus-dev/dev.yml with a unique project name.
// {{IMAGE}} and {{PROJECT}} in devYAML are substituted.
func newFixture(t *testing.T, devYAML string) *fixture {
	t.Helper()

	requireIncus(t)

	root := t.TempDir()
	project := fmt.Sprintf("idev-it-%d", time.Now().UnixNano()%1e9)
	instance := "dev-" + project

	body := strings.ReplaceAll(devYAML, "{{PROJECT}}", project)
	body = strings.ReplaceAll(body, "{{IMAGE}}", testImage)
	body = strings.ReplaceAll(body, "{{ANSIBLE_IMAGE}}", ansibleImage)

	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), body)
	writeFile(t, filepath.Join(root, "src", "marker.txt"), "hello from host\n")

	f := &fixture{t: t, root: root, project: project, instance: instance}
	t.Cleanup(f.cleanup)
	return f
}

func (f *fixture) cleanup() {
	_ = exec.Command("incus", "delete", "--force", f.instance).Run()
}

// run executes idev and returns its combined output.
func (f *fixture) run(args ...string) (string, error) {
	f.t.Helper()

	cmd := exec.Command(idevBin, args...)
	cmd.Dir = f.root
	cmd.Env = os.Environ() // carry across whatever t.Setenv set

	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mustRun requires the idev run to succeed.
func (f *fixture) mustRun(args ...string) string {
	f.t.Helper()

	out, err := f.run(args...)
	if err != nil {
		f.t.Fatalf("idev %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// mustFail requires the idev run to fail.
func (f *fixture) mustFail(args ...string) string {
	f.t.Helper()

	out, err := f.run(args...)
	if err == nil {
		f.t.Fatalf("idev %s succeeded unexpectedly:\n%s", strings.Join(args, " "), out)
	}
	return out
}

// incusOut returns the standard output of the incus command.
func incusOut(t *testing.T, args ...string) string {
	t.Helper()

	out, err := exec.Command("incus", args...).Output()
	if err != nil {
		t.Fatalf("incus %s failed: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
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

func requireCommand(t *testing.T, name string) {
	t.Helper()

	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("skipping: %s was not found", name)
	}
}

// runIncus executes the incus command and returns its output and error.
func runIncus(args ...string) (string, error) {
	out, err := exec.Command("incus", args...).CombinedOutput()
	return string(out), err
}
