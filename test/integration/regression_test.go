//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests here exist because a defect got through. Each one names the
// belief about Incus that turned out to be wrong.
//
// A unit test could not have caught any of them: they are all about what
// Incus does, and the fake in internal/incus/incustest encodes whatever idev
// believed at the time -- so the belief and its test are the same mistake
// written twice (CLAUDE.md, "回帰テスト").

// A snapshot name Incus's storage driver cannot handle leaves a snapshot the
// instance cannot be deleted around: exec stops working, idev destroy fails,
// and so does incus delete --force. Recovering it needs a manual btrfs
// subvolume delete.
//
// The first fix then went the other way and refused names Incus accepts.
func TestSnapshotNameRules(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")

	// Refused before Incus is asked, so nothing is created.
	for _, name := range []string{".", "..", "a/b", "a b"} {
		out, err := f.run("snapshot", "create", name)
		if err == nil {
			t.Errorf("snapshot create %q succeeded, want it refused:\n%s", name, out)
		}
		if !strings.Contains(out, "invalid snapshot name") {
			t.Errorf("snapshot create %q said %q, want the name reported", name, out)
		}
	}

	// Accepted, because Incus accepts them. A rule stricter than the daemon's
	// refuses names projects may already be using.
	for _, name := range []string{"_baseline", "v1@rc", "before:upgrade"} {
		f.mustRun("snapshot", "create", name)
	}
	// A leading dash needs the usual --, which the unit test could not show:
	// it calls CreateSnapshot directly and never meets the flag parser.
	if out, err := f.run("snapshot", "create", "-wip"); err == nil {
		t.Errorf("snapshot create -wip succeeded, want the flag parser to refuse it:\n%s", out)
	}
	f.mustRun("snapshot", "create", "--", "-wip")
	if out := f.mustRun("snapshot", "list"); !strings.Contains(out, "v1@rc") {
		t.Errorf("snapshot list = %q, want the snapshots it created", out)
	}

	// The instance is still usable and still deletable, which is what the
	// rule is protecting.
	f.mustRun("exec", "--", "true")
	f.mustRun("destroy", "--force")
}

// A restart-required value changed and then changed back needs no restart:
// the running container already has what dev.yml asks for. Comparing the
// stored config before and after read the revert as a fresh change, so the
// warning never went away and 'idev up --restart' killed whatever was running
// inside the container to apply nothing.
func TestRestartPendingConvergesAfterARevert(t *testing.T) {
	const withNesting = minimalYAML + `    security.nesting: "true"
`
	const withoutNesting = minimalYAML + `    security.nesting: "false"
`

	f := newFixture(t, withNesting)
	f.mustRun("up")

	if out := f.mustRun("up"); strings.Contains(out, "restart") {
		t.Fatalf("a run that changed nothing asked for a restart:\n%s", out)
	}

	// Change it: a restart is owed, and recorded.
	writeFile(t, filepath.Join(f.root, ".incus-dev", "dev.yml"), render(f, withoutNesting))
	if out := f.mustRun("up"); !strings.Contains(out, "restart it to apply") {
		t.Fatalf("a real change did not ask for a restart:\n%s", out)
	}

	// Change it back: the container is already running with this.
	writeFile(t, filepath.Join(f.root, ".incus-dev", "dev.yml"), render(f, withNesting))
	if out := f.mustRun("up"); strings.Contains(out, "restart") {
		t.Errorf("a reverted change still asked for a restart:\n%s", out)
	}
	if got := incusOut(t, "config", "get", f.instance, "user.incus-dev.restart-pending"); got != "" {
		t.Errorf("restart-pending = %q, want it cleared", got)
	}

	// And the preview says the same thing, since it is read to decide.
	if out := f.mustRun("up", "--dry-run"); strings.Contains(out, "restart") {
		t.Errorf("the preview still asked for a restart:\n%s", out)
	}
}

// Two checkouts of one project share an instance by design, and up repoints
// the workspace at whichever ran last. What each command then says about the
// other checkout's tree was wrong for three rounds running: the warning for
// up was given to commands that remount nothing.
func TestWarningsForASecondCheckout(t *testing.T) {
	first := newFixture(t, minimalYAML)
	first.mustRun("up")

	// A second checkout of the same project: same dev.yml, another directory.
	second := &fixture{t: t, root: t.TempDir(), project: first.project, instance: first.instance}
	body, err := os.ReadFile(filepath.Join(first.root, ".incus-dev", "dev.yml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(second.root, ".incus-dev", "dev.yml"), string(body))

	tests := []struct {
		name string
		args []string
		want string
	}{
		// exec leaves the mount alone, so it acts on the first checkout.
		{"exec", []string{"exec", "--", "true"}, "stays mounted from there"},
		// The preview changes nothing, so it says what up would do.
		{"dry run", []string{"up", "--dry-run"}, "would remount"},
		// up is the one that repoints it.
		{"up", []string{"up"}, "is being remounted from this one"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := second.mustRun(tt.args...)
			if !strings.Contains(out, first.root) {
				t.Errorf("output = %q, want the other checkout named", out)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("output = %q, want it to contain %q", out, tt.want)
			}
		})
	}
}

// destroy deletes the instance before its volumes, because Incus refuses to
// delete an attached one -- and the instance carries the only record naming
// them. Every mistake here was about that ordering: offering to delete a
// volume the same line promised to keep, or leaving one nothing could name.
func TestDestroyVolumesAfterOneLeavesTheDeclaration(t *testing.T) {
	const withTwo = minimalYAML + `
volumes:
  keep:
    path: /keep
  gone:
    path: /gone
`
	const withOne = minimalYAML + `
volumes:
  keep:
    path: /keep
`

	f := newFixture(t, withTwo)
	f.mustRun("up")

	kept := f.instance + "-keep"
	dropped := f.instance + "-gone"
	for _, v := range []string{kept, dropped} {
		if out := incusOut(t, "storage", "volume", "list", "default", "--format", "csv"); !strings.Contains(out, v) {
			t.Fatalf("volume %s was not created:\n%s", v, out)
		}
	}

	// Drop one from the declaration. The volume stays; up says so, and must
	// not offer a command that deletes the one it is keeping.
	writeFile(t, filepath.Join(f.root, ".incus-dev", "dev.yml"), render(f, withOne))
	out := f.mustRun("up")
	if !strings.Contains(out, dropped) {
		t.Errorf("up = %q, want the dropped volume named", out)
	}
	if strings.Contains(out, "delete default "+kept) {
		t.Errorf("up = %q, want no command that deletes the volume it keeps", out)
	}

	// destroy --volumes reaches the dropped one, which nothing else can name.
	f.mustRun("destroy", "--volumes", "--force")
	list := incusOut(t, "storage", "volume", "list", "default", "--format", "csv")
	for _, v := range []string{kept, dropped} {
		if strings.Contains(list, v) {
			t.Errorf("volume %s survived destroy --volumes:\n%s", v, list)
		}
	}
}

// An instance made before idev recorded what it applied is not adopted, and
// idev cannot follow what it never wrote down. The first up on one is the
// last run that can say so.
func TestAnInstanceFromBeforeTheRecords(t *testing.T) {
	f := newFixture(t, minimalYAML)
	f.mustRun("up")

	// Roll it back to what an older idev left: the markers it had then, plus
	// a config key and a device from a dev.yml that has since changed.
	for _, key := range []string{"user.incus-dev.managed", "user.incus-dev.devices", "user.incus-dev.image"} {
		if out, err := runIncus("config", "unset", f.instance, key); err != nil {
			t.Fatalf("incus config unset %s: %v\n%s", key, err, out)
		}
	}
	if out, err := runIncus("config", "set", f.instance, "limits.memory=512MiB"); err != nil {
		t.Fatalf("incus config set: %v\n%s", err, out)
	}

	out := f.mustRun("up")
	if !strings.Contains(out, "predates idev's record") {
		t.Errorf("up = %q, want it to say the instance predates the records", out)
	}
	if !strings.Contains(out, "limits.memory") {
		t.Errorf("up = %q, want the setting it cannot follow named", out)
	}
	// Incus writes these itself; naming them buries the ones the user can act on.
	for _, unwanted := range []string{"volatile.", "image.os"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("up = %q, want %q left out", out, unwanted)
		}
	}

	// status does not present the declaration as what the instance was made
	// from when it has no record of it.
	if got := f.mustRun("status"); !strings.Contains(got, "not recorded") {
		t.Errorf("status = %q, want the image row to say it is not recorded", got)
	}
}

// idev has to create an instance from an image the daemon has not cached.
//
// It used to resolve the alias to a fingerprint itself and ask the daemon for
// that fingerprint. A simplestreams remote cannot be queried by fingerprint,
// so the daemon could only satisfy it out of its local cache -- which made
// every 'idev up' fail from the moment the upstream image was rebuilt (daily,
// for these images) until something else pulled it by alias, and made a first
// run on a clean host fail outright.
//
// The test deletes the cached copy first, which is what a clean host has.
func TestUpFromAnImageTheDaemonHasNotCached(t *testing.T) {
	f := newFixture(t, minimalYAML)

	// The fingerprint the alias points at right now, and the copy the daemon
	// happens to hold. Removing it puts this host in the state a new one is
	// in. Incus re-downloads it, so nothing is lost.
	out, err := runIncus("image", "list", "--format", "csv", "-c", "fd")
	if err != nil {
		t.Fatalf("incus image list: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.SplitN(line, ",", 2)
		if len(fields) == 2 && strings.Contains(strings.ToLower(fields[1]), "alpine") {
			if _, err := runIncus("image", "delete", fields[0]); err != nil {
				t.Logf("could not remove the cached image %s: %v", fields[0], err)
			}
		}
	}

	f.mustRun("up")
	if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "s"); got != "RUNNING" {
		t.Errorf("status = %q, want RUNNING", got)
	}
}
