//go:build integration

// Package integration holds the integration tests that run against a real Incus.
//
//	go test -tags integration ./test/integration/...
package integration_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	t       *testing.T
	root    string
	project string
	// instance is derived on first use, not at construction: deriving it
	// asks idev, and some fixtures hold a dev.yml that is deliberately
	// invalid and has no answer to give.
	instance string
}

// newFixture creates a .incus-dev/dev.yml with a unique project name.
// {{IMAGE}} and {{PROJECT}} in devYAML are substituted.
func newFixture(t *testing.T, devYAML string) *fixture {
	t.Helper()

	requireIncus(t)

	root := t.TempDir()
	project := fmt.Sprintf("idev-it-%d", time.Now().UnixNano()%1e9)

	f := &fixture{t: t, root: root, project: project}
	writeFile(t, filepath.Join(root, ".incus-dev", "dev.yml"), render(f, devYAML))
	writeFile(t, filepath.Join(root, "src", "marker.txt"), "hello from host\n")

	t.Cleanup(f.cleanup)
	return f
}

// render substitutes the placeholders in a dev.yml body for this fixture, so
// a test can rewrite the file mid-run the way a user edits it.
func render(f *fixture, devYAML string) string {
	body := strings.ReplaceAll(devYAML, "{{PROJECT}}", f.project)
	body = strings.ReplaceAll(body, "{{IMAGE}}", testImage)
	return strings.ReplaceAll(body, "{{ANSIBLE_IMAGE}}", ansibleImage)
}

func (f *fixture) cleanup() {
	// Derived rather than read: a test may have created the instance without
	// ever asking for its name, and leaving it behind costs the next run.
	if name, ok := f.deriveInstanceName(); ok {
		_ = exec.Command("incus", "delete", "--force", name).Run()
	}
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

// runSplit runs idev with the two streams kept apart.
//
// run merges them, which is right for asserting on a message but cannot say
// which stream carried it. `idev ip` exists to be substituted, so what is on
// stdout and only stdout is the whole point (spec 04-cli.md 4.4.1).
func (f *fixture) runSplit(args ...string) (stdout, stderr string, err error) {
	f.t.Helper()

	cmd := exec.Command(idevBin, args...)
	cmd.Dir = f.root
	cmd.Env = os.Environ()

	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

// newFixtureFromExample runs one of the shipped examples/ directories.
//
// The example itself, not a copy of its shape: what these tests are for is the
// file a user is told to copy, and a paraphrase of it can be right while the
// file is wrong.
func newFixtureFromExample(t *testing.T, name string) *fixture {
	t.Helper()

	requireIncus(t)

	root := t.TempDir()
	src := filepath.Join("..", "..", "examples", name, ".incus-dev")
	if out, err := exec.Command("cp", "-r", src, filepath.Join(root, ".incus-dev")).CombinedOutput(); err != nil {
		t.Fatalf("copy %s: %v\n%s", src, err, out)
	}

	path := filepath.Join(root, ".incus-dev", "dev.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	project := fmt.Sprintf("idev-it-%d", time.Now().UnixNano()%1e9)
	text := regexp.MustCompile(`(?m)^  name: .*$`).ReplaceAllString(string(body), "  name: "+project)

	// The ids the example asks for are the host's wherever the mapping is
	// shift; under raw they are the container's and stay as written.
	if strings.Contains(text, "idmap: shift") {
		text = strings.ReplaceAll(text, `DEV_UID: "1000"`, fmt.Sprintf(`DEV_UID: "%d"`, os.Getuid()))
		text = strings.ReplaceAll(text, `DEV_GID: "1000"`, fmt.Sprintf(`DEV_GID: "%d"`, os.Getgid()))
	}
	writeFile(t, path, text)

	f := &fixture{t: t, root: root, project: project}
	t.Cleanup(f.cleanup)
	return f
}

// instanceName is what idev calls this project's instance.
//
// Asked of idev rather than computed here: project.scope decides it, and a
// test that reimplements the rule agrees with itself rather than with idev.
// status names it without creating anything.
func (f *fixture) instanceName() string {
	f.t.Helper()

	name, ok := f.deriveInstanceName()
	if !ok {
		f.t.Fatalf("idev could not name this project's instance:\n%s",
			f.mustRun("status"))
	}
	return name
}

// deriveInstanceName is instanceName without failing the test, for the cleanup
// that runs even when the fixture's dev.yml never became valid.
func (f *fixture) deriveInstanceName() (string, bool) {
	if f.instance != "" {
		return f.instance, true
	}

	out, err := f.run("status")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(line, "Instance:"); ok {
			f.instance = strings.TrimSpace(name)
			return f.instance, f.instance != ""
		}
	}
	return "", false
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
