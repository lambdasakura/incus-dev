package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/incus/incustest"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
)

// appFromAnotherCheckout builds an App over an instance recorded as last used
// somewhere else, which is what makes the checkout warning fire.
func appFromAnotherCheckout(t *testing.T, extra map[string]string) (*App, *incustest.Fake, *bytes.Buffer) {
	t.Helper()

	cfg := mustParse(t, rootYAML)
	config := map[string]string{
		managedProjectKey: "example-project",
		managedRootKey:    "/home/u/other-checkout",
	}
	for k, v := range extra {
		config[k] = v
	}

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   config,
	})

	errOut := &bytes.Buffer{}
	return NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	}), client, errOut
}

// rebuild recreates the instance from this checkout, so it must not say the
// workspace stays mounted from the other one.
func TestRebuildSaysTheWorkspaceIsRemounted(t *testing.T) {
	app, _, errOut := appFromAnotherCheckout(t, nil)

	if err := app.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}

	got := errOut.String()
	if !strings.Contains(got, "/home/u/other-checkout") {
		t.Fatalf("warning = %q, want the other checkout named", got)
	}
	if strings.Contains(got, "acts on that tree") {
		t.Errorf("warning = %q, want it not to claim rebuild acts on the other tree", got)
	}
	if !strings.Contains(got, "remounted from this one") {
		t.Errorf("warning = %q, want it to say the workspace is remounted here", got)
	}
}

// destroy touches no tree at all: it deletes the instance the other checkout
// is using. Saying it "acts on that tree" is contradicted three lines later.
func TestDestroySaysTheInstanceIsInUseElsewhere(t *testing.T) {
	app, _, errOut := appFromAnotherCheckout(t, nil)

	if err := app.Destroy(context.Background(), DestroyOptions{}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}

	got := errOut.String()
	if strings.Contains(got, "acts on that tree") {
		t.Errorf("warning = %q, want destroy not to claim it acts on a tree", got)
	}
	if !strings.Contains(got, "in use from there") {
		t.Errorf("warning = %q, want it to say the environment is in use there", got)
	}
}

// The closing line counts warnings; it must not claim they were all things
// idev tried and failed to apply. A missing network address is neither.
func TestReadyLineDoesNotSayIdevFailedToApply(t *testing.T) {
	app, _, errOut := appFromAnotherCheckout(t, nil)

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	got := errOut.String()
	if !strings.Contains(got, "2 warning(s) above") {
		t.Errorf("output = %q, want the closing line to count both warnings", got)
	}
	if strings.Contains(got, "could not apply") {
		t.Errorf("output = %q, want no claim about what idev could not apply", got)
	}
}

// A pending restart is one fact, so the preview states it once, on the same
// stream up uses.
func TestPlanReportsAPendingRestartOnce(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	app, _, errOut := appFromAnotherCheckout(t, map[string]string{
		managedRestartKey: recordRestart(started, map[string]bootedValue{"security.nesting": {value: "false", known: true}}),
	})

	out := &bytes.Buffer{}
	app.out = out
	if err := app.Plan(context.Background()); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	both := errOut.String() + out.String()
	if n := strings.Count(both, "security.nesting"); n != 1 {
		t.Errorf("plan mentioned the pending key %d times, want 1:\nstderr=%s\nstdout=%s",
			n, errOut.String(), out.String())
	}
	if strings.Contains(out.String(), "Needs a restart") {
		t.Errorf("stdout = %q, want the restart reported as a warning, as up does", out.String())
	}
}

// The advice for a volume idev is leaving behind has to be a command that
// runs; the recorded form is pool/name, not the argument form.
func TestKeptVolumeAdviceIsRunnable(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache",
		},
	})
	client.Volumes["default/dev-example-project-cache"] = true

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Destroy(context.Background(), DestroyOptions{}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}

	if !strings.Contains(errOut.String(),
		"incus storage volume delete default dev-example-project-cache") {
		t.Errorf("output = %q, want the advice to name pool and volume as operands", errOut.String())
	}
}

// A handler built without newLogger still logs a warning instead of panicking.
func TestHandlerWithoutACounterDoesNotPanic(t *testing.T) {
	var buf bytes.Buffer
	h := &handler{w: &buf, level: slog.LevelInfo}

	if err := h.Handle(context.Background(),
		slog.NewRecord(time.Time{}, slog.LevelWarn, "careful", 0)); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !strings.Contains(buf.String(), "[idev] warning: careful") {
		t.Errorf("output = %q", buf.String())
	}
}

// A record written by hand, or by a version that stored something else, still
// produces advice — just without operands it cannot know.
func TestKeptVolumeAdviceSurvivesAMalformedRecord(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "garbage",
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Destroy(context.Background(), DestroyOptions{}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if !strings.Contains(errOut.String(), "incus storage volume delete <pool> <volume>") {
		t.Errorf("output = %q, want the advice without operands", errOut.String())
	}
}

// With no instance there is nothing for --volumes to delete, so the prompt
// does not escalate; destroy itself reports that it does not exist.
func TestHasVolumesIsFalseWithoutAnInstance(t *testing.T) {
	cfg := mustParse(t, rootYAML)
	app := NewApp(AppOptions{
		Config: cfg, Client: incustest.New(), Runner: &runnertest.Fake{},
		CheckIDMap: func(int, int) error { return nil },
	})

	if app.HasVolumes(context.Background()) {
		t.Error("HasVolumes() = true, want false when the instance does not exist")
	}
}

// A stopped instance applies everything when it starts, so the preview does
// not ask for a restart.
func TestPlanSaysNothingAboutRestartingAStoppedInstance(t *testing.T) {
	app, client, errOut := appFromAnotherCheckout(t, map[string]string{
		managedRestartKey: recordRestart(time.Now().Add(-time.Hour), map[string]bootedValue{"security.nesting": {value: "false", known: true}}),
	})
	// appFromAnotherCheckout leaves it Running; stop it.
	client.Instances["dev-example-project"].Status = "Stopped"

	app.out = &bytes.Buffer{}
	if err := app.Plan(context.Background()); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart") {
		t.Errorf("warnings = %q, want no restart warning for a stopped instance", errOut.String())
	}
}

// destroy keeps the declared volumes and says the next up adopts them. The
// command it offers for "any other" must not name one of those: pasting it
// would delete live project data the same sentence promised to keep.
func TestKeptVolumeAdviceNamesOnlyTheUndeclaredOnes(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  data:\n    path: /data\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-data,default/dev-example-project-old",
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Destroy(context.Background(), DestroyOptions{}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}

	got := errOut.String()
	if strings.Contains(got, "delete default dev-example-project-data") {
		t.Errorf("output = %q, want the declared volume not offered for deletion", got)
	}
	if !strings.Contains(got, "delete default dev-example-project-old") {
		t.Errorf("output = %q, want the undeclared volume named", got)
	}
}

// Every recorded volume is still declared, so there is no "any other" to
// remove and no command to offer.
func TestKeptVolumeAdviceIsSilentWithNothingElseToRemove(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  data:\n    path: /data\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-data",
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Destroy(context.Background(), DestroyOptions{}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if strings.Contains(errOut.String(), "incus storage volume delete") {
		t.Errorf("output = %q, want no deletion advice when nothing else is recorded", errOut.String())
	}
}

// A record can outlive the volume: destroy never prunes it. Escalating the
// prompt for data that is already gone is the scare that makes a user answer
// N to a harmless destroy.
func TestHasVolumesIgnoresARecordWithoutAVolume(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache",
		},
	})
	// The pool has no such volume: someone removed it by hand.

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		CheckIDMap: func(int, int) error { return nil },
	})

	if app.HasVolumes(context.Background()) {
		t.Error("HasVolumes() = true, want false when the recorded volume is gone")
	}
}

// The preview changes nothing (spec 04-cli.md 4.8), so it says what up would
// do, not what is happening.
func TestPlanSaysTheRemountWouldHappen(t *testing.T) {
	app, _, errOut := appFromAnotherCheckout(t, nil)
	app.out = &bytes.Buffer{}

	if err := app.Plan(context.Background()); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if strings.Contains(errOut.String(), "is being remounted") {
		t.Errorf("warning = %q, want the preview not to claim it is remounting", errOut.String())
	}
	if !strings.Contains(errOut.String(), "would remount") {
		t.Errorf("warning = %q, want the preview to say what up would do", errOut.String())
	}
}

// --force asks nothing, so it must not pay for the lookup that only decides
// how the question is worded.
func TestForcedDestroyDoesNotLookUpVolumes(t *testing.T) {
	out := &bytes.Buffer{}
	app, client := fakeAppWith(t, rootYAML+"volumes:\n  data:\n    path: /data\n", out)
	client.Volumes["default/dev-example-project-data"] = true
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
	})

	root := newRootCommand("test", stub(app), stub(app))
	root.SetArgs([]string{"destroy", "--volumes", "--force"})
	root.SetOut(out)
	root.SetErr(out)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !slices.Contains(client.Calls, "volume delete default dev-example-project-data") {
		t.Fatalf("calls = %v, want the volume deleted", client.Calls)
	}
	// One lookup, made by destroy itself.
	var lookups int
	for _, c := range client.Calls {
		if c == "instance dev-example-project" {
			lookups++
		}
	}
	if lookups != 1 {
		t.Errorf("instance lookups = %d, want 1 with --force", lookups)
	}
}

// More than one volume is left behind, so the advice says the command has to
// be repeated rather than naming only the first.
func TestKeptVolumeAdviceSaysWhenThereAreMore(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-a,default/dev-example-project-b",
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Destroy(context.Background(), DestroyOptions{}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if !strings.Contains(errOut.String(), "dev-example-project-a', and so on") {
		t.Errorf("output = %q, want it to say there are more", errOut.String())
	}
}

// A record entry that is not pool/name names no volume, so it is not data the
// prompt should ask about.
func TestHasVolumesIgnoresAMalformedRecord(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "garbage",
		},
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		CheckIDMap: func(int, int) error { return nil },
	})

	if app.HasVolumes(context.Background()) {
		t.Error("HasVolumes() = true, want false for a record naming no volume")
	}
}

// The record outlives a volume someone removed by hand, and destroy never
// prunes it. Deleting straight from the record would fail against Incus with
// the instance and some volumes already gone.
func TestDestroyVolumesSkipsARecordWithoutAVolume(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.Volumes["default/dev-example-project-b"] = true
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			// a is recorded but gone from the pool.
			managedVolumesKey: "default/dev-example-project-a,default/dev-example-project-b",
		},
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Destroy(context.Background(), DestroyOptions{Volumes: true}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if slices.Contains(client.Calls, "volume delete default dev-example-project-a") {
		t.Errorf("calls = %v, want no delete for a volume that is gone", client.Calls)
	}
	if !slices.Contains(client.Calls, "volume delete default dev-example-project-b") {
		t.Errorf("calls = %v, want the volume that is there deleted", client.Calls)
	}
}

// The instance carries the only record naming its volumes, and destroy
// deletes the instance first. If the cleanup then fails, the names have to
// come out in the error -- nothing can produce them again.
func TestDestroyNamesTheVolumesItCouldNotDelete(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.Volumes["default/dev-example-project-a"] = true
	client.Volumes["default/dev-example-project-b"] = true
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-a,default/dev-example-project-b",
		},
	})
	client.FailOn = map[string]error{"volume delete default dev-example-project-a": errBoom}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Destroy(context.Background(), DestroyOptions{Volumes: true})
	if err == nil {
		t.Fatal("Destroy() = nil error, want the failure reported")
	}
	for _, want := range []string{"dev-example-project-a", "dev-example-project-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err.Error(), want)
		}
	}
}

// A volume's disk device is removed from the instance only if the device
// record names it: ApplyDevices merges, so staleDevices is the whole
// mechanism. Without the name, a volume dropped from dev.yml stays mounted.
func TestManagedDevicesRecordsVolumeDevices(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n")

	plan := idmapPlan{}
	desired := desiredConfig(cfg, plan, nil, "dev-example-project")

	// The device keys themselves: staleDevices matches the record against
	// them, so recording the volume's name instead would look right in a
	// substring check and remove nothing.
	want := slices.Sorted(maps.Keys(desiredDevices(cfg, plan, "dev-example-project")))
	got := splitList(desired[managedDevicesKey])

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", managedDevicesKey, diff)
	}
}

// up never reassigns profiles, so a changed declaration has no effect until
// the instance is rebuilt. Comparing only the count would miss a rename.
func TestUpWarnsWhenAProfileIsRenamed(t *testing.T) {
	cfg := mustParse(t, rootYAML+"  profiles: [web]\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"}, // same count, different name
		Config:   map[string]string{managedProjectKey: "example-project"},
	})
	client.Profiles = []string{"default", "web"}

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(errOut.String(), "idev rebuild") {
		t.Errorf("output = %q, want the profile change reported", errOut.String())
	}
}

// status names the declared workspace source only when it is not the one
// mounted -- getting this backwards hides the drift and invents it.
func TestStatusDeclaresTheWorkspaceSourceOnlyOnDrift(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
		Devices: map[string]incus.Device{
			"workspace": {"type": "disk", "path": "/workspace", "source": "/home/u/other"},
		},
	})

	out := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Status(context.Background(), true); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !strings.Contains(out.String(), `"workspace_source_declared"`) {
		t.Errorf("status = %s, want the declared source named on drift", out.String())
	}

	// The same source: nothing to declare.
	out.Reset()
	client.Instances["dev-example-project"].Devices["workspace"]["source"] = cfg.WorkspaceSourcePath()
	if err := app.Status(context.Background(), true); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if strings.Contains(out.String(), `"workspace_source_declared"`) {
		t.Errorf("status = %s, want it omitted when they agree", out.String())
	}
}

// The record is compared against the instance's last start time, so it has to
// keep the precision that comparison needs. Truncated to the second, a record
// written now looks older than the start it was written for, and the warning
// disappears on the very next run.
func TestRestartRecordKeepsSubSecondPrecision(t *testing.T) {
	started := time.Date(2026, 9, 1, 12, 0, 0, 123456789, time.UTC)
	record := recordRestart(started, map[string]bootedValue{"raw.idmap": {value: "uid 1000 0", known: true}})

	got := pendingRestart(map[string]string{managedRestartKey: record}, started)

	if b := got["raw.idmap"]; b.value != "uid 1000 0" || !b.known {
		t.Errorf("pendingRestart() = %v, want the record still owed", got)
	}
}

// up remounts the workspace from this checkout, so its warning says so. The
// other wording belongs to the commands that leave the mount alone.
func TestUpSaysTheWorkspaceIsBeingRemounted(t *testing.T) {
	app, _, errOut := appFromAnotherCheckout(t, nil)

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(errOut.String(), "the workspace is being remounted from this one") {
		t.Errorf("warning = %q, want up to say it is remounting", errOut.String())
	}
}

// The preview prunes the record as up does, so it does not warn about a
// volume up would stay quiet about.
func TestPlanDoesNotWarnAboutAVolumeAlreadyGone(t *testing.T) {
	app, client, errOut := appFromAnotherCheckout(t, map[string]string{
		managedVolumesKey: "default/dev-example-project-cache",
	})
	// The pool no longer has it, so the record is stale, not a dropped volume.
	if len(client.Volumes) != 0 {
		t.Fatalf("Volumes = %v, want the pool empty", client.Volumes)
	}

	if err := app.Plan(context.Background()); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if strings.Contains(errOut.String(), "no longer declared") {
		t.Errorf("warning = %q, want no complaint about a volume that is gone", errOut.String())
	}
}

// The names in a failed cleanup are the ones that were not deleted -- not a
// claim about what is on the pool, which idev cannot know once the client
// has failed. A record entry that names no volume is left out: it is what
// splitVolume exists to discard.
func TestFailedVolumeCleanupNamesOnlyRealReferences(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.Volumes["default/dev-example-project-a"] = true
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-a,junk-entry,default/dev-example-project-b",
		},
	})
	client.FailOn = map[string]error{"volume delete default dev-example-project-a": errBoom}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Destroy(context.Background(), DestroyOptions{Volumes: true})
	if err == nil {
		t.Fatal("Destroy() = nil error, want the failure reported")
	}
	if strings.Contains(err.Error(), "junk-entry") {
		t.Errorf("error = %q, want the malformed record entry left out", err.Error())
	}
	for _, want := range []string{"dev-example-project-a", "dev-example-project-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err.Error(), want)
		}
	}
}

// A cancellation that lands while the instance is being deleted takes the
// record with it, so the names have to come out of that failure too.
func TestDestroyNamesTheVolumesWhenTheInstanceDeleteFails(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.Volumes["default/dev-example-project-a"] = true
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-a",
		},
	})
	client.FailOn = map[string]error{"delete dev-example-project": errBoom}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Destroy(context.Background(), DestroyOptions{Volumes: true})
	if err == nil {
		t.Fatal("Destroy() = nil error, want the failure reported")
	}
	if !strings.Contains(err.Error(), "dev-example-project-a") {
		t.Errorf("error = %q, want it to name the volume left behind", err.Error())
	}
}

// When the instance is still there, its volumes are still attached: telling
// the user to delete them by hand hands them a command Incus refuses. The
// action that works is running destroy again.
func TestDestroySaysToRetryWhenTheInstanceSurvived(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.Volumes["default/dev-example-project-cache"] = true
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache",
		},
	})
	client.FailOn = map[string]error{"delete dev-example-project": errBoom}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Destroy(context.Background(), DestroyOptions{Volumes: true})
	if err == nil {
		t.Fatal("Destroy() = nil error, want the failure reported")
	}
	if !strings.Contains(err.Error(), "idev destroy --volumes") {
		t.Errorf("error = %q, want it to say to run destroy again", err.Error())
	}
	if strings.Contains(err.Error(), "if the instance is gone") {
		t.Errorf("error = %q, want no hedging about a fact the caller knows", err.Error())
	}
}

// Once the instance is deleted, idev knows the record went with it. The
// message says so rather than hedging.
func TestFailedVolumeCleanupIsCertainTheInstanceIsGone(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.Volumes["default/dev-example-project-cache"] = true
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-cache",
		},
	})
	client.FailOn = map[string]error{"volume delete": errBoom}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Destroy(context.Background(), DestroyOptions{Volumes: true})
	if err == nil {
		t.Fatal("Destroy() = nil error, want the failure reported")
	}
	if strings.Contains(err.Error(), "if the instance is gone") {
		t.Errorf("error = %q, want it stated, not hedged", err.Error())
	}
	if !strings.Contains(err.Error(), "incus storage volume delete default dev-example-project-cache") {
		t.Errorf("error = %q, want the command to remove them", err.Error())
	}
}

// A snapshot name goes straight to Incus, and a bad one can leave the
// instance unusable and undeletable -- the pool keeps a snapshot idev can
// name but nothing can remove. Every other identifier idev forwards is
// checked; this one was not.
func TestSnapshotNameIsChecked(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	// Rejected: the ones that wedge the instance or that Incus itself refuses.
	for _, name := range []string{".", "..", "a/b", "a b", "with\nnewline", "\x01ctl"} {
		t.Run(name, func(t *testing.T) {
			client.Calls = nil

			if err := app.CreateSnapshot(context.Background(), name); err == nil {
				t.Fatalf("CreateSnapshot(%q) = nil error, want it refused", name)
			}
			if slices.ContainsFunc(client.Calls, func(c string) bool {
				return strings.HasPrefix(c, "snapshot create")
			}) {
				t.Errorf("calls = %v, want nothing sent to Incus", client.Calls)
			}
		})
	}

	// Accepted: everything Incus accepts. A rule stricter than the daemon's
	// would refuse names projects may already be using.
	for _, name := range []string{"_baseline", "feature+1", "v1@rc", "before:upgrade",
		"-wip", "a~1", "20260901-125226"} {
		t.Run(name, func(t *testing.T) {
			client.Calls = nil

			if err := app.CreateSnapshot(context.Background(), name); err != nil {
				t.Errorf("CreateSnapshot(%q) error = %v, want it accepted", name, err)
			}
		})
	}

	// The documented "no name given" case still works: the timestamp it
	// generates has to pass the same check.
	t.Run("no name", func(t *testing.T) {
		client.Calls = nil

		if err := app.CreateSnapshot(context.Background(), ""); err != nil {
			t.Fatalf("CreateSnapshot(\"\") error = %v, want the timestamp used", err)
		}
		if !slices.ContainsFunc(client.Calls, func(c string) bool {
			return strings.HasPrefix(c, "snapshot create")
		}) {
			t.Errorf("calls = %v, want the snapshot created", client.Calls)
		}
	})
}

// A pool that no longer exists holds no volumes, so there is nothing for
// destroy to delete and nothing for the record to name.
//
// Failing here is worse than useless: the instance is already gone, so the
// run cannot be retried, and the command it suggests names a pool Incus says
// does not exist.
func TestDestroyVolumesToleratesAMissingPool(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "oldpool/dev-example-project-data",
		},
	})
	client.FailOn = map[string]error{
		"volume exists oldpool": fmt.Errorf("storage pool oldpool: %w", incus.ErrPoolNotFound),
	}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Destroy(context.Background(), DestroyOptions{Volumes: true}); err != nil {
		t.Errorf("Destroy() error = %v, want a pool that is gone to be nothing to do", err)
	}
}

// The record names a pool with no row, so no volume it names can exist. That
// is a record to drop, not one to keep warning about on every run.
func TestUpDropsARecordOnAMissingPool(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "oldpool/dev-example-project-data",
		},
	})
	client.FailOn = map[string]error{
		"volume exists oldpool": fmt.Errorf("storage pool oldpool: %w", incus.ErrPoolNotFound),
	}

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "no longer declared") {
		t.Errorf("output = %q, want no warning about a pool that is gone", errOut.String())
	}
	if got := client.Instances["dev-example-project"].Config[managedVolumesKey]; got != "" {
		t.Errorf("%s = %q, want the record dropped", managedVolumesKey, got)
	}
}

// The preview shows a volume on a pool that is gone as one that would be
// created; up is what reports the pool, and says so plainly.
func TestPlanToleratesAMissingPool(t *testing.T) {
	cfg := mustParse(t, rootYAML+"volumes:\n  cache:\n    path: /cache\n    pool: oldpool\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		Config:   map[string]string{managedProjectKey: "example-project"},
	})
	client.FailOn = map[string]error{
		"volume exists oldpool": fmt.Errorf("storage pool oldpool: %w", incus.ErrPoolNotFound),
	}

	out := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Plan(context.Background()); err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !strings.Contains(out.String(), "Create volume") {
		t.Errorf("plan =\n%s\nwant the volume shown as one to create", out.String())
	}
}

// Any other failure to check a volume still stops the cleanup, naming what is
// left.
func TestDestroyVolumesStopsOnAnUncheckableVolume(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedVolumesKey: "default/dev-example-project-a",
		},
	})
	client.FailOn = map[string]error{"volume exists default": errBoom}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	err := app.Destroy(context.Background(), DestroyOptions{Volumes: true})
	if err == nil || !strings.Contains(err.Error(), "dev-example-project-a") {
		t.Errorf("error = %v, want the volume named", err)
	}
}

// A restart-required value changed and then changed back needs no restart:
// the running container already has what dev.yml now asks for.
//
// Comparing the stored config before and after reads the revert as a fresh
// change, so the warning never goes away and 'idev up --restart' kills the
// container to apply nothing.
func TestRestartOwedClearsWhenAChangeIsReverted(t *testing.T) {
	cfg := mustParse(t, rootYAML+
		"  config:\n    security.nesting: \"true\"\nworkspace:\n  idmap: none\n")

	client := incustest.New()
	started := time.Now().Add(-time.Hour)
	client.AddInstance(&incus.Instance{
		Name:       "dev-example-project",
		Status:     "Running",
		Profiles:   []string{"default"},
		LastUsedAt: started,
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedKeysKey:    "security.nesting",
			// The declaration said false last run, so this is what is stored;
			// the container booted with true.
			"security.nesting": "false",
			managedRestartKey:  recordRestart(started, map[string]bootedValue{"security.nesting": {value: "true", known: true}}),
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart") {
		t.Errorf("output = %q, want no restart warning once the value is back", errOut.String())
	}
	if got := client.Instances["dev-example-project"].Config[managedRestartKey]; got != "" {
		t.Errorf("%s = %q, want the record cleared", managedRestartKey, got)
	}
}

// A change that is still a change keeps the record, and the record carries
// what the container booted with.
func TestRestartRecordCarriesTheBootedValue(t *testing.T) {
	cfg := mustParse(t, rootYAML+
		"  config:\n    security.nesting: \"true\"\nworkspace:\n  idmap: none\n")

	client := incustest.New()
	started := time.Now().Add(-time.Hour)
	client.AddInstance(&incus.Instance{
		Name:       "dev-example-project",
		Status:     "Running",
		Profiles:   []string{"default"},
		LastUsedAt: started,
		Config: map[string]string{
			managedProjectKey:  "example-project",
			"security.nesting": "false",
		},
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	got := client.Instances["dev-example-project"].Config[managedRestartKey]
	if !strings.Contains(got, "security.nesting=false") {
		t.Errorf("%s = %q, want it to record what the container booted with", managedRestartKey, got)
	}
}

// Two snapshots in the same second are the "run it twice" case for the one
// command whose default name comes from the clock.
func TestSnapshotCollisionIsExplained(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
	})
	client.FailOn = map[string]error{
		"snapshot create": fmt.Errorf("create snapshot x: %w", incus.ErrSnapshotExists),
	}

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	// The clock explanation belongs only to the name idev chose itself.
	err := app.CreateSnapshot(context.Background(), "")
	if err == nil {
		t.Fatal("CreateSnapshot() = nil error, want the collision reported")
	}
	for _, want := range []string{"already has a snapshot", "time to the second"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if !errors.Is(err, incus.ErrSnapshotExists) {
		t.Errorf("error = %v, want it to stay matchable", err)
	}

	err = app.CreateSnapshot(context.Background(), "before-upgrade")
	if err == nil {
		t.Fatal("CreateSnapshot() = nil error, want the collision reported")
	}
	if strings.Contains(err.Error(), "time to the second") {
		t.Errorf("error = %q, want no clock explanation for a name the user typed", err.Error())
	}
}

// A record written before values were stored says a restart is owed and does
// not say what the container booted with. Reading the missing value as ""
// makes it match a key an earlier run unset, and the owed restart vanishes:
// no warning, the record cleared, and 'idev up --restart' refusing to
// restart. The container keeps running with the old idmap for good.
func TestOldRestartRecordForAnUnsetKeyStaysOwed(t *testing.T) {
	cfg := mustParse(t, rootYAML+"workspace:\n  idmap: none\n")

	started := time.Now().Add(-time.Hour)
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:       "dev-example-project",
		Status:     "Running",
		Profiles:   []string{"default"},
		LastUsedAt: started,
		Config: map[string]string{
			managedProjectKey: "example-project",
			// What an older idev wrote: the key, no value. It unset
			// raw.idmap in the same run, so the config no longer has it.
			managedRestartKey: started.Format(time.RFC3339Nano) + "|raw.idmap",
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(errOut.String(), "raw.idmap") {
		t.Errorf("output = %q, want the owed restart still reported", errOut.String())
	}
	if got := client.Instances["dev-example-project"].Config[managedRestartKey]; got == "" {
		t.Error("the record was cleared, so nothing will report it again")
	}
}

// And --restart actually restarts for it.
func TestOldRestartRecordForAnUnsetKeyRestarts(t *testing.T) {
	cfg := mustParse(t, rootYAML+"workspace:\n  idmap: none\n")

	started := time.Now().Add(-time.Hour)
	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:       "dev-example-project",
		Status:     "Running",
		Profiles:   []string{"default"},
		LastUsedAt: started,
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedRestartKey: started.Format(time.RFC3339Nano) + "|raw.idmap",
		},
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{Restart: true}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !slices.Contains(client.Calls, "stop dev-example-project") {
		t.Errorf("calls = %v, want the instance restarted", client.Calls)
	}
}

// An unknown booted value stays unknown when the record is rewritten:
// inventing one would let the next run decide the restart is no longer owed.
func TestUnknownBootedValueSurvivesRewriting(t *testing.T) {
	record := recordRestart(time.Time{}, map[string]bootedValue{
		"raw.idmap":        {known: false},
		"security.nesting": {value: "true", known: true},
	})

	got := pendingRestart(map[string]string{managedRestartKey: record}, time.Time{})

	if b := got["raw.idmap"]; b.known {
		t.Errorf("raw.idmap = %+v, want it still unknown", b)
	}
	if b := got["security.nesting"]; !b.known || b.value != "true" {
		t.Errorf("security.nesting = %+v, want the value kept", b)
	}
}

// The rules that derive an instance name have changed, so an upgraded
// checkout can stop finding its own environment. Creating a second, empty one
// beside it -- with the provisioned one still running, unreachable by every
// idev command, and its volumes unnameable -- must not happen silently.
func TestUpNamesAnInstanceItCanNoLongerDerive(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project-feature-a-very-long-branch-name",
		Status: "Running",
		Config: map[string]string{
			managedProjectKey: "example-project",
			managedRootKey:    cfg.Root,
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	got := errOut.String()
	if !strings.Contains(got, "dev-example-project-feature-a-very-long-branch-name") {
		t.Errorf("output = %q, want the stranded instance named", got)
	}
}

// The same for one an older idev made under the previous marker prefix: idev
// will not adopt it, so the user has to be told it is there.
func TestUpNamesAnInstanceFromAnOlderMarkerPrefix(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project-old",
		Status: "Running",
		Config: map[string]string{
			"user.incus-devkit.project": "example-project",
			"user.incus-devkit.root":    cfg.Root,
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if !strings.Contains(errOut.String(), "dev-example-project-old") {
		t.Errorf("output = %q, want the older instance named", errOut.String())
	}
}

// The raw.idmap idev writes changed spelling; the mapping did not. Demanding
// a restart to apply what the kernel is already doing costs every upgrader
// whatever they had running inside the container.
func TestUpDoesNotRestartForARespelledIDMap(t *testing.T) {
	cfg := mustParse(t, rootYAML+"workspace:\n  idmap: raw\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:       "dev-example-project",
		Status:     "Running",
		Profiles:   []string{"default"},
		LastUsedAt: time.Now().Add(-time.Hour),
		Config: map[string]string{
			managedProjectKey: "example-project",
			// What an older idev wrote for the same mapping.
			idmapConfigKey: "both 1000 0",
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut,
		Host:       &HostIDs{UID: 1000, GID: 1000},
		CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "restart") {
		t.Errorf("output = %q, want no restart for the same mapping respelled", errOut.String())
	}
}

// The look for a stranded instance is advisory: a failure must not stop up,
// and another checkout of the same project is not stranded.
func TestStrandedLookIsAdvisoryAndScopeAware(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	t.Run("a failure does not stop up", func(t *testing.T) {
		client := incustest.New()
		client.FailOn = map[string]error{"instances": errBoom}

		app := NewApp(AppOptions{
			Config: cfg, Client: client, Runner: &runnertest.Fake{},
			Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
		})
		if err := app.Up(context.Background(), UpOptions{}); err != nil {
			t.Errorf("Up() error = %v, want the look to be advisory", err)
		}
	})

	t.Run("another checkout is not stranded", func(t *testing.T) {
		client := incustest.New()
		client.AddInstance(&incus.Instance{
			Name:   "dev-example-project-other",
			Status: "Running",
			Config: map[string]string{
				managedProjectKey: "example-project",
				managedRootKey:    "/home/u/another-checkout",
			},
		})
		// Nor is somebody else's project.
		client.AddInstance(&incus.Instance{
			Name:   "dev-unrelated",
			Status: "Running",
			Config: map[string]string{managedProjectKey: "unrelated"},
		})

		errOut := &bytes.Buffer{}
		app := NewApp(AppOptions{
			Config: cfg, Client: client, Runner: &runnertest.Fake{},
			Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
		})
		if err := app.Up(context.Background(), UpOptions{}); err != nil {
			t.Fatalf("Up() error = %v", err)
		}
		for _, unwanted := range []string{"dev-example-project-other", "dev-unrelated"} {
			if strings.Contains(errOut.String(), unwanted) {
				t.Errorf("output = %q, want %q left alone", errOut.String(), unwanted)
			}
		}
	})

	// The instance being created cannot be its own stranded predecessor, even
	// if it appears in the listing between the lookup and the list.
	t.Run("the instance itself is skipped", func(t *testing.T) {
		client := incustest.New()
		client.AddInstance(&incus.Instance{
			Name:   "dev-example-project",
			Status: "Running",
			Config: map[string]string{managedProjectKey: "example-project"},
		})

		errOut := &bytes.Buffer{}
		app := NewApp(AppOptions{
			Config: cfg, Client: client, Runner: &runnertest.Fake{},
			Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
		})
		app.warnStrandedInstances(context.Background())

		if errOut.String() != "" {
			t.Errorf("output = %q, want nothing said about the instance itself", errOut.String())
		}
	})
}

// An instance made before the volume record still has its volumes attached as
// disk devices, so 'destroy --volumes' can find them there. Falling back to
// the declaration alone means the flag silently deletes nothing for exactly
// the volumes the user can no longer name.
func TestDestroyVolumesFindsThemOnAnInstanceWithNoRecord(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.Volumes["default/dev-example-project-data"] = true
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		// No user.incus-dev.volumes: this instance predates it.
		Config: map[string]string{managedProjectKey: "example-project"},
		Devices: map[string]incus.Device{
			"data": {"type": "disk", "pool": "default",
				"source": "dev-example-project-data", "path": "/data"},
			"workspace": {"type": "disk", "source": "/home/u/src/example", "path": "/workspace"},
		},
	})

	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Destroy(context.Background(), DestroyOptions{Volumes: true}); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if !slices.Contains(client.Calls, "volume delete default dev-example-project-data") {
		t.Errorf("calls = %v, want the volume deleted", client.Calls)
	}
	// The workspace is a host path, not a volume.
	for _, c := range client.Calls {
		if strings.HasPrefix(c, "volume delete") && strings.Contains(c, "workspace") {
			t.Errorf("calls = %v, want the workspace bind mount left alone", client.Calls)
		}
	}
}

// status must not present the declared image as the one the instance was made
// from when it has no record of it.
func TestStatusDoesNotInventTheImageOfAnOlderInstance(t *testing.T) {
	cfg := mustParse(t, rootYAML)

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:   "dev-example-project",
		Status: "Running",
		Config: map[string]string{managedProjectKey: "example-project"},
	})

	out := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: out, ErrOut: &bytes.Buffer{}, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Status(context.Background(), false); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !strings.Contains(out.String(), "not recorded") {
		t.Errorf("status =\n%s\nwant the image row to say it is not recorded", out.String())
	}
}

// An instance from before idev recorded what it applied cannot have its old
// config and devices told apart from ones set by hand, so idev leaves them.
// The first up is the last moment anything can say so: it writes the records,
// and from then on those settings are outside them for good.
func TestUpNamesWhatItCannotManageOnAnOlderInstance(t *testing.T) {
	cfg := mustParse(t, rootYAML+"  config:\n    limits.memory: 2GiB\n")

	client := incustest.New()
	client.AddInstance(&incus.Instance{
		Name:     "dev-example-project",
		Status:   "Running",
		Profiles: []string{"default"},
		// No user.incus-dev.managed or .devices: this instance predates them.
		Config: map[string]string{
			managedProjectKey: "example-project",
			"limits.memory":   "1GiB", // declared, so idev takes it over
			"limits.cpu":      "2",    // not declared: from an older dev.yml, or by hand
			// Incus's own, which the user did not set and cannot act on.
			"image.os":            "Alpine",
			"volatile.base_image": "abc123",
		},
		Devices: map[string]incus.Device{
			"workspace": {"type": "disk", "source": "/home/u/src/example", "path": "/workspace"},
			"extradisk": {"type": "disk", "source": "/srv/x", "path": "/x"},
		},
	})

	errOut := &bytes.Buffer{}
	app := NewApp(AppOptions{
		Config: cfg, Client: client, Runner: &runnertest.Fake{},
		Out: &bytes.Buffer{}, ErrOut: errOut, CheckIDMap: func(int, int) error { return nil },
	})

	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	got := errOut.String()
	for _, want := range []string{"limits.cpu", "extradisk"} {
		if !strings.Contains(got, want) {
			t.Errorf("output = %q, want it to name %q", got, want)
		}
	}
	// Declared things are managed from now on, so they are not in the list.
	if strings.Contains(got, "limits.memory") {
		t.Errorf("output = %q, want the declared key left out", got)
	}
	for _, unwanted := range []string{"image.os", "volatile."} {
		if strings.Contains(got, unwanted) {
			t.Errorf("output = %q, want %q left out; Incus set it", got, unwanted)
		}
	}
	// And it is said once: the next run has the records.
	errOut.Reset()
	if err := app.Up(context.Background(), UpOptions{}); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	if strings.Contains(errOut.String(), "limits.cpu") {
		t.Errorf("second run = %q, want it said only while it could be", errOut.String())
	}
}
