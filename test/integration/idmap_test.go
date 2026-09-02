//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
		if got := incusOut(t, "config", "get", f.instanceName(), "raw.idmap"); got != "" {
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

	if got := incusOut(t, "config", "get", f.instanceName(), "raw.idmap"); got != "" {
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
	if got := incusOut(t, "list", f.instanceName(), "--format", "csv", "-c", "n"); got != "" {
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

// The two spellings of raw.idmap ask the kernel for the same thing.
//
// idev skips a restart on that basis, and CLAUDE.md lists it among the beliefs
// about Incus that have already caused regressions here. Until now the only
// guard was a unit test of idev's own opinion, which is the shape that
// produces a wrong fake and a matching wrong test.
//
// The daemon settles it: volatile.idmap.current is what it computed. Note that
// the two are not textually equal there either -- "both" yields one entry with
// both flags set, the split form yields two -- so this expands them the same
// way idev does before comparing.
func TestBothAndSplitIDMapAreOneMapping(t *testing.T) {
	requireIncus(t)

	name := fmt.Sprintf("dev-idmap-%d", time.Now().UnixNano()%1e9)
	if out, err := runIncus("launch", testImage, name, "-c", "raw.idmap=both 1000 0"); err != nil {
		t.Skipf("cannot create an instance with raw.idmap here: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = runIncus("delete", "-f", name) })

	computed := func() []idmapEntry {
		t.Helper()
		// The mapping is computed at start, so it is there once it is running.
		var raw string
		for range 30 {
			raw = strings.TrimSpace(incusOut(t, "config", "get", name, "volatile.idmap.current"))
			if raw != "" && raw != "[]" {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if raw == "" || raw == "[]" {
			t.Fatalf("the daemon computed no idmap for %s", name)
		}
		var entries []idmapEntry
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			t.Fatalf("volatile.idmap.current = %q: %v", raw, err)
		}
		return entries
	}

	fromBoth := expandIDMap(computed())

	if out, err := runIncus("stop", "-f", name); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	if out, err := runIncus("config", "set", name, "raw.idmap", "uid 1000 0\ngid 1000 0"); err != nil {
		t.Fatalf("set the split form: %v\n%s", err, out)
	}
	if out, err := runIncus("start", name); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}

	fromSplit := expandIDMap(computed())

	if !slices.Equal(fromBoth, fromSplit) {
		t.Errorf("the daemon computed different mappings:\n  both  = %v\n  split = %v\n"+
			"incus.SameIDMapping treats these as one, and idev skips a restart on that",
			fromBoth, fromSplit)
	}
}

// idmapEntry is one row of volatile.idmap.current.
type idmapEntry struct {
	Isuid    bool
	Isgid    bool
	Hostid   int
	Nsid     int
	Maprange int
}

// expandIDMap splits an entry that carries both flags into its uid and gid
// halves, so the two spellings become comparable -- the same expansion
// incus.SameIDMapping does on the text.
func expandIDMap(entries []idmapEntry) []string {
	var out []string
	for _, e := range entries {
		if e.Isuid {
			out = append(out, fmt.Sprintf("uid %d %d %d", e.Hostid, e.Nsid, e.Maprange))
		}
		if e.Isgid {
			out = append(out, fmt.Sprintf("gid %d %d %d", e.Hostid, e.Nsid, e.Maprange))
		}
	}
	slices.Sort(out)
	return out
}

// The map form of workspace mounts every entry, and the daemon agrees about
// which device carries which (spec 3.7.2, 3.7.8).
//
// The unit tests check what idev asks for. This checks what the container
// actually has, and in particular that main is still the device named
// workspace: an instance made before the map form existed has a device by
// that name, and idev must not rename it out from under a running project.
func TestWorkspaceMapFormMountsEveryEntry(t *testing.T) {
	f := newFixture(t, idmapYAML)

	data := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "host.txt"), []byte("from host\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(f.root, ".incus-dev", "dev.yml"),
		strings.ReplaceAll(strings.ReplaceAll(idmapYAML, "{{PROJECT}}", f.project),
			"{{IMAGE}}", testImage)+
			strings.ReplaceAll(`workspace:
  idmap: shift
  extra:
    source: DATA
    target: /data
`, "DATA", data))

	f.mustRun("up")

	// The project's own tree, under the name it has always had.
	if got := f.mustRun("shell", "--", "sh", "-c", "test -d /workspace && echo ok"); !strings.Contains(got, "ok") {
		t.Errorf("main is not mounted at /workspace: %q", got)
	}
	if got := incusOut(t, "config", "device", "get", f.instanceName(), "workspace", "path"); strings.TrimSpace(got) != "/workspace" {
		t.Errorf("the device %q is not main's mount: %q", "workspace", got)
	}
	if out, err := runIncus("config", "device", "get", f.instanceName(), "main", "path"); err == nil {
		t.Errorf("a device named main exists (%q); main is applied as %q so an "+
			"instance from an older idev is not remounted", out, "workspace")
	}

	// The second entry, under its own name.
	if got := f.mustRun("shell", "--", "cat", "/data/host.txt"); !strings.Contains(got, "from host") {
		t.Errorf("the second mount cannot be read: %q", got)
	}
	f.mustRun("shell", "--", "sh", "-c", "echo written > /data/from-container.txt")
	if _, err := os.Stat(filepath.Join(data, "from-container.txt")); err != nil {
		t.Errorf("the second mount could not be written: %v", err)
	}

	// The mapping is instance-wide, so it reached both disks.
	for _, device := range []string{"workspace", "extra"} {
		if got := incusOut(t, "config", "device", "get", f.instanceName(), device, "shift"); strings.TrimSpace(got) != "true" {
			t.Errorf("device %s shift = %q, want the section's idmap applied to every mount",
				device, got)
		}
	}
}

// raw and shift map opposite ends of the container (spec 3.7.3).
//
// The whole examples/dev-user/ configuration rests on this: raw maps the
// container's root onto the host user, so the workspace appears root-owned
// inside and an ordinary account cannot write to it at all. shift maps a
// container uid onto the host uid of the same number, so an account given the
// host user's uid can. Written as a comment either would be a belief; here it
// is the daemon's answer.
func TestIDMapMapsOppositeEndsForRawAndShift(t *testing.T) {
	requireIncus(t)

	hostUID := os.Getuid()
	if hostUID == 0 {
		t.Skip("running as root leaves nothing to tell apart")
	}

	for _, tt := range []struct {
		mode string
		// rootWriteOwner is the host uid of a file the container writes as
		// root; userCanWrite says whether an account holding the host's own
		// uid can write to the workspace at all.
		rootWriteOwner int
		userCanWrite   bool
	}{
		{"raw", hostUID, false},
		{"shift", 0, true},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			f := newFixture(t, strings.Replace(idmapYAML,
				"  image: {{IMAGE}}",
				"  image: {{IMAGE}}\nworkspace:\n  idmap: "+tt.mode, 1))

			if out, err := f.run("up"); err != nil {
				t.Skipf("this host cannot run idmap: %s here: %v\n%s", tt.mode, err, out)
			}

			// adduser on the busybox image, useradd on a glibc one.
			if out, err := f.run("exec", "--", "sh", "-c", fmt.Sprintf(
				"adduser -D -H -u %d probe 2>/dev/null || "+
					"useradd -M --non-unique -u %d probe", hostUID, hostUID)); err != nil {
				t.Fatalf("creating the probe account: %v\n%s", err, out)
			}
			f.mustRun("exec", "--", "touch", "/workspace/by-root")

			info, err := os.Stat(filepath.Join(f.root, "by-root"))
			if err != nil {
				t.Fatalf("by-root: %v", err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatalf("by-root: no ownership from %T", info.Sys())
			}
			if got := int(stat.Uid); got != tt.rootWriteOwner {
				t.Errorf("idmap %s: a file the container wrote as root is owned by "+
					"uid %d on the host, want %d", tt.mode, got, tt.rootWriteOwner)
			}

			_, err = f.run("exec", "--user", "probe", "--", "touch", "/workspace/by-user")
			switch {
			case tt.userCanWrite && err != nil:
				t.Errorf("idmap %s: an account with the host's uid cannot write to "+
					"the workspace: %v", tt.mode, err)
			case !tt.userCanWrite && err == nil:
				t.Errorf("idmap %s: an ordinary account could write to the workspace; "+
					"under raw it appears owned by root inside", tt.mode)
			}
		})
	}
}
