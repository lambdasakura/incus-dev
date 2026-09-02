package cli

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

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
