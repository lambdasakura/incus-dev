package cli

import (
	"bytes"
	"context"
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
		managedRestartKey: recordRestart(started, []string{"security.nesting"}),
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
		managedRestartKey: recordRestart(time.Now().Add(-time.Hour), []string{"security.nesting"}),
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
	record := recordRestart(started, []string{"raw.idmap"})

	got := pendingRestart(map[string]string{managedRestartKey: record}, started)

	if len(got) != 1 || got[0] != "raw.idmap" {
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
