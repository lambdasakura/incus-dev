//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const idmapYAML = `
schema: 1
project:
  name: {{PROJECT}}
instance:
  image: {{IMAGE}}
`

// workspace.idmap: auto, the default, makes the workspace writable on either
// kind of host. Only who owns what depends on the host configuration.
func TestWorkspaceIDMapAuto(t *testing.T) {
	f := newFixture(t, idmapYAML)

	out := f.mustRun("up")

	if !idmapPermitted(t) {
		// On a host where raw does not work it falls back to shift, and says so.
		if !strings.Contains(out, "shift") {
			t.Errorf("output = %q, want it to say it fell back", out)
		}
		if got := incusOut(t, "config", "get", f.instance, "raw.idmap"); got != "" {
			t.Errorf("raw.idmap = %q, want it unset when it does not work", got)
		}
	}

	f.mustRun("shell", "--", "sh", "-c", "echo written > /workspace/from-container.txt")

	path := filepath.Join(f.root, "from-container.txt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the container could not write to the host: %v", err)
	}

	// Only where raw works does the host-side owner become the invoking user.
	if idmapPermitted(t) {
		if uid := fileUID(info); uid != os.Getuid() {
			t.Errorf("uid = %d, want %d (the invoking host user must own it)", uid, os.Getuid())
		}
	}
}

// Host mounts other than the workspace are readable and writable on the
// defaults too. The idmap strategy is decided by the host, so they have to get
// the same treatment with nothing written in dev.yml.
func TestExtraHostMountIsWritable(t *testing.T) {
	f := newFixture(t, idmapYAML)

	data := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "host.txt"), []byte("from host\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(f.root, ".incus-dev", "dev.yml"),
		strings.ReplaceAll(idmapYAML, "{{PROJECT}}", f.project)+
			strings.ReplaceAll(`  devices:
    extdata:
      type: disk
      source: DATA
      path: /data
`, "DATA", data))
	// Substitute the image in the template too.
	body, err := os.ReadFile(filepath.Join(f.root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.root, ".incus-dev", "dev.yml"),
		strings.ReplaceAll(string(body), "{{IMAGE}}", testImage))

	f.mustRun("up")

	if got := f.mustRun("shell", "--", "cat", "/data/host.txt"); !strings.Contains(got, "from host") {
		t.Errorf("the extra mount cannot be read: %q", got)
	}
	f.mustRun("shell", "--", "sh", "-c", "echo written > /data/from-container.txt")

	if _, err := os.Stat(filepath.Join(data, "from-container.txt")); err != nil {
		t.Errorf("the extra mount could not be written: %v", err)
	}
}

// idmap: shift makes the workspace readable and writable with no extra host
// setup.
func TestWorkspaceIDMapShift(t *testing.T) {
	f := newFixture(t, idmapYAML+"workspace:\n  idmap: shift\n")

	f.mustRun("up")

	if got := incusOut(t, "config", "get", f.instance, "raw.idmap"); got != "" {
		t.Errorf("raw.idmap = %q, want it unset under shift", got)
	}
	if got := f.mustRun("shell", "--", "cat", "/workspace/src/marker.txt"); !strings.Contains(got, "hello from host") {
		t.Errorf("a file on the host cannot be read: %q", got)
	}
	f.mustRun("shell", "--", "sh", "-c", "echo written > /workspace/shift.txt")

	if _, err := os.Stat(filepath.Join(f.root, "shift.txt")); err != nil {
		t.Errorf("the container could not write to the host: %v", err)
	}
}

// Where the host does not permit it, idmap: raw fails before creating an
// instance.
func TestWorkspaceIDMapRawRequiresHostSetup(t *testing.T) {
	if idmapPermitted(t) {
		t.Skip("skipping: this host permits raw.idmap")
	}
	f := newFixture(t, idmapYAML+"workspace:\n  idmap: raw\n")

	out := f.mustFail("up")

	for _, want := range []string{"/etc/subuid", "root:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
		t.Errorf("created the instance before the check: %q", got)
	}
}

// fileUID returns the uid that owns a file.
func fileUID(info os.FileInfo) int {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(st.Uid)
}

// idmapPermitted reports whether root may map the current uid and gid.
func idmapPermitted(t *testing.T) bool {
	t.Helper()

	return subIDAllows(t, "/etc/subuid", os.Getuid()) && subIDAllows(t, "/etc/subgid", os.Getgid())
}

func subIDAllows(t *testing.T, path string, id int) bool {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ":")
		if len(parts) != 3 || parts[0] != "root" {
			continue
		}
		start, err1 := strconv.Atoi(parts[1])
		count, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil {
			continue
		}
		if id >= start && id < start+count {
			return true
		}
	}
	return false
}
