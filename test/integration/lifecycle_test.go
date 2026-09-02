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

// minimalYAML is the smallest configuration that does not depend on idmap.
// idmap itself is covered separately, in idmap_test.go.
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
	// It changes nothing in Incus.
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Errorf("validate created an instance: %q", got)
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	f := newFixture(t, minimalYAML+"\nfeatures:\n  python: {}\n")

	out := f.mustFail("validate")
	if !strings.Contains(out, "features") {
		t.Errorf("validate output = %q, want the unknown field reported", out)
	}
}

// idev up starts the container, and the host's files are visible through the
// workspace.
func TestUpCreatesRunningInstanceWithWorkspace(t *testing.T) {
	f := newFixture(t, minimalYAML)

	f.mustRun("up")

	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "ns"); !strings.Contains(got, "RUNNING") {
		t.Fatalf("instance state = %q, want RUNNING", got)
	}

	out := f.mustRun("shell", "--", "cat", "/workspace/src/marker.txt")
	if !strings.Contains(out, "hello from host") {
		t.Errorf("the host's file is not visible through the workspace: %q", out)
	}

	// The mark that says idev manages it.
	if got := incusOut(t, "config", "get", f.instance, "user.incus-dev.project"); got != f.project {
		t.Errorf("user.incus-dev.project = %q, want %q", got, f.project)
	}
}

func TestStatusReportsRunningInstance(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")

	out := f.mustRun("status")
	for _, want := range []string{f.project, f.instance, "Running", "/workspace"} {
		if !strings.Contains(out, want) {
			t.Errorf("status = %q, want it to contain %q", out, want)
		}
	}

	if js := f.mustRun("status", "--json"); !strings.Contains(js, `"status": "Running"`) {
		t.Errorf("status --json = %q", js)
	}
}

// The provisioning steps run, and running them again still succeeds (REQ-005).
func TestProvisionIsRepeatable(t *testing.T) {
	f := newFixture(t, minimalYAML+`
provision:
  - name: create marker
    run: |
      [ -f /etc/idev-marker ] || echo created > /etc/idev-marker
  - name: read workspace
    run: cat "$IDEV_WORKSPACE/src/marker.txt"
`)
	out := f.mustRun("up")
	if !strings.Contains(out, "hello from host") {
		t.Errorf("the provisioning step's output is not visible: %q", out)
	}

	f.mustRun("provision")
	f.mustRun("provision")

	if got := f.mustRun("shell", "--", "cat", "/etc/idev-marker"); !strings.Contains(got, "created") {
		t.Errorf("marker = %q", got)
	}
}

// A failed step can be identified.
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
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
	if _, err := f.run("shell", "--", "test", "-f", "/etc/should-not-exist"); err == nil {
		t.Error("a step after the failure ran")
	}
}

// idev shell -- cmd keeps its output intact off a terminal, and passes the exit
// code through.
func TestShellCommandOutputAndExitCode(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")

	out := f.mustRun("shell", "--", "printf", `a\nb\n`)
	if strings.Contains(out, "\r") {
		t.Errorf("output = %q, want no carriage returns in piped output", out)
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
		t.Errorf("exit code = %d, want 42 (the command's exit code must pass through)", exitErr.ExitCode())
	}
}

// idev provision never silently falls back to up (spec 04-cli.md 4.2).
func TestProvisionRequiresExistingInstance(t *testing.T) {
	f := newFixture(t, minimalYAML)

	out := f.mustFail("provision")
	if !strings.Contains(out, "idev up") {
		t.Errorf("output = %q, want it to point at idev up", out)
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Errorf("provision created an instance: %q", got)
	}
}

// A dev.yml change is applied without destroying the existing instance.
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
		t.Errorf("the instance was recreated: %q", got)
	}
}

// rebuild throws away the state inside the instance.
func TestRebuildDiscardsInstanceState(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")
	f.mustRun("shell", "--", "sh", "-c", "echo old > /etc/idev-state")

	f.mustRun("rebuild", "--force")

	if _, err := f.run("shell", "--", "test", "-f", "/etc/idev-state"); err == nil {
		t.Error("state inside the instance survived the rebuild")
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "ns"); !strings.Contains(got, "RUNNING") {
		t.Errorf("state after the rebuild = %q, want RUNNING", got)
	}
}

// destroy deletes the instance alone, leaving the sources on the host.
func TestDestroyKeepsHostFiles(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")

	f.mustRun("destroy", "--force")

	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Errorf("the instance was not deleted: %q", got)
	}
	for _, path := range []string{
		filepath.Join(f.root, "src", "marker.txt"),
		filepath.Join(f.root, ".incus-dev", "dev.yml"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was deleted: %v", path, err)
		}
	}
}

// An instance idev does not manage is left alone (spec 05-incus.md 5.2).
func TestRefusesUnmanagedInstance(t *testing.T) {
	f := newFixture(t, minimalYAML)

	if out, err := runIncus("create", testImage, f.instance); err != nil {
		t.Fatalf("the setup failed: %v\n%s", err, out)
	}

	out := f.mustFail("up")
	if !strings.Contains(out, "not managed by idev") {
		t.Errorf("output = %q, want it to say the instance is unmanaged", out)
	}
	if out := f.mustFail("destroy", "--force"); !strings.Contains(out, "not managed by idev") {
		t.Errorf("destroy output = %q", out)
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got == "" {
		t.Error("deleted an unmanaged instance")
	}
}

// A named profile that does not exist fails explicitly (spec 05-incus.md 5.3).
func TestFailsWhenProfileMissing(t *testing.T) {
	f := newFixture(t, minimalYAML+`
  profiles:
    - default
    - idev-nonexistent-profile
`)
	out := f.mustFail("up")

	if !strings.Contains(out, "idev-nonexistent-profile") {
		t.Errorf("output = %q, want it to name the missing profile", out)
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Errorf("created the instance before checking the profiles: %q", got)
	}
}

// idev ships no profile. config and devices alone can declare an environment
// (REQ-007).
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

// A step that needs the network succeeds even right after creation.
//
// By the time the instance has started and can run commands, no IPv4 address is
// assigned yet and nothing reaches the outside. Without waiting here, any
// project that installs a package fails its very first up.
func TestFirstUpCanUseNetwork(t *testing.T) {
	f := newFixture(t, minimalYAML+`
provision:
  - name: install package
    run: command -v jq >/dev/null 2>&1 || apk add --no-cache jq
`)

	f.mustRun("up")

	if got := f.mustRun("shell", "--", "sh", "-c", "command -v jq"); !strings.Contains(got, "jq") {
		t.Errorf("the package was not installed: %q", got)
	}
}

// A partial provisioning run (spec 04-cli.md 4.2).
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
		t.Errorf("--list = %q, want it to show the step names", out)
	}

	f.mustRun("provision", "--step", "second")
	if got := f.mustRun("shell", "--", "cat", "/etc/idev-order"); strings.TrimSpace(got) != "second" {
		t.Errorf("result = %q, want only the named step run", got)
	}

	f.mustRun("shell", "--", "sh", "-c", ": > /etc/idev-order")
	f.mustRun("provision", "--from", "2")

	want := "second\nthird"
	if got := f.mustRun("shell", "--", "cat", "/etc/idev-order"); strings.TrimSpace(got) != want {
		t.Errorf("result = %q, want %q", got, want)
	}

	// A selection that cannot be resolved fails on the spot.
	if out := f.mustFail("provision", "--step", "no-such-step"); !strings.Contains(out, "available steps") {
		t.Errorf("output = %q, want it to show the steps that can be chosen", out)
	}
}

// --dry-run changes nothing at all in Incus (spec 04-cli.md 4.8).
func TestUpDryRun(t *testing.T) {
	f := newFixture(t, minimalYAML+`
provision:
  - name: setup
    run: "true"
`)

	out := f.mustRun("up", "--dry-run")

	for _, want := range []string{
		"Create instance " + f.instance,
		"Add device workspace",
		"Start instance",
		"Provision step 1/1: setup (run)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output =\n%s\nwant it to contain %q", out, want)
		}
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Fatalf("an instance was created: %q", got)
	}

	// Once it really exists, it is treated as an existing instance.
	f.mustRun("up")
	if out := f.mustRun("up", "--dry-run"); !strings.Contains(out, "Use existing instance") {
		t.Errorf("output =\n%s\nwant it treated as an existing instance", out)
	}
}

// A persistent volume survives a recreated instance (spec 03-configuration.md
// 3.13).
func TestPersistentVolume(t *testing.T) {
	f := newFixture(t, minimalYAML+`
volumes:
  cache:
    path: /cache
    size: 64MiB
`)
	t.Cleanup(func() {
		_, _ = runIncus("storage", "volume", "delete", "default", f.instance+"-cache")
	})

	f.mustRun("up")
	f.mustRun("shell", "--", "sh", "-c", "echo persistent > /cache/data")

	f.mustRun("rebuild", "--force")
	if got := f.mustRun("shell", "--", "cat", "/cache/data"); !strings.Contains(got, "persistent") {
		t.Errorf("rebuild lost the volume's contents: %q", got)
	}

	// destroy keeps it.
	f.mustRun("destroy", "--force")
	if out, _ := runIncus("storage", "volume", "list", "default", "--format", "csv"); !strings.Contains(out, f.instance+"-cache") {
		t.Error("destroy deleted the volume as well")
	}

	// Only --volumes deletes it.
	f.mustRun("up")
	f.mustRun("destroy", "--force", "--volumes")
	if out, _ := runIncus("storage", "volume", "list", "default", "--format", "csv"); strings.Contains(out, f.instance+"-cache") {
		t.Error("--volumes did not delete the volume")
	}
}

// Secrets are injected from the host, and masked when displayed.
func TestSecretInjection(t *testing.T) {
	f := newFixture(t, minimalYAML+`
secrets:
  API_TOKEN:
    env: IDEV_TEST_TOKEN
provision:
  - name: use secret
    run: printf %s "$API_TOKEN" > /etc/idev-secret
`)

	// Unset, it stops before creating an instance.
	out := f.mustFail("up")
	if !strings.Contains(out, "API_TOKEN") || !strings.Contains(out, "IDEV_TEST_TOKEN") {
		t.Errorf("output = %q, want the missing secret reported", out)
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Error("created the instance despite the secret not resolving")
	}

	t.Setenv("IDEV_TEST_TOKEN", "from-host-env")
	f.mustRun("up")

	if got := f.mustRun("shell", "--", "cat", "/etc/idev-secret"); got != "from-host-env" {
		t.Errorf("the value inside the container = %q", got)
	}
}
